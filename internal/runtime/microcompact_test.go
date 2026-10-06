package runtime

// Tests for the loop's microcompaction pass (T3.3): the pressure gate appends
// one durable MicrocompactMessage marker to the live list, the request view
// shows placeholders for the evicted results, and a second pass is sticky —
// already-cleared results are never re-evicted.

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
)

func collectAgentEvents(t *testing.T, stream *LoopEventStream) []agentcore.AgentEvent {
	t.Helper()
	var out []agentcore.AgentEvent
	for ev := range stream.Events() {
		out = append(out, ev)
	}
	return out
}

func fatReadTurns(n, chars int) agentcore.MessageList {
	var msgs agentcore.MessageList
	for i := 0; i < n; i++ {
		id := "call-" + string(rune('a'+i))
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.ToolCallContent{Type: "toolCall", ID: id, Name: "read", Arguments: []byte(`{"path":"f.go"}`)},
			},
			StopReason: agentcore.StopReasonToolUse,
		})
		msgs = append(msgs, agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: id,
			ToolName:   "read",
			Content:    agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", chars))},
		})
	}
	return msgs
}

func TestMaybeMicrocompactPressureAppendsMarker(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 20_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000},
	}}
	// autoLine 18k; micro line min(16.2k, 16k) = 16k. 8 fat read turns ≈ 2k
	// tokens each ⇒ ~16k+ estimated ⇒ pressure fires.
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 8000)}
	before := len(agentCtx.Messages)

	var events []agentcore.AgentEvent
	maybeMicrocompact(context.Background(), agentCtx, &cfg, func(ev agentcore.AgentEvent) error {
		events = append(events, ev)
		return nil
	})

	if len(agentCtx.Messages) != before+1 {
		t.Fatalf("microcompact must append exactly one marker, %d -> %d", before, len(agentCtx.Messages))
	}
	marker, ok := agentCtx.Messages[len(agentCtx.Messages)-1].(agentcore.MicrocompactMessage)
	if !ok {
		t.Fatalf("last message should be a MicrocompactMessage, got %T", agentCtx.Messages[len(agentCtx.Messages)-1])
	}
	if len(marker.ClearedCallIDs) == 0 || marker.SavedTokens <= 0 {
		t.Fatalf("marker should record evictions: %+v", marker)
	}
	var found *agentcore.MicrocompactEvent
	for i := range events {
		if e, ok := events[i].(agentcore.MicrocompactEvent); ok {
			found = &e
		}
	}
	if found == nil || found.ClearedCount != len(marker.ClearedCallIDs) {
		t.Fatalf("expected a MicrocompactEvent matching the marker, got %+v", events)
	}

	// The request view carries placeholders for the evicted results.
	view := compaction.ProjectView(agentCtx.Messages)
	for _, m := range view {
		if tr, ok := m.(agentcore.ToolResultMessage); ok {
			for _, id := range marker.ClearedCallIDs {
				if tr.ToolCallID == id {
					text := agentcore.ContentToText(tr.Content)
					if !strings.Contains(text, "cleared to reduce context") || len(text) > 300 {
						t.Fatalf("cleared result %s should be a placeholder, got %q", id, text)
					}
				}
			}
		}
	}
}

func TestMaybeMicrocompactStickySecondPass(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 20_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000},
	}}
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 8000)}
	emit := func(_ agentcore.AgentEvent) error { return nil }
	maybeMicrocompact(context.Background(), agentCtx, &cfg, emit)
	first := len(agentCtx.Messages)

	// Second pass on the same context: everything evictable is already covered
	// by the sticky marker, so no new marker lands.
	maybeMicrocompact(context.Background(), agentCtx, &cfg, emit)
	if len(agentCtx.Messages) != first {
		t.Fatalf("second pass must not append another marker (%d -> %d)", first, len(agentCtx.Messages))
	}
}

func TestMaybeMicrocompactNoopBelowLine(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 200_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 16_384, KeepRecentTokens: 20_000},
	}}
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(3, 500)}
	before := len(agentCtx.Messages)
	emit := func(_ agentcore.AgentEvent) error { return nil }
	maybeMicrocompact(context.Background(), agentCtx, &cfg, emit)
	if len(agentCtx.Messages) != before {
		t.Fatalf("low-pressure context must stay untouched")
	}
}
