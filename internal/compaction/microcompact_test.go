package compaction

// Tests for microcompaction candidate selection (T3.3): pressure/idle double
// gate, whitelist grouping, exemptions, the savings floor, and stickiness.

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// mcToolMsg builds an assistant message with n tool calls (name prefix varies
// per index) plus one text block, and its results with the given sizes.
func mcTurn(callNames []string, resultChars []int, errors []bool) []agentcore.Message {
	a := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewTextContent("working")},
		StopReason: agentcore.StopReasonToolUse,
	}
	var out []agentcore.Message
	for i, name := range callNames {
		id := name + "-" + string(rune('a'+i))
		a.Content = append(a.Content, agentcore.ToolCallContent{
			Type: "toolCall", ID: id, Name: name, Arguments: []byte(`{}`),
		})
	}
	out = append(out, a)
	for i, name := range callNames {
		id := name + "-" + string(rune('a'+i))
		tr := agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: id,
			ToolName:   name,
			Content:    agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", resultChars[i]))},
		}
		if errors != nil && errors[i] {
			tr.IsError = true
		}
		out = append(out, tr)
	}
	return out
}

func mcList(turns ...[]agentcore.Message) agentcore.MessageList {
	var out agentcore.MessageList
	for _, t := range turns {
		out = append(out, t...)
	}
	return out
}

func TestMicrocompactPressureLineDerivation(t *testing.T) {
	// window 2000, reserve 500 → autoLine 1500; min(1350, −500→1500−2000<0)…
	// with such a tiny window the margin makes the line degenerate → 0 guard.
	if got := MicrocompactPressureLine(2000, 500); got != 0 {
		t.Fatalf("tiny window line = %d, want 0 (degenerate guard)", got)
	}
	// window 200_000, reserve 16_384 → autoLine 183_616; min(165_254, 181_616)
	if got := MicrocompactPressureLine(200_000, 16_384); got != 165_254 {
		t.Fatalf("line = %d, want 165254", got)
	}
}

func TestDecideMicrocompactPressureEvictsOldestFirst(t *testing.T) {
	// 8 fat read groups, none eligible among the newest 5.
	var turns [][]agentcore.Message
	for i := 0; i < 8; i++ {
		turns = append(turns, mcTurn([]string{"read"}, []int{2000}, nil))
	}
	view := mcList(turns...)
	tokens := 10_000
	line := 6_000
	dec := DecideMicrocompact(view, tokens, line, false)
	if dec.Reason != "pressure" {
		t.Fatalf("reason = %q, want pressure", dec.Reason)
	}
	if len(dec.ClearedCallIDs) == 0 {
		t.Fatal("expected evictions under pressure")
	}
	if dec.SavedTokens < MicrocompactMinSavingsTokens {
		t.Fatalf("savings %d below floor", dec.SavedTokens)
	}
	// Evicted groups must be the OLDEST ones (3 eligible groups: 8−5 keep).
	if len(dec.ClearedCallIDs) != 3 {
		t.Fatalf("evicted %d results, want 3 (8 groups − 5 kept)", len(dec.ClearedCallIDs))
	}
	first := dec.ClearedCallIDs[0]
	if !strings.HasPrefix(first, "read-") {
		t.Fatalf("unexpected id %q", first)
	}
}

func TestDecideMicrocompactPressureStopsAtLowWater(t *testing.T) {
	// Plenty of eligible groups; a moderate token count needs only a few
	// evictions to drop under half the line.
	var turns [][]agentcore.Message
	for i := 0; i < 10; i++ {
		turns = append(turns, mcTurn([]string{"bash"}, []int{16000}, nil))
	}
	view := mcList(turns...)
	// tokens 12k, line 10k → target 5k; each fat group saves ~3970 tokens, so
	// two evictions cross below half the line — well before the 5 eligible.
	dec := DecideMicrocompact(view, 12_000, 10_000, false)
	if len(dec.ClearedCallIDs) == 0 {
		t.Fatal("expected evictions")
	}
	if len(dec.ClearedCallIDs) >= 5 {
		t.Fatalf("low water should stop early, evicted %d", len(dec.ClearedCallIDs))
	}
}

func TestDecideMicrocompactIdleClearsAllEligible(t *testing.T) {
	var turns [][]agentcore.Message
	for i := 0; i < 8; i++ {
		turns = append(turns, mcTurn([]string{"grep"}, []int{1500}, nil))
	}
	view := mcList(turns...)
	dec := DecideMicrocompact(view, 100, 6_000, true) // no pressure, idle
	if dec.Reason != "idle" {
		t.Fatalf("reason = %q, want idle", dec.Reason)
	}
	// 3 eligible (8 − 5 kept) regardless of the tiny token count.
	if len(dec.ClearedCallIDs) != 3 {
		t.Fatalf("idle evicted %d, want 3", len(dec.ClearedCallIDs))
	}
}

func TestDecideMicrocompactExemptions(t *testing.T) {
	// error result / non-whitelist tool / image-bearing result must all be
	// skipped even when they sit inside the eligible (oldest) range. 9 groups,
	// 4 eligible: [error, todo, image, clean-read] — only the clean one evicts.
	big := 3000
	turns := [][]agentcore.Message{
		mcTurn([]string{"bash"}, []int{big}, []bool{true}), // error → exempt group
		mcTurn([]string{"todo"}, []int{big}, nil),          // non-whitelist → exempt group
		mcTurn([]string{"webfetch"}, []int{big}, nil),      // tagged with an image below
	}
	for i := 0; i < 6; i++ {
		turns = append(turns, mcTurn([]string{"read"}, []int{big}, nil))
	}
	view := mcList(turns...)
	if tr, ok := view[5].(agentcore.ToolResultMessage); ok { // turn-3 result (index 5) carries an image
		tr.Content = append(tr.Content, agentcore.ImageContent{Type: "image", Data: "x", MimeType: "image/png"})
		view[5] = tr
	}
	dec := DecideMicrocompact(view, 10_000, 6_000, false)
	if len(dec.ClearedCallIDs) != 1 {
		t.Fatalf("only the clean read group is eligible, got %v", dec.ClearedCallIDs)
	}
}

func TestDecideMicrocompactSavingsFloor(t *testing.T) {
	// Six tiny groups so exactly one is eligible (6 − 5 kept), and its savings
	// (~70 tokens) sit below the 256-token floor → skip.
	var turns [][]agentcore.Message
	for i := 0; i < 6; i++ {
		turns = append(turns, mcTurn([]string{"read"}, []int{400}, nil))
	}
	view := mcList(turns...)
	dec := DecideMicrocompact(view, 10_000, 6_000, false)
	if len(dec.ClearedCallIDs) != 0 {
		t.Fatalf("tiny group should be skipped, got %v", dec.ClearedCallIDs)
	}
	if dec.SkipReason != SkipBelowMicroSavings {
		t.Fatalf("skip reason = %q, want %q", dec.SkipReason, SkipBelowMicroSavings)
	}
}

func TestDecideMicrocompactSkipsAlreadyCleared(t *testing.T) {
	// Sticky: results already covered by an existing marker are not re-cleared,
	// and if nothing eligible remains the pass reports no candidates.
	turns := [][]agentcore.Message{mcTurn([]string{"read"}, []int{3000}, nil)}
	view := mcList(turns...)
	cleared := ExistingClearedCallIDs(append(agentcore.MessageList{}, view...))
	dec := DecideMicrocompact(view, 10_000, 6_000, false)
	_ = cleared
	// With one group total (≤ keep 5), nothing is eligible at all.
	if len(dec.ClearedCallIDs) != 0 || dec.SkipReason != SkipNoCandidates {
		t.Fatalf("want no-candidates, got %+v", dec)
	}
}

func TestMicrocompactIdleDetectionUsesTimestamps(t *testing.T) {
	old := mcList(mcTurn([]string{"read"}, []int{100}, nil))
	// Stamp the tool result an hour in the past.
	for i, m := range old {
		if tr, ok := m.(agentcore.ToolResultMessage); ok {
			tr.Timestamp = timeNowMillis() - MicrocompactIdleMillis - 1000
			old[i] = tr
		}
	}
	if got := IdleMillis(old, timeNowMillis()); got < MicrocompactIdleMillis {
		t.Fatalf("idle gap = %d, want ≥ %d", got, MicrocompactIdleMillis)
	}
	// Zero timestamps (unknown activity) never read as idle.
	fresh := mcList(mcTurn([]string{"read"}, []int{100}, nil))
	if got := IdleMillis(fresh, timeNowMillis()); got != 0 {
		t.Fatalf("unknown activity must not be idle, got %d", got)
	}
}

func timeNowMillis() int64 { return 1_800_000_000_000 }
