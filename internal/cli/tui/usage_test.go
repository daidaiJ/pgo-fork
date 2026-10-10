package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/statline"
)

// newTestUsageRecorder opens a usage recorder over root (the session store's
// directory), which the test then binds to the session via Options.Usage.
func newTestUsageRecorder(t *testing.T, root string) *runtime.UsageRecorder {
	t.Helper()
	return runtime.NewUsageRecorder(root)
}

// TestSlashUsageRendersSessionLedger drives /usage end to end through the TUI's
// slash path: the executor hook reads the session usage ledger and the outcome
// renders as a system block (O1/T7.3c).
func TestSlashUsageRendersSessionLedger(t *testing.T) {
	store := newTestStore(t)
	rec := newTestUsageRecorder(t, store.Dir())
	s, _, err := newRunSessionWithStore(store, Options{Model: "usage-model", ProviderName: "p", Usage: rec})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if err := statline.AppendUsage(store.Dir(), s.header.ID,
		statline.Record{At: time.Now().UTC(), Model: "usage-model", Input: 1200, Output: 90, CacheRead: 1000, TTFTMs: 500, DurationMs: 2000},
		statline.Record{At: time.Now().UTC(), Model: "usage-model", Input: 800, Output: 60, Subagent: true, AgentID: "call-1"},
	); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}

	m := NewModel(Options{}).withSession(s, nil)
	got, _ := m.runSlash("/usage")
	blocks := blockTexts(got.(Model).transcript)
	if len(blocks) == 0 {
		t.Fatal("no transcript blocks after /usage")
	}
	out := strings.Join(blocks, "\n")
	for _, want := range []string{"session usage", "✓ 2", "in 2.0K", "subagents"} {
		if !strings.Contains(out, want) {
			t.Errorf("/usage block missing %q; got:\n%s", want, out)
		}
	}
}

// TestSlashStatsRendersLedgerWindows drives /stats: the window argument selects
// the range over every session ledger under the store, and the report renders as
// a system block.
func TestSlashStatsRendersLedgerWindows(t *testing.T) {
	store := newTestStore(t)
	rec := newTestUsageRecorder(t, store.Dir())
	s, _, err := newRunSessionWithStore(store, Options{Model: "usage-model", ProviderName: "p", Usage: rec})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	now := time.Now().UTC()
	if err := statline.AppendUsage(store.Dir(), s.header.ID,
		statline.Record{At: now.Add(-2 * time.Hour), Model: "recent", Input: 1000, Output: 100},
		statline.Record{At: now.Add(-4 * 24 * time.Hour), Model: "old", Input: 5000, Output: 500},
	); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}

	m := NewModel(Options{}).withSession(s, nil)
	got, _ := m.runSlash("/stats day")
	out := strings.Join(blockTexts(got.(Model).transcript), "\n")
	if !strings.Contains(out, "last 24h") || !strings.Contains(out, "recent") {
		t.Fatalf("/stats day should report the recent model; got:\n%s", out)
	}
	if strings.Contains(out, "old") {
		t.Errorf("/stats day should exclude the 4-day-old record; got:\n%s", out)
	}

	got, _ = NewModel(Options{}).withSession(s, nil).runSlash("/stats all")
	out = strings.Join(blockTexts(got.(Model).transcript), "\n")
	if !strings.Contains(out, "all time") || !strings.Contains(out, "old") {
		t.Errorf("/stats all should include the whole ledger; got:\n%s", out)
	}
}
