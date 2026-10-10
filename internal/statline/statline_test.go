package statline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

func rec(input, output, cacheRead int) Record {
	return Record{At: time.Now().UTC(), Input: input, Output: output, CacheRead: cacheRead}
}

func TestAggregateIsCumulative(t *testing.T) {
	recs := []Record{
		rec(1000, 100, 0),
		rec(2000, 200, 1500),
		{At: time.Now().UTC(), Err: true},
	}
	s := Aggregate(recs)
	if s.Calls != 3 || s.Errors != 1 {
		t.Fatalf("calls/errors = %d/%d, want 3/1", s.Calls, s.Errors)
	}
	if s.OK() != 2 {
		t.Errorf("OK = %d, want 2", s.OK())
	}
	if s.Input != 3000 || s.Output != 300 || s.CacheRead != 1500 {
		t.Errorf("tokens = in %d out %d cache %d, want 3000/300/1500", s.Input, s.Output, s.CacheRead)
	}
	// The first call is exempt (cold prefix); the failed call never read a
	// prompt; the second call has a cache read, so no miss at all.
	if s.CacheMisses != 0 {
		t.Errorf("cache misses = %d, want 0", s.CacheMisses)
	}
}

func TestAggregateCountsCacheMissesBeyondFirst(t *testing.T) {
	recs := []Record{
		rec(1000, 10, 0),   // first: exempt
		rec(1100, 11, 0),   // miss
		rec(1200, 12, 900), // hit
		rec(1300, 13, 0),   // miss
	}
	if got := Aggregate(recs).CacheMisses; got != 2 {
		t.Errorf("cache misses = %d, want 2", got)
	}
}

func TestAggregateSubagentAttribution(t *testing.T) {
	recs := []Record{
		rec(100, 10, 0),
		{At: time.Now().UTC(), Subagent: true, AgentID: "call-1", Input: 200, Output: 20, Err: true, Incomplete: true},
		{At: time.Now().UTC(), Subagent: true, AgentID: "call-2", Input: 300, Output: 30},
	}
	s := Aggregate(recs)
	if s.SubagentCalls != 2 || s.SubagentErrors != 1 || s.SubagentIncomplete != 1 {
		t.Fatalf("subagent = %d/%d/%d, want 2/1/1", s.SubagentCalls, s.SubagentErrors, s.SubagentIncomplete)
	}
	if s.Input != 600 {
		t.Errorf("input = %d, want 600 (sub-agent tokens count toward the session)", s.Input)
	}
}

func TestAggregatePerfIsLastTurn(t *testing.T) {
	recs := []Record{
		{At: time.Now().UTC(), Output: 100, TTFTMs: 500, DurationMs: 5000, Model: "m1"},
		{At: time.Now().UTC(), Output: 200, TTFTMs: 800, DurationMs: 3000, Model: "m2"},
	}
	s := Aggregate(recs)
	if s.LastTTFTMs != 800 || s.LastDurationMs != 3000 || s.LastOutput != 200 || s.LastModel != "m2" {
		t.Fatalf("last = %+v, want the second record's perf", s)
	}
	tps, ok := s.LastTPS()
	if !ok {
		t.Fatal("LastTPS not ok")
	}
	// generation window = 3000-800 = 2200ms → 200 tokens / 2.2s ≈ 90.9
	if tps < 90 || tps > 92 {
		t.Errorf("LastTPS = %.1f, want ≈90.9", tps)
	}
}

func TestDerivedSegmentsHideWhenUnknown(t *testing.T) {
	var s Stats
	if _, ok := s.CachePercent(); ok {
		t.Error("empty stats reported a cache share")
	}
	if _, ok := s.ThinkPercent(); ok {
		t.Error("empty stats reported a think share")
	}
	if _, ok := s.LastTPS(); ok {
		t.Error("empty stats reported a rate")
	}
	s = Stats{Input: 100, CacheRead: 300}
	pct, ok := s.CachePercent()
	if !ok || pct != 75 {
		t.Errorf("cache share = %v/%v, want 75/true", pct, ok)
	}
	s = Stats{ThinkChars: 60, StreamChars: 40}
	pct, ok = s.ThinkPercent()
	if !ok || pct != 60 {
		t.Errorf("think share = %v/%v, want 60/true", pct, ok)
	}
}

func TestFromTurnFoldsUsageAndErrors(t *testing.T) {
	m := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Model:      "claude-x",
		Provider:   "anthropic",
		StopReason: agentcore.StopReasonEndTurn,
		Usage:      &agentcore.Usage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 8},
	}
	r := FromTurn(m, Timing{TTFT: 1200 * time.Millisecond, Duration: 4 * time.Second, ThinkChars: 7, StreamChars: 9, Retries: 1}, "fallback", "p")
	if r.Model != "claude-x" || r.Provider != "anthropic" {
		t.Errorf("model/provider = %s/%s, want the message's values", r.Model, r.Provider)
	}
	if r.Input != 10 || r.Output != 2 || r.CacheRead != 8 {
		t.Errorf("usage = %+v, want the message's buckets", r)
	}
	if r.TTFTMs != 1200 || r.DurationMs != 4000 || r.Retries != 1 {
		t.Errorf("timing = %+v, want 1200/4000/1", r)
	}
	if r.Err {
		t.Error("end_turn turn marked as an error")
	}

	// A failed turn with no usage payload and no message model falls back to
	// the request's resolved values and is marked Err.
	failed := FromTurn(agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonError}, Timing{}, "req-model", "req-provider")
	if !failed.Err {
		t.Error("error turn not marked Err")
	}
	if failed.Model != "req-model" || failed.Provider != "req-provider" {
		t.Errorf("fallback = %s/%s, want req-model/req-provider", failed.Model, failed.Provider)
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	root := t.TempDir()
	const id = "20261010-120000-000001"
	if err := AppendUsage(root, id, rec(10, 1, 0), rec(20, 2, 15)); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	if err := AppendUsage(root, id, rec(30, 3, 25)); err != nil {
		t.Fatalf("AppendUsage (second): %v", err)
	}
	got, err := LoadUsage(root, id)
	if err != nil {
		t.Fatalf("LoadUsage: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("records = %d, want 3", len(got))
	}
	if got[2].Input != 30 || got[2].CacheRead != 25 {
		t.Errorf("third record = %+v, want the appended values", got[2])
	}
}

func TestLedgerMissingAndEmptyAreInert(t *testing.T) {
	root := t.TempDir()
	recs, err := LoadUsage(root, "nope")
	if err != nil || recs != nil {
		t.Fatalf("missing ledger = %v/%v, want nil/nil", recs, err)
	}
	if recs, err := LoadUsage("", "nope"); err != nil || recs != nil {
		t.Fatalf("empty root = %v/%v, want nil/nil", recs, err)
	}
	if err := AppendUsage("", "nope", rec(1, 1, 0)); err != nil {
		t.Fatalf("empty-root append = %v, want nil (no store to write to)", err)
	}
}

func TestLedgerTornTailDroppedButMidfileCorruptionReported(t *testing.T) {
	root := t.TempDir()
	const id = "20261010-130000-000002"
	if err := AppendUsage(root, id, rec(10, 1, 0)); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	path := UsagePath(root, id)
	// A torn final line (a crash between bytes and newline) is dropped.
	if err := os.WriteFile(path, []byte(`{"input":1,"output":1}`+"\n"+`{"input":2,"outp`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadUsage(root, id)
	if err != nil || len(got) != 1 {
		t.Fatalf("torn tail = %d records, err %v; want 1/nil", len(got), err)
	}
	// Damage followed by a good line is corruption, not truncation.
	if err := os.WriteFile(path, []byte(`{"input":1,"output":1}`+"\n"+`{oops`+"\n"+`{"input":3,"output":3}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUsage(root, id); err == nil {
		t.Fatal("mid-file corruption loaded cleanly, want an error")
	}
}

func TestUsageFileNameIsNotSessionFile(t *testing.T) {
	if UsageFileName("s1") != "s1.usage.jsonl" {
		t.Fatalf("UsageFileName = %q", UsageFileName("s1"))
	}
	if !IsUsageFileName(UsageFileName("s1")) {
		t.Error("IsUsageFileName rejected its own name")
	}
	if IsUsageFileName("s1.jsonl") {
		t.Error("IsUsageFileName accepted a session file")
	}
	if got, want := UsagePath("root", "s1"), filepath.Join("root", "s1.usage.jsonl"); got != want {
		t.Fatalf("UsagePath = %q, want %q", got, want)
	}
}
