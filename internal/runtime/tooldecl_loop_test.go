package runtime

// End-to-end tests for the deferred tool declaration machinery (T4.1): the
// declared face is filtered per the plan, the announcement rides the request as
// an ephemeral reminder, a search_tools claim enters the face on the NEXT
// request, the persisted ToolClaimMessage record lands in the live list, and
// the executor gate blocks an unclaimed call with the search_tools guidance.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/tooldecl"
)

func declPlan(deferred ...string) *tooldecl.Plan {
	p := tooldecl.Plan{Deferred: map[string]tooldecl.ToolInfo{}}
	for _, n := range deferred {
		p.Deferred[n] = tooldecl.ToolInfo{Name: n, Description: "the " + n + " tool", Source: "plugin"}
	}
	return &p
}

// toolNamesOf returns the tool names the request declared.
func toolNamesOf(req provider.CompletionRequest) []string {
	out := make([]string, 0, len(req.Context.Tools))
	for _, t := range req.Context.Tools {
		out = append(out, t.Name())
	}
	return out
}

func hasTool(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func TestToolDeclarationFaceAnnouncementAndClaim(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("s1", "search_tools", `{"query":"plug_a"}`),
			toolCallTurn("p1", "plug_a", `{}`),
			textTurn("done"),
		},
	}
	ran := false
	plug := execTool{
		name: "plug_a",
		mode: agentcore.ToolExecutionParallel,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			ran = true
			return agentcore.AgentToolResult{}, nil
		},
	}
	cfg := newFauxRunCfg(p, echoTool("echo", agentcore.ToolExecutionParallel, false),
		plug, &agenttool.SearchToolsTool{})
	cfg.ToolDeclaration = declPlan("plug_a")
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}},
		Tools: []agentcore.AgentTool{
			echoTool("echo", agentcore.ToolExecutionParallel, false),
			plug,
			&agenttool.SearchToolsTool{},
		},
	}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if p.callCount() != 3 {
		t.Fatalf("provider called %d times, want 3", p.callCount())
	}
	// Request 1: plug_a withheld; the announcement lists it; search_tools stays.
	face := toolNamesOf(p.requestAt(0))
	if hasTool(face, "plug_a") {
		t.Errorf("request 1 must not declare plug_a, got %v", face)
	}
	if !hasTool(face, "search_tools") || !hasTool(face, "echo") {
		t.Errorf("request 1 must declare echo + search_tools, got %v", face)
	}
	if body := requestText(p.requestAt(0)); !strings.Contains(body, "Additional tools are available but not loaded") ||
		!strings.Contains(body, "- plug_a") {
		t.Errorf("request 1 must carry the announcement listing plug_a, got %q", body)
	}
	// Request 2 (after the claim): plug_a joins the face, announcement gone.
	face = toolNamesOf(p.requestAt(1))
	if !hasTool(face, "plug_a") {
		t.Errorf("claim must enter the face on the next request, got %v", face)
	}
	if body := requestText(p.requestAt(1)); strings.Contains(body, "Additional tools are available") {
		t.Errorf("fully-claimed state must drop the announcement, got %q", body)
	}
	// The deferred tool actually executed once it was claimed.
	if !ran {
		t.Error("plug_a must execute after its claim")
	}
	// The durable claim record sits in the live list (resume replay source).
	claims := 0
	var claimed []string
	for _, m := range agentCtx.Messages {
		if c, ok := m.(agentcore.ToolClaimMessage); ok {
			claims++
			claimed = c.Tools
		}
	}
	if claims != 1 || len(claimed) != 1 || claimed[0] != "plug_a" {
		t.Errorf("want exactly one ToolClaimMessage for [plug_a], got %d records %v", claims, claimed)
	}
	// The claim record never reaches a request view.
	for i := 0; i < p.callCount(); i++ {
		if body := requestText(p.requestAt(i)); strings.Contains(body, "toolClaim") {
			t.Errorf("request %d must not render the claim record", i)
		}
	}
}

func TestToolDeclarationGateBlocksUnclaimed(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("p1", "plug_a", `{}`),
			textTurn("recovered"),
		},
	}
	ran := false
	plug := execTool{
		name: "plug_a",
		mode: agentcore.ToolExecutionParallel,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			ran = true
			return agentcore.AgentToolResult{}, nil
		},
	}
	cfg := newFauxRunCfg(p, plug, &agenttool.SearchToolsTool{})
	cfg.ToolDeclaration = declPlan("plug_a")
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}},
		Tools:    []agentcore.AgentTool{plug, &agenttool.SearchToolsTool{}},
	}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if ran {
		t.Error("the gate must block an unclaimed deferred call")
	}
	// The model gets the kimi guidance path instead of an execution.
	blocked := false
	for _, m := range agentCtx.Messages {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID == "p1" {
			if tr.IsError && strings.Contains(agentcore.ContentToText(tr.Content), "search_tools") {
				blocked = true
			}
		}
	}
	if !blocked {
		t.Error("blocked call must carry the search_tools guidance in an error result")
	}
}

func TestToolDeclarationResumeReplayRestoresFace(t *testing.T) {
	// Resume atomicity (spec §2.4): history carries a persisted claim record;
	// the first request already declares the claimed tool — no re-search.
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("p1", "plug_a", `{}`),
			textTurn("done"),
		},
	}
	plug := echoTool("plug_a", agentcore.ToolExecutionParallel, false)
	cfg := newFauxRunCfg(p, plug, &agenttool.SearchToolsTool{})
	cfg.ToolDeclaration = declPlan("plug_a")
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser},
			agentcore.ToolClaimMessage{RoleField: agentcore.RoleToolClaim, Tools: []string{"plug_a"}, Timestamp: 1},
		},
		Tools: []agentcore.AgentTool{plug, &agenttool.SearchToolsTool{}},
	}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	face := toolNamesOf(p.requestAt(0))
	if !hasTool(face, "plug_a") {
		t.Errorf("resume must restore the claimed tool into the first request's face, got %v", face)
	}
	if body := requestText(p.requestAt(0)); strings.Contains(body, "Additional tools are available") {
		t.Errorf("restored claims produce no announcement, got %q", body)
	}
}

func TestToolDeclarationNilPlanIsInert(t *testing.T) {
	// No plan: today's behavior byte-for-byte — full face, no announcement, no gate.
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("p1", "plug_a", `{}`),
			textTurn("done"),
		},
	}
	ran := false
	plug := execTool{
		name: "plug_a",
		mode: agentcore.ToolExecutionParallel,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			ran = true
			return agentcore.AgentToolResult{}, nil
		},
	}
	cfg := newFauxRunCfg(p, plug)
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}},
		Tools:    []agentcore.AgentTool{plug},
	}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if !ran {
		t.Error("without a plan the tool executes as before")
	}
	if body := requestText(p.requestAt(0)); strings.Contains(body, "Additional tools are available") {
		t.Error("without a plan there is no announcement")
	}
}
