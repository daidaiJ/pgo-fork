package compaction

// Tests for the Pipeline orchestration (T3.3.1): the microcompaction pass
// semantics (pressure/idle gates, sticky markers, residency revocation) and
// the post-compaction one-shot reminder incl. its snapshot rendering. Moved
// with the code from internal/runtime (behavior-preserving T3.3.1 move); the
// loop-level wiring tests stay on the runtime side driving agentLoop.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// fatReadTurns returns n assistant read turns each carrying a fat `chars`
// result, the canonical microcompaction pressure fixture.
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

// bigUserMessages returns n user messages each carrying `chars` characters,
// used to inflate estimated context tokens past a small window.
func bigUserMessages(n, chars int) agentcore.MessageList {
	body := strings.Repeat("x", chars)
	msgs := make(agentcore.MessageList, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(body)},
		})
	}
	return msgs
}

// padSummary pads text with filler lines until it clears the 500-char
// degenerate-summary floor (T3.3), so test fixtures resemble real summaries.
func padSummary(text string) string {
	const floor = 600
	if len(text) >= floor {
		return text
	}
	filler := "\n- filler context line for the test fixture"
	for len(text) < floor {
		text += filler
	}
	return text
}

// summarizer builds a SummarizeFunc backed by a scripted summary stream — the
// same fixture shape the runtime loop tests use for cfg.SummaryStream. The
// text is padded past the degenerate-summary floor, which real summaries
// always clear.
func summarizer(text string) SummarizeFunc {
	stream := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		msg := agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			StopReason: agentcore.StopReasonEndTurn,
			Content:    agentcore.ContentList{agentcore.NewTextContent(padSummary(text))},
		}
		s := provider.NewAssistantMessageEventStream(0)
		go func() { _ = s.Emit(ctx, provider.StreamDoneEvent{Message: msg}); s.Close() }()
		return s, nil
	}
	settings := CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}
	return func(ctx context.Context, view agentcore.MessageList, prevIdx int, prevSummary string, prevDetails *CompactionDetails) (*CompactionResult, error) {
		return Compact(ctx, stream, provider.Model{Provider: "test", ID: "test"}, view, settings, prevIdx, prevDetails, prevSummary, provider.StreamConfig{})
	}
}

// newPipeline builds a test pipeline (standard settings triple
// window/reserve/keep, an optional event collector, optional Summarizer).
func newPipeline(win, reserve, keep int, events *[]agentcore.AgentEvent, summarize SummarizeFunc) *Pipeline {
	return NewPipeline(PipelineConfig{
		Settings:      CompactionSettings{Enabled: true, ReserveTokens: reserve, KeepRecentTokens: keep},
		ContextWindow: win,
		Model:         func() string { return "" },
		Emit: func(ev agentcore.AgentEvent) error {
			if events != nil {
				*events = append(*events, ev)
			}
			return nil
		},
		Now:       func() int64 { return time.Now().UnixMilli() },
		Summarize: summarize,
	})
}

func TestMicrocompactPressureAppendsMarker(t *testing.T) {
	var events []agentcore.AgentEvent
	p := newPipeline(20_000, 2_000, 1_000, &events, nil)
	// autoLine 18k; micro line min(16.2k, 16k) = 16k. 8 fat read turns ≈ 2k
	// tokens each ⇒ ~16k+ estimated ⇒ pressure fires.
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 8000)}
	before := len(agentCtx.Messages)

	p.RequestView(context.Background(), agentCtx)

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
	view := ProjectView(agentCtx.Messages)
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

func TestMicrocompactStickySecondPass(t *testing.T) {
	p := newPipeline(20_000, 2_000, 1_000, nil, nil)
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 8000)}
	p.RequestView(context.Background(), agentCtx)
	first := len(agentCtx.Messages)

	// Second pass on the same context: everything evictable is already covered
	// by the sticky marker, so no new marker lands.
	p.RequestView(context.Background(), agentCtx)
	if len(agentCtx.Messages) != first {
		t.Fatalf("second pass must not append another marker (%d -> %d)", first, len(agentCtx.Messages))
	}
}

func TestMicrocompactNoopBelowLine(t *testing.T) {
	p := newPipeline(200_000, 16_384, 20_000, nil, nil)
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(3, 500)}
	before := len(agentCtx.Messages)
	p.RequestView(context.Background(), agentCtx)
	if len(agentCtx.Messages) != before {
		t.Fatalf("low-pressure context must stay untouched")
	}
}

func TestPostCompactReminderSetAndConsumed(t *testing.T) {
	p := newPipeline(2_000, 500, 100, nil, summarizer("## Goal\ncompacted"))
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

	p.AfterTurn(context.Background(), agentCtx)

	if p.postCompactReminder == "" {
		t.Fatal("expected a post-compaction reminder after a compaction with reads")
	}
	if !strings.Contains(p.postCompactReminder, "a.go") || !strings.Contains(p.postCompactReminder, "<system-reminder>") {
		t.Fatalf("reminder should list a.go inside system-reminder tags: %q", p.postCompactReminder)
	}
	// One-shot: the first take clears it.
	if got := p.TakePostCompactReminder(); got == "" {
		t.Fatal("first take should return the reminder")
	}
	if got := p.TakePostCompactReminder(); got != "" {
		t.Fatalf("reminder must be one-shot, got %q", got)
	}
}

func TestPostCompactReminderEmptyWithoutReads(t *testing.T) {
	p := newPipeline(2_000, 500, 100, nil, summarizer("## Goal\ncompacted"))
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(8, 600)}

	p.AfterTurn(context.Background(), agentCtx)

	if p.postCompactReminder != "" {
		t.Fatalf("no reads in range ⇒ no reminder, got %q", p.postCompactReminder)
	}
}

func TestMicrocompactRevokesReadResidency(t *testing.T) {
	// #4239 closure (T3.5): evicting a file's read results withdraws the file's
	// residency — the next edit on it is refused until the model re-reads —
	// while files whose reads stay in the kept tail keep it.
	p := newPipeline(20_000, 2_000, 1_000, nil, nil)
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

	p.RequestView(context.Background(), agentCtx)

	if !agentCtx.ReadFiles.Residency("/w/b.go") {
		t.Fatal("b.go reads stay in the kept tail — residency must survive")
	}
	if agentCtx.ReadFiles.Residency("/w/a.go") {
		t.Fatal("a.go reads were evicted — residency must be revoked")
	}
}

func TestMicrocompactUnmappableReadRevokesAll(t *testing.T) {
	p := newPipeline(20_000, 2_000, 1_000, nil, nil)
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 8000), ReadFiles: agentcore.NewReadFileState()}
	// A ledger entry whose read call is NOT one of the evicted ids — recorded
	// before the ledger existed (restored session). Any evicted read that
	// cannot be reverse-mapped must revoke everything.
	agentCtx.ReadFiles.RecordRead(agentcore.ReadRecord{
		CallID: "ancient-1", ArgPath: "g.go", ResolvedPath: "/w/g.go",
		Content: "ggg", ModTime: time.Now(), Size: 100,
	})
	p.RequestView(context.Background(), agentCtx)
	if agentCtx.ReadFiles.Residency("/w/g.go") {
		t.Fatal("unmappable evicted read must trigger the revoke-all branch")
	}
}

func TestIdleGateFiresWithStampedTimestamp(t *testing.T) {
	p := newPipeline(20_000, 2_000, 1_000, nil, nil)
	// Low pressure (8 thin groups ≈ 1.2k tokens ≪ 16k line) but the newest
	// message is two hours old → the idle branch must evict everything
	// eligible (3 groups × ~95 tokens ≥ 256 floor).
	msgs := fatReadTurns(8, 500)
	last := msgs[len(msgs)-1].(agentcore.ToolResultMessage)
	last.Timestamp = time.Now().Add(-2 * time.Hour).UnixMilli()
	msgs[len(msgs)-1] = last
	agentCtx := &agentcore.AgentContext{Messages: msgs}
	before := len(agentCtx.Messages)

	p.RequestView(context.Background(), agentCtx)

	if len(agentCtx.Messages) != before+1 {
		t.Fatal("two-hour-old newest message must open the idle gate and append a marker")
	}
	marker := agentCtx.Messages[len(agentCtx.Messages)-1].(agentcore.MicrocompactMessage)
	if len(marker.ClearedCallIDs) == 0 {
		t.Fatalf("expected an idle-window eviction, got %+v", marker)
	}
}

func TestIdleGateSilentWithoutTimestamps(t *testing.T) {
	// The production status quo ante: no message carries a timestamp → the
	// idle gate must stay closed (never idle on unknowns), and a low-pressure
	// context is left untouched.
	p := newPipeline(20_000, 2_000, 1_000, nil, nil)
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 500)}
	before := len(agentCtx.Messages)
	p.RequestView(context.Background(), agentCtx)
	if len(agentCtx.Messages) != before {
		t.Fatal("zero timestamps must never open the idle gate")
	}
}

func reminderRangeMsgs(paths ...string) []agentcore.Message {
	msgs := []agentcore.Message{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
	}
	for i, p := range paths {
		args := `{"path":"` + p + `"}`
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("reading"),
				agentcore.ToolCallContent{Type: "toolCall", ID: string(rune('a'+i)), Name: "read", Arguments: []byte(args)},
			},
			StopReason: agentcore.StopReasonToolUse,
		})
	}
	return msgs
}

func TestPostCompactReminderCarriesSnapshots(t *testing.T) {
	st := agentcore.NewReadFileState()
	st.RecordRead(agentcore.ReadRecord{
		CallID: "a", ArgPath: "a.go", ResolvedPath: "/w/a.go",
		Content: "package a\n\nfunc A() {}\n", ModTime: time.Now(), Size: 22,
	})
	// b.go was read through the tool but its snapshot is absent (e.g. restored
	// session): the reminder degrades that one entry to a reference line.
	st.RecordRead(agentcore.ReadRecord{
		CallID: "b", ArgPath: "b.go", ResolvedPath: "/w/b.go",
		Content: "", ModTime: time.Now(), Size: 5,
	})

	body := PostCompactReminderText(reminderRangeMsgs("a.go", "b.go"), st)
	if !strings.Contains(body, "<system-reminder>") {
		t.Fatal("reminder must stay wrapped")
	}
	if !strings.Contains(body, "package a\n\nfunc A() {}") {
		t.Errorf("snapshot content for a.go must be injected, got %q", body)
	}
	if !strings.Contains(body, "b.go (no content snapshot; re-read if needed)") {
		t.Errorf("b.go without a snapshot must degrade to a reference line, got %q", body)
	}
}

func TestPostCompactReminderPerFileCap(t *testing.T) {
	st := agentcore.NewReadFileState()
	st.RecordRead(agentcore.ReadRecord{
		CallID: "a", ArgPath: "big.go", ResolvedPath: "/w/big.go",
		Content: strings.Repeat("x", 6*1024), // below the 8K snapshot cap, above the 5K injection cap
		ModTime: time.Now(), Size: 6 * 1024,
	})
	body := PostCompactReminderText(reminderRangeMsgs("big.go"), st)
	if !strings.Contains(body, "… (snapshot truncated)") {
		t.Errorf("oversized snapshot must be truncated with a visible mark, got %d chars", len(body))
	}
	if len(body) > reminderMaxFileChars+1024 {
		t.Errorf("injected snapshot must respect the per-file cap, got %d chars", len(body))
	}
}

func TestPostCompactReminderNilLedgerKeepsReferenceForm(t *testing.T) {
	// No ledger (standalone use, or a driver that never ran the loop): the
	// original path-reference wording must be preserved verbatim.
	body := PostCompactReminderText(reminderRangeMsgs("a.go"), nil)
	if !strings.Contains(body, "whose exact contents may no longer be in context") {
		t.Errorf("nil ledger must keep the reference-hint form, got %q", body)
	}
	if !strings.Contains(body, "- a.go") {
		t.Errorf("reference form must list the path, got %q", body)
	}
	if strings.Contains(body, "package a") {
		t.Error("no content must be injected without a ledger")
	}
}
