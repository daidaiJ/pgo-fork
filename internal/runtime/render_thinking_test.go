package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// thinkingAssistant builds an assistant message carrying a thinking block plus
// optional reply text, the shape reasoning-model providers stream.
func thinkingAssistant(thinking, text string) agentcore.AssistantMessage {
	content := agentcore.ContentList{}
	if thinking != "" {
		content = append(content, agentcore.NewThinkingContent(thinking))
	}
	if text != "" {
		content = append(content, agentcore.NewTextContent(text))
	}
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: content}
}

// TestDrainStreamThinkingDeltas verifies OnThinking receives only the new
// suffix of the streaming thinking region — tracked independently from OnText,
// since reasoning and reply interleave as separate content blocks within one
// message.
func TestDrainStreamThinkingDeltas(t *testing.T) {
	ctx := context.Background()
	stream := emitStream(func(s *LoopEventStream) {
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: thinkingAssistant("thi", "")})
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: thinkingAssistant("thinking ", "re")})
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: thinkingAssistant("thinking done", "reply")})
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: thinkingAssistant("thinking done", "reply")})
		s.SetResult([]agentcore.AgentMessage{thinkingAssistant("thinking done", "reply")})
	})

	var thought, text strings.Builder
	final, err := DrainStream(ctx, stream, StreamHandler{
		OnThinking: func(delta string) { thought.WriteString(delta) },
		OnText:     func(delta string) { text.WriteString(delta) },
	})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if got := thought.String(); got != "thinking done" {
		t.Errorf("thinking deltas = %q, want %q", got, "thinking done")
	}
	if got := text.String(); got != "reply" {
		t.Errorf("text deltas = %q, want %q", got, "reply")
	}
	if final == nil || agentcore.ContentToThinking(final.Content) != "thinking done" {
		t.Errorf("final message thinking = %v, want %q", final, "thinking done")
	}
}

// TestDrainStreamThinkingTurnEndFlush covers a provider that never streams
// thinking and only delivers the full message at turn end: the final thinking
// must reach OnThinking exactly once, and the per-turn counter must reset so a
// second turn's thinking does not get swallowed.
func TestDrainStreamThinkingTurnEndFlush(t *testing.T) {
	ctx := context.Background()
	stream := emitStream(func(s *LoopEventStream) {
		// Turn 1: thinking only arrives at turn end.
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: thinkingAssistant("first reasoning", "ok")})
		// Turn 2: streams the same thinking region it ends with.
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: thinkingAssistant("second ", "")})
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: thinkingAssistant("second reasoning", "")})
		s.SetResult([]agentcore.AgentMessage{thinkingAssistant("second reasoning", "")})
	})

	var thought strings.Builder
	if _, err := DrainStream(ctx, stream, StreamHandler{
		OnThinking: func(delta string) { thought.WriteString(delta) },
	}); err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if got := thought.String(); got != "first reasoningsecond reasoning" {
		t.Errorf("thinking deltas across turns = %q, want %q",
			got, "first reasoningsecond reasoning")
	}
}
