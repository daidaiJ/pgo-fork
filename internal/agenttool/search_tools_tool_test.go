package agenttool

// Tests for the search_tools claim path (T4.1): scoring → claim → persisted
// ToolClaimMessage → result text, plus the repeat-call and no-match paths.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/tooldecl"
)

func searchRequest(t *testing.T, query string) (agentcore.AgentToolResult, *agentcore.AgentContext) {
	t.Helper()
	plan := tooldecl.Plan{Deferred: map[string]tooldecl.ToolInfo{
		"plug_a": {Name: "plug_a", Description: "the alpha tool"},
		"plug_b": {Name: "plug_b", Description: "the beta tool"},
	}}
	st := tooldecl.NewState(plan)
	tool := &SearchToolsTool{}
	tool.Bind(st)
	agentCtx := &agentcore.AgentContext{}
	ctx := agentcore.WithAgentContext(context.Background(), agentCtx)
	args, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	res, rerr := tool.Execute(ctx, "call-1", args, nil)
	if rerr != nil {
		t.Fatalf("Execute: %v", rerr)
	}
	return res, agentCtx
}

func claimRecords(agentCtx *agentcore.AgentContext) []agentcore.ToolClaimMessage {
	var out []agentcore.ToolClaimMessage
	for _, m := range agentCtx.Messages {
		if c, ok := m.(agentcore.ToolClaimMessage); ok {
			out = append(out, c)
		}
	}
	return out
}

func TestSearchToolsClaimsMatches(t *testing.T) {
	res, agentCtx := searchRequest(t, "plug_a")
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "plug_a") || strings.Contains(text, "plug_b") {
		t.Errorf("only plug_a should be claimed, got %q", text)
	}
	records := claimRecords(agentCtx)
	if len(records) != 1 || len(records[0].Tools) != 1 || records[0].Tools[0] != "plug_a" {
		t.Fatalf("want one claim record for [plug_a], got %+v", records)
	}
	if records[0].RoleField != agentcore.RoleToolClaim {
		t.Errorf("record role = %q", records[0].RoleField)
	}
}

func TestSearchToolsEmptyMatchIsNotError(t *testing.T) {
	res, agentCtx := searchRequest(t, "zzz_nope")
	if res.Details == nil {
		t.Error("empty match should still shape a result")
	}
	if len(claimRecords(agentCtx)) != 0 {
		t.Error("no match must not claim anything")
	}
	// Spec验收 2: 未命中返回空集不报错 — the miss is reported as a normal
	// result, not an error result (IsError is never set on the content path).
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "No tools matched") {
		t.Errorf("miss should state the miss, got %q", text)
	}
}

func TestSearchToolsRepeatClaimNoop(t *testing.T) {
	plan := tooldecl.Plan{Deferred: map[string]tooldecl.ToolInfo{
		"plug_a": {Name: "plug_a"},
	}}
	st := tooldecl.NewState(plan)
	tool := &SearchToolsTool{}
	tool.Bind(st)
	agentCtx := &agentcore.AgentContext{}
	ctx := agentcore.WithAgentContext(context.Background(), agentCtx)
	args := json.RawMessage(`{"query":"plug_a"}`)
	if _, rerr := tool.Execute(ctx, "c1", args, nil); rerr != nil {
		t.Fatal(rerr)
	}
	res, rerr := tool.Execute(ctx, "c2", args, nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "Already loaded: plug_a") {
		t.Errorf("repeat claim should report already loaded, got %q", text)
	}
	if records := claimRecords(agentCtx); len(records) != 1 {
		t.Errorf("repeat claim must not append a second record, got %d", len(records))
	}
}

func TestSearchToolsUnboundReportsInactive(t *testing.T) {
	tool := &SearchToolsTool{}
	res, rerr := tool.Execute(context.Background(), "c", json.RawMessage(`{"query":"x"}`), nil)
	if rerr != nil {
		t.Fatalf("Execute: %v", rerr)
	}
	if !strings.Contains(agentcore.ContentToText(res.Content), "not active") {
		t.Errorf("unbound tool must report inactive, got %q", agentcore.ContentToText(res.Content))
	}
}
