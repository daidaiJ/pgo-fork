package runtime

// Tests for the T4.4 余项 dynamic max_tokens stamp: when the model's output cap
// is known and the window is resolved, each request carries
// min(cap, max(floor, window − estimate − margin − thinking)) in the stream
// Extra hint; without a cap (or an unknown window) the wire shape is unchanged
// and the shared Extra map is never mutated.

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
)

// captureExtra returns a StreamFn that records the request's StreamConfig.Extra
// and ends the turn with a plain text message.
func captureExtra(got *map[string]any) provider.StreamFn {
	return func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		*got = cfg.Extra
		msg := agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			StopReason: agentcore.StopReasonEndTurn,
			Content:    agentcore.ContentList{agentcore.NewTextContent("ok")},
		}
		s := provider.NewAssistantMessageEventStream(0)
		go func() { _ = s.Emit(ctx, provider.StreamDoneEvent{Message: msg}); s.Close() }()
		return s, nil
	}
}

// noopEmit is an EmitFunc that drops every event.
var noopEmit agentcore.EmitFunc = func(context.Context, agentcore.AgentEvent) error { return nil }

func TestDynamicMaxTokensStamp(t *testing.T) {
	var got map[string]any
	cfg := LoopConfig{
		Model:    "m",
		Provider: "p",
		Stream:   captureExtra(&got),
		// Window 100K, cap 32K: the estimate is ~25K tokens, so the
		// window-derived bound (100000 − 25000 − 2048) exceeds the cap and the
		// configured cap wins.
		ContextWindow:   100_000,
		MaxOutputTokens: 32_768,
		Compaction:      compaction.DefaultCompactionSettings,
	}
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{
			agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", 100_000))},
			},
		},
	}
	if _, err := streamAssistantResponse(context.Background(), agentCtx, cfg, noopEmit, nil); err != nil {
		t.Fatalf("streamAssistantResponse: %v", err)
	}
	if v, ok := got["max_tokens"].(int); !ok || v != 32_768 {
		t.Errorf("stamped max_tokens = %v, want 32768", got["max_tokens"])
	}
}

func TestDynamicMaxTokensStampDerivedBinds(t *testing.T) {
	var got map[string]any
	cfg := LoopConfig{
		Model:    "m",
		Provider: "p",
		Stream:   captureExtra(&got),
		// ~95K-token estimate against a 100K window: the derived bound
		// (100000 − 95000 − 2048 = 2952) sinks below the output floor and the
		// floor wins; it also binds under the configured cap.
		ContextWindow:   100_000,
		MaxOutputTokens: 32_768,
		Compaction:      compaction.DefaultCompactionSettings,
	}
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{
			agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", 380_000))},
			},
		},
	}
	if _, err := streamAssistantResponse(context.Background(), agentCtx, cfg, noopEmit, nil); err != nil {
		t.Fatalf("streamAssistantResponse: %v", err)
	}
	if v, ok := got["max_tokens"].(int); !ok || v != compaction.OutputFloorTokens {
		t.Errorf("stamped max_tokens = %v, want %d", got["max_tokens"], compaction.OutputFloorTokens)
	}
}

func TestDynamicMaxTokensNoStampWithoutCap(t *testing.T) {
	var got map[string]any
	shared := map[string]any{"sentinel": 1}
	cfg := LoopConfig{
		Model:    "m",
		Provider: "p",
		Stream:   captureExtra(&got),
		// Cap unknown (0) → no stamp; the shared Extra map passes through
		// untouched (same identity, no max_tokens key added).
		ContextWindow: 100_000,
		Extra:         shared,
		Compaction:    compaction.DefaultCompactionSettings,
	}
	agentCtx := &agentcore.AgentContext{
		Messages: agentcore.MessageList{
			agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", 100_000))},
			},
		},
	}
	if _, err := streamAssistantResponse(context.Background(), agentCtx, cfg, noopEmit, nil); err != nil {
		t.Fatalf("streamAssistantResponse: %v", err)
	}
	if _, ok := got["max_tokens"]; ok {
		t.Errorf("unexpected max_tokens stamp without a cap: %v", got["max_tokens"])
	}
	if _, ok := got["sentinel"]; !ok {
		t.Error("shared Extra sentinel missing: the passthrough map was replaced")
	}
	if len(shared) != 1 {
		t.Errorf("shared Extra mutated: %v", shared)
	}
}
