package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/statline"
	"github.com/smallnest/pigo/internal/testenv"
)

func TestWriteUsageReportRendersSessionTallies(t *testing.T) {
	now := time.Now().UTC()
	recs := []statline.Record{
		{At: now, Model: "claude", Input: 40000, Output: 400, CacheRead: 38000, TTFTMs: 700, DurationMs: 4000, ThinkChars: 300, StreamChars: 700},
		{At: now, Model: "claude", Input: 2000, Output: 200, CacheRead: 0, Err: true, Incomplete: true},
		{At: now, Model: "claude", Input: 1500, Output: 100, CacheRead: 0, Subagent: true, AgentID: "call-9", TTFTMs: 900, DurationMs: 3000},
	}
	var b bytes.Buffer
	WriteUsageReport(&b, statline.Aggregate(recs), UsageReportOptions{SessionID: "sess-1", Model: "claude"})
	out := b.String()
	for _, want := range []string{
		"session usage · claude", "sess-1",
		"✓ 2", "✗ 1", // one successful sub-agent turn + the primary, one failure
		"cache misses 1",
		"in 44K", "out 700",
		"cache 46.6%", "think 30.0%",
		"ttft", "tok/s",
		"subagents", "1 calls",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage report missing %q; got:\n%s", want, out)
		}
	}
}

func TestWriteUsageReportEmptySession(t *testing.T) {
	var b bytes.Buffer
	WriteUsageReport(&b, statline.Stats{}, UsageReportOptions{SessionID: "sess-empty"})
	if out := b.String(); !strings.Contains(out, "no accounted turns yet") {
		t.Errorf("empty session should render the dim notice; got:\n%s", out)
	}
}

func TestWriteStatsReportWindowsAndModels(t *testing.T) {
	now := time.Now().UTC()
	recs := []statline.Record{
		{At: now.Add(-2 * time.Hour), Model: "claude", Input: 1000, Output: 100, CacheRead: 900},
		{At: now.Add(-3 * 24 * time.Hour), Model: "gpt", Input: 2000, Output: 200},
	}
	var b bytes.Buffer
	WriteStatsReport(&b, recs, runtime.UsageWindowWeek, now)
	out := b.String()
	for _, want := range []string{"last 7 days", "2 turns", "claude", "gpt", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats report missing %q; got:\n%s", want, out)
		}
	}
	// The day window drops the three-day-old record.
	b.Reset()
	WriteStatsReport(&b, recs, runtime.UsageWindowDay, now)
	out = b.String()
	if !strings.Contains(out, "last 24h") || !strings.Contains(out, "claude") {
		t.Fatalf("day window should keep the recent model; got:\n%s", out)
	}
	if strings.Contains(out, "gpt") {
		t.Errorf("day window should exclude the 3-day-old record; got:\n%s", out)
	}
	// An empty window renders a notice, not an empty table.
	b.Reset()
	WriteStatsReport(&b, nil, runtime.UsageWindowAll, now)
	if out := b.String(); !strings.Contains(out, "no accounted turns in this window") {
		t.Errorf("empty ledger should render the notice; got:\n%s", out)
	}
}

func TestUsageLedgerLoadsEverySessionSorted(t *testing.T) {
	root := testenv.Dir(t)
	now := time.Now().UTC()
	if err := statline.AppendUsage(root, "s1", statline.Record{At: now.Add(-time.Hour), Input: 1}); err != nil {
		t.Fatal(err)
	}
	if err := statline.AppendUsage(root, "s2", statline.Record{At: now, Input: 2}, statline.Record{At: now.Add(-2 * time.Hour), Input: 3}); err != nil {
		t.Fatal(err)
	}
	// A stray non-ledger file must be ignored, not break the walk.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := UsageLedger(root)
	if len(got) != 3 {
		t.Fatalf("records = %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.Before(got[i-1].At) {
			t.Fatalf("records not sorted by time: %+v", got)
		}
	}
	if UsageLedger("") != nil {
		t.Error("empty root should yield nil")
	}
}
