// Tests for the runaway sentinel wiring (T3.2): the provider adapter over
// internal/runaway, and the injection path through the reminder registry onto
// a real loop request. Detection semantics are covered in the runaway package.
package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// runawayStreakMsgs builds a history whose trailing steps repeat one call with
// one identical result, with a user turn anchoring the window.
func runawayStreakMsgs(streak int) agentcore.MessageList {
	msgs := agentcore.MessageList{agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("go")},
	}}
	for i := range streak {
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

func TestRunawayProviderFiresOnStreak(t *testing.T) {
	p := &RunawayReminderProvider{}
	body, ok := p.Reminder(t.Context(), runawayStreakMsgs(3))
	if !ok || body == "" {
		t.Fatal("three identical calls with identical results must fire")
	}
	if !strings.Contains(body, "runaway guard") {		t.Errorf("reminder body should carry the sentinel marker, got %q", body)
	}
}

func TestRunawayProviderSilentOnVariedResults(t *testing.T) {
	p := &RunawayReminderProvider{}
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
			Content:    agentcore.ContentList{agentcore.NewTextContent("result " + string(rune('a'+i)))},
		})
	}
	if body, ok := p.Reminder(t.Context(), msgs); ok {
		t.Errorf("changing results must stay silent, got %q", body)
	}
}

// TestRunawayReminderInjectedEphemeral drives a real turn through the loop with
// the sentinel registered: the model's request carries the wrapped reminder,
// and the persisted history stays clean.
func TestRunawayReminderInjectedEphemeral(t *testing.T) {
	reg := NewReminderRegistry(&RunawayReminderProvider{})
	agentCtx := &agentcore.AgentContext{Messages: runawayStreakMsgs(3)}

	seen := reminderTextsInRequest(t, reg, agentCtx)
	found := false
	for _, s := range seen {
		if strings.Contains(s, "runaway guard") && strings.Contains(s, "<system-reminder>") {
			found = true
		}
	}
	if !found {
		t.Fatalf("sentinel reminder must reach the model request, saw %v", seen)
	}
	for _, m := range agentCtx.Messages {
		if um, ok := m.(agentcore.UserMessage); ok && strings.Contains(agentcore.ContentToText(um.Content), "runaway guard") {
			t.Errorf("reminder leaked into persisted message history: %+v", um)
		}
	}
}
