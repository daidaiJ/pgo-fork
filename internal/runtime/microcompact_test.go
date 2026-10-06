package runtime

// Tests for the loop's microcompaction pass (T3.3): the pressure gate appends
// one durable MicrocompactMessage marker to the live list, the request view
// shows placeholders for the evicted results, and a second pass is sticky —
// already-cleared results are never re-evicted.

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestPostCompactReminderSetAndConsumed(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 2_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100},
	}}
	cfg.SummaryStream = summaryStream(padSummary("## Goal\ncompacted"))
	// Summarized range: a read of a.go, then filler; kept tail plain.
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("reading"),
				agentcore.ToolCallContent{Type: "toolCall", ID: "c1", Name: "read", Arguments: []byte(`{"path":"a.go"}`)},
			},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "c1",
			ToolName:   "read",
			Content:    agentcore.ContentList{agentcore.NewTextContent("package a")},
		},
	}}
	agentCtx.Messages = append(agentCtx.Messages, bigUserMessages(6, 1600)...)
	cmp := &compactor{}
	var events []agentcore.AgentEvent
	emit := func(_ agentcore.AgentEvent) error { return nil }
	_ = events
	maybeAutoCompact(context.Background(), agentCtx, &cfg, emit, nil, cmp)
	if cmp.postCompactReminder == "" {
		t.Fatal("expected a post-compaction reminder after a compaction with reads")
	}
	if !strings.Contains(cmp.postCompactReminder, "a.go") || !strings.Contains(cmp.postCompactReminder, "<system-reminder>") {
		t.Fatalf("reminder should list a.go inside system-reminder tags: %q", cmp.postCompactReminder)
	}
	// One-shot: the first take clears it.
	if got := cmp.takePostCompactReminder(); got == "" {
		t.Fatal("first take should return the reminder")
	}
	if got := cmp.takePostCompactReminder(); got != "" {
		t.Fatalf("reminder must be one-shot, got %q", got)
	}
}

func TestPostCompactReminderEmptyWithoutReads(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 2_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100},
	}}
	cfg.SummaryStream = summaryStream(padSummary("## Goal\ncompacted"))
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(8, 600)}
	cmp := &compactor{}
	emit := func(_ agentcore.AgentEvent) error { return nil }
	maybeAutoCompact(context.Background(), agentCtx, &cfg, emit, nil, cmp)
	if cmp.postCompactReminder != "" {
		t.Fatalf("no reads in range ⇒ no reminder, got %q", cmp.postCompactReminder)
	}
}

func TestMaybeMicrocompactRevokesReadResidency(t *testing.T) {
	// #4239 closure (T3.5): evicting a file's read results withdraws the file's
	// residency — the next edit on it is refused until the model re-reads —
	// while files whose reads stay in the kept tail keep it.
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 20_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000},
	}}
	// Four fat a.go turns (oldest, all four evicted — at 40000 chars the
	// low-water loop keeps taking groups until the fourth) + five thin b.go
	// turns (newest 5 of 9 groups, never candidates). ≈40.4k ≥ micro line 16k
	// ⇒ pressure.
	var msgs agentcore.MessageList
	for i := 0; i < 4; i++ {
		id := "call-a" + string(rune('0'+i))
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.ToolCallContent{Type: "toolCall", ID: id, Name: "read", Arguments: []byte(`{"path":"a.go"}`)},
			},
			StopReason: agentcore.StopReasonToolUse,
		})
		msgs = append(msgs, agentcore.ToolResultMessage{
			RoleField: agentcore.RoleToolResult, ToolCallID: id, ToolName: "read",
			Content: agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", 40000))},
		})
	}
	for i := 0; i < 5; i++ {
		id := "call-b" + string(rune('0'+i))
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.ToolCallContent{Type: "toolCall", ID: id, Name: "read", Arguments: []byte(`{"path":"b.go"}`)},
			},
			StopReason: agentcore.StopReasonToolUse,
		})
		msgs = append(msgs, agentcore.ToolResultMessage{
			RoleField: agentcore.RoleToolResult, ToolCallID: id, ToolName: "read",
			Content: agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("y", 500))},
		})
	}
	agentCtx := &agentcore.AgentContext{Messages: msgs, ReadFiles: agentcore.NewReadFileState()}
	for i := 0; i < 4; i++ {
		agentCtx.ReadFiles.RecordRead(agentcore.ReadRecord{
			CallID: "call-a" + string(rune('0'+i)), ArgPath: "a.go", ResolvedPath: "/w/a.go",
			Content: "aaa", ModTime: time.Now(), Size: 40000,
		})
	}
	for i := 0; i < 5; i++ {
		agentCtx.ReadFiles.RecordRead(agentcore.ReadRecord{
			CallID: "call-b" + string(rune('0'+i)), ArgPath: "b.go", ResolvedPath: "/w/b.go",
			Content: "bbb", ModTime: time.Now(), Size: 500,
		})
	}

	maybeMicrocompact(context.Background(), agentCtx, &cfg, func(_ agentcore.AgentEvent) error { return nil })

	if !agentCtx.ReadFiles.Residency("/w/b.go") {
		t.Fatal("b.go reads stay in the kept tail — residency must survive")
	}
	if agentCtx.ReadFiles.Residency("/w/a.go") {
		t.Fatal("a.go reads were evicted — residency must be revoked")
	}
}

func TestMaybeMicrocompactUnmappableReadRevokesAll(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 20_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000},
	}}
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 8000), ReadFiles: agentcore.NewReadFileState()}
	// A ledger entry whose read call is NOT one of the evicted ids — recorded
	// before the ledger existed (restored session). Any evicted read that
	// cannot be reverse-mapped must revoke everything.
	agentCtx.ReadFiles.RecordRead(agentcore.ReadRecord{
		CallID: "ancient-1", ArgPath: "g.go", ResolvedPath: "/w/g.go",
		Content: "ggg", ModTime: time.Now(), Size: 100,
	})
	maybeMicrocompact(context.Background(), agentCtx, &cfg, func(_ agentcore.AgentEvent) error { return nil })
	if agentCtx.ReadFiles.Residency("/w/g.go") {
		t.Fatal("unmappable evicted read must trigger the revoke-all branch")
	}
}
