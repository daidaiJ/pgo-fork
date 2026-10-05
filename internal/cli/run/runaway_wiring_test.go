package run

// Tests for the runaway sentinel wiring (T3.2): WithRunawayGuard registers the
// sentinel provider on the per-turn reminder registry every assembled run
// carries, so a stuck model gets the ephemeral nudge on every driver (REPL,
// TUI, headless, SDK, task children, goal loop).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runtime"
)

// runawayMsgs builds a history whose trailing steps repeat one call with one
// identical result — the pattern the sentinel must catch.
func runawayMsgs() agentcore.MessageList {
	msgs := agentcore.MessageList{agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("go")},
	}}
	for i := range 3 {
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{agentcore.ToolCallContent{
				Type: agentcore.ContentTypeToolCall, ID: string(rune('a' + i)), Name: "read",
				Arguments: json.RawMessage(`{"path":"x.go"}`),
			}},
			StopReason: agentcore.StopReasonToolUse,
		}, agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: string(rune('a' + i)),
			ToolName:   "read",
			Content:    agentcore.ContentList{agentcore.NewTextContent("same")},
		})
	}
	return msgs
}

func TestWithRunawayGuardRegistersSentinel(t *testing.T) {
	reg := WithRunawayGuard(nil)
	if reg == nil || reg.Empty() {
		t.Fatal("WithRunawayGuard(nil) must yield a non-empty registry")
	}
	rem := reg.Messages(context.Background(), runawayMsgs())
	if len(rem) != 1 {
		t.Fatalf("sentinel must inject one reminder on a stuck tail, got %d", len(rem))
	}
	if text := agentcore.ContentToText(rem[0].(agentcore.UserMessage).Content); !strings.Contains(text, "runaway guard") {
		t.Errorf("reminder text should carry the sentinel marker, got %q", text)
	}
}

func TestWithRunawayGuardKeepsExistingProviders(t *testing.T) {
	reg := WithRunawayGuard(runtime.NewReminderRegistry())
	// The wrapped registry still consults its original providers (none here)
	// plus the sentinel: only the sentinel fires on a stuck tail.
	if rem := reg.Messages(context.Background(), runawayMsgs()); len(rem) != 1 {
		t.Fatalf("wrapped registry must keep exactly one firing provider here, got %d", len(rem))
	}
	// Registration is idempotent: wrapping again must not double-inject.
	reg = WithRunawayGuard(reg)
	if rem := reg.Messages(context.Background(), runawayMsgs()); len(rem) != 1 {
		t.Fatalf("double wrap must not inject the sentinel twice, got %d", len(rem))
	}
}
