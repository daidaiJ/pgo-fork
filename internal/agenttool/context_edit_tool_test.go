package agenttool

// Tests for the context_edit tool (T3.4): per-edit validation and rejection
// reporting, marker appending through the loop-injected AgentContext, and the
// registry schema boundary.

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func ctxEditFixture() *agentcore.AgentContext {
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser,
			Content: agentcore.ContentList{agentcore.NewTextContent("big paste")}},
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
			Content:    agentcore.ContentList{agentcore.NewTextContent("output")},
		},
	}}
	return agentCtx
}

func runCtxEdit(t *testing.T, agentCtx *agentcore.AgentContext, args string) (agentcore.AgentToolResult, error) {
	t.Helper()
	tool := &ContextEditTool{}
	ctx := agentcore.WithAgentContext(context.Background(), agentCtx)
	return tool.Execute(ctx, "call-ce", []byte(args), nil)
}

func TestContextEditToolReplaceByCallIDAppendsMarker(t *testing.T) {
	agentCtx := ctxEditFixture()
	res, err := runCtxEdit(t, agentCtx, `{"edits":[{"tool_call_id":"c1","mode":"replace","digest":"the listing"}]}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "Applied 1") {
		t.Fatalf("result should report the applied edit, got %q", text)
	}
	marker, ok := agentCtx.Messages[len(agentCtx.Messages)-1].(agentcore.ContextEditMessage)
	if !ok {
		t.Fatalf("last message should be the edit marker, got %T", agentCtx.Messages[len(agentCtx.Messages)-1])
	}
	if len(marker.Edits) != 1 || marker.Edits[0].TargetCallID != "c1" ||
		marker.Edits[0].Mode != agentcore.ModeReplace || marker.Edits[0].Digest != "the listing" {
		t.Fatalf("marker edits wrong: %+v", marker.Edits)
	}
	// The raw target stays verbatim.
	if tr := agentCtx.Messages[2].(agentcore.ToolResultMessage); agentcore.ContentToText(tr.Content) != "output" {
		t.Fatalf("raw history must stay untouched, got %q", agentcore.ContentToText(tr.Content))
	}
}

func TestContextEditToolSeqTargetingAndMixedReport(t *testing.T) {
	agentCtx := ctxEditFixture()
	res, err := runCtxEdit(t, agentCtx, `{"edits":[
		{"seq":0,"mode":"hide"},
		{"tool_call_id":"nope","mode":"replace","digest":"x"},
		{"seq":5,"mode":"hide"},
		{"seq":2,"mode":"replace"},
		{"seq":1,"mode":"hide"}]}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "Applied 1") {
		t.Fatalf("only the seq-0 hide should apply, got %q", text)
	}
	if !strings.Contains(text, "no tool result with call id") || !strings.Contains(text, "out of range") ||
		!strings.Contains(text, "requires a non-empty digest") {
		t.Fatalf("rejections should be reported per edit, got %q", text)
	}
	// seq 1 is the assistant with a tool call — hide must be rejected.
	if !strings.Contains(text, "tool calls") {
		t.Fatalf("hide of an assistant with tool calls must be rejected, got %q", text)
	}
	marker := agentCtx.Messages[len(agentCtx.Messages)-1].(agentcore.ContextEditMessage)
	if len(marker.Edits) != 1 || marker.Edits[0].TargetSeq != 0 || marker.Edits[0].ContentHash == "" {
		t.Fatalf("only the seq-0 hide should apply, got %+v", marker.Edits)
	}
}

func TestContextEditToolAllRejectedAppendsNothing(t *testing.T) {
	agentCtx := ctxEditFixture()
	before := len(agentCtx.Messages)
	res, err := runCtxEdit(t, agentCtx, `{"edits":[{"tool_call_id":"missing","mode":"replace","digest":"x"}]}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "no edit was applicable") {
		t.Fatalf("all-rejected batch should fail loudly, got %q", text)
	}
	if len(agentCtx.Messages) != before {
		t.Fatal("no marker may be appended when nothing applied")
	}
}

func TestContextEditToolWithoutLoopContext(t *testing.T) {
	tool := &ContextEditTool{}
	res, err := tool.Execute(context.Background(), "call-ce", []byte(`{"edits":[{"seq":0,"mode":"hide"}]}`), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "only available inside a running agent loop") {
		t.Fatalf("expected the loop-context guard, got %q", text)
	}
}

func TestContextEditToolSchemaRejectsModelessEdit(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(&ContextEditTool{}); err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("context_edit", []byte(`{"edits":[{"tool_call_id":"c1"}]}`)); len(errs) == 0 {
		t.Fatal("an edit without mode must fail schema validation")
	}
	if errs := reg.Validate("context_edit", []byte(`{"edits":[{"tool_call_id":"c1","mode":"replace","digest":"d"}]}`)); len(errs) != 0 {
		t.Fatalf("a valid call-id replace must validate: %+v", errs)
	}
	if errs := reg.Validate("context_edit", []byte(`{"edits":[]}`)); len(errs) == 0 {
		t.Fatal("an empty edits array must fail schema validation")
	}
}
