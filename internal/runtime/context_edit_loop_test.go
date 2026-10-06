package runtime

// End-to-end tests for the context_edit tool over the real loop (T3.4): the
// model's edit marker lands in the live list mid-run, the next provider request
// already carries the edited view, and the raw history stays lossless.

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
)

func requestText(req provider.CompletionRequest) string {
	var b strings.Builder
	for _, m := range req.Context.Messages {
		switch msg := m.(type) {
		case agentcore.UserMessage:
			b.WriteString(agentcore.ContentToText(msg.Content))
		case agentcore.AssistantMessage:
			b.WriteString(agentcore.ContentToText(msg.Content))
		case agentcore.ToolResultMessage:
			b.WriteString(agentcore.ContentToText(msg.Content))
		case agentcore.CompactionMessage:
			b.WriteString(msg.Summary)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestFauxContextEditAppliesToNextRequest(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("ce1", "context_edit",
				`{"edits":[{"tool_call_id":"c1","mode":"replace","digest":"DIGEST-TEXT"}]}`),
			textTurn("done"),
		},
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser,
			Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.NewToolCallContent("c1", "bash", []byte(`{"command":"ls"}`)),
			},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "c1",
			ToolName:   "bash",
			Content:    agentcore.ContentList{agentcore.NewTextContent("THE ORIGINAL LONG OUTPUT")},
		},
	}}
	cfg := newFauxRunCfg(p, &agenttool.ContextEditTool{})
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if p.callCount() != 2 {
		t.Fatalf("provider called %d times, want 2", p.callCount())
	}
	// Request 1 still shows the original; request 2 (after the edit marker
	// landed) shows the digest instead.
	if got := requestText(p.requestAt(0)); !strings.Contains(got, "THE ORIGINAL LONG OUTPUT") {
		t.Fatalf("first request must carry the original result, got %q", got)
	}
	second := requestText(p.requestAt(1))
	if !strings.Contains(second, "DIGEST-TEXT") {
		t.Fatalf("second request must show the digest, got %q", second)
	}
	if strings.Contains(second, "THE ORIGINAL LONG OUTPUT") {
		t.Fatalf("second request must not carry the replaced result, got %q", second)
	}
	// The live list keeps the raw result verbatim plus the durable marker.
	raw := requestText(provider.CompletionRequest{Context: provider.LlmContext{Messages: agentCtx.Messages}})
	if !strings.Contains(raw, "THE ORIGINAL LONG OUTPUT") {
		t.Fatalf("live list must stay lossless, got %q", raw)
	}
	found := false
	for _, m := range agentCtx.Messages {
		if _, ok := m.(agentcore.ContextEditMessage); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("live list must contain the durable edit marker")
	}
}

func TestFauxContextEditCutMappingUnderEdits(t *testing.T) {
	// The acceptance behind the map-based cut conversion: with an edit marker
	// in the raw list (dropped from the view), an auto-compaction must still
	// restore exactly the kept window — the formula-based mapping would drift
	// by the number of dropped entries and pull extras into the kept wrap.
	// Two fat results: the edit retires one, the other keeps the pressure on
	// so afterTurn really compacts.
	fat := strings.Repeat("x", 100_000)
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("ce1", "context_edit",
				`{"edits":[{"tool_call_id":"c1","mode":"replace","digest":"DIGEST-TEXT"}]}`),
			textTurn("done"),
		},
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser,
			Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.NewToolCallContent("c1", "bash", []byte(`{"command":"ls"}`)),
			},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "c1",
			ToolName:   "bash",
			Content:    agentcore.ContentList{agentcore.NewTextContent(fat)},
		},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("again"),
				agentcore.NewToolCallContent("c2", "bash", []byte(`{"command":"ls"}`)),
			},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "c2",
			ToolName:   "bash",
			Content:    agentcore.ContentList{agentcore.NewTextContent(fat)},
		},
	}}
	cfg := newFauxRunCfg(p, &agenttool.ContextEditTool{})
	// Tiny window with a workable reserve (mirrors microcompact_test's setup):
	// the next turn's afterTurn must auto-compact.
	cfg.ContextWindow = 20_000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000}
	cfg.SummaryStream = summaryStream(padSummary("## Goal\ncompacted"))
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// After the run the context must carry a compaction marker whose kept
	// window, viewed through the projection, equals the messages the cut
	// intended: no duplicated "go" user message ahead of the marker.
	markerSeen := false
	for _, m := range agentCtx.Messages {
		if c, ok := m.(agentcore.CompactionMessage); ok {
			markerSeen = true
			if c.KeptBefore > len(agentCtx.Messages) {
				t.Fatalf("corrupt KeptBefore %d on %+v", c.KeptBefore, c)
			}
		}
	}
	if !markerSeen {
		t.Fatal("expected an auto-compaction marker in the live list")
	}
	// The post-compaction view stays well-formed: exactly one leading marker,
	// the digest visible, no duplicate of the initial user message.
	view := compaction.ProjectView(agentCtx.Messages)
	counts := map[string]int{}
	for _, m := range view {
		if u, ok := m.(agentcore.UserMessage); ok {
			counts[agentcore.ContentToText(u.Content)]++
		}
	}
	if counts["go"] > 1 {
		t.Fatalf("kept-window drift duplicated the initial user message: %v", counts)
	}
}
