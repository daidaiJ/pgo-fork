package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/statline"
	"github.com/smallnest/pigo/internal/usage"
)

// newTestUsageRecorder opens a usage recorder over root (the session store's
// directory), which the test then binds to the session via Options.Usage.
func newTestUsageRecorder(t *testing.T, root string) *runtime.UsageRecorder {
	t.Helper()
	return runtime.NewUsageRecorder(root)
}

// TestSlashUsageRendersSessionLedger drives /usage end to end through the TUI's
// TestSlashUsageOpensPanel drives /usage through the TUI's slash path: the
// declared ProjUsagePanel face opens the overlay at its plan-quota tab, whose
// body is the shared /usage report (session tallies + provider quota), and the
// provider probe runs off the tea loop (T6.3 + T7.3 panel form).
func TestSlashUsageOpensPanel(t *testing.T) {
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
	next, cmd := m.runSlash("/usage")
	mm := next.(Model)
	if !mm.ctxPanel.open || mm.ctxPanel.tab != tabUsage {
		t.Fatalf("/usage should open the panel at the usage tab (open=%v tab=%d)", mm.ctxPanel.open, mm.ctxPanel.tab)
	}
	// The provider is "p": no quota source, so the lookup is refused locally
	// and no probe command is issued (and nothing touches the network).
	if cmd != nil {
		t.Error("/usage issued a probe for a provider with no quota source")
	}
	view, _ := mm.renderContent()
	out := stripANSI(view)
	for _, want := range []string{"用量上限", "session usage", "✓ 2", "in 2.0K", "subagents"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage panel missing %q; got:\n%s", want, out)
		}
	}
	// Tabs cycle: shift+tab returns to the context tab.
	mm.ctxPanel.handleKey("shift+tab")
	if mm.ctxPanel.tab != tabContext {
		t.Errorf("shift+tab landed on tab %d, want the context tab", mm.ctxPanel.tab)
	}
}

// TestUsageQuotaMsgFeedsThePanel verifies the async probe's result lands in
// the panel body, including the failure notice (one dim line, never a plan
// invented at 0%).
func TestUsageQuotaMsgFeedsThePanel(t *testing.T) {
	store := newTestStore(t)
	rec := newTestUsageRecorder(t, store.Dir())
	s, _, err := newRunSessionWithStore(store, Options{Model: "usage-model", ProviderName: "p", Usage: rec})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := NewModel(Options{}).withSession(s, nil)
	next, _ := m.runSlash("/usage")
	mm := next.(Model)
	mm.usageWaiting = true

	sec := &cli.QuotaSection{Label: "commandcode", Snapshot: &usage.Snapshot{
		Source: "commandcode",
		Plan:   "GOAT",
		Windows: []usage.Window{{
			Kind:    usage.WindowWeekly,
			Percent: ptrFloat(14.3),
		}},
	}}
	updated, _ := mm.Update(usageQuotaMsg{sec: sec})
	final := updated.(Model)
	if final.usageWaiting {
		t.Error("the probe result should clear the waiting flag")
	}
	out := stripANSI(final.usageReport())
	for _, want := range []string{"commandcode · plan quota (GOAT)", "Weekly limit", "14%"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel body missing %q; got:\n%s", want, out)
		}
	}
}

func ptrFloat(v float64) *float64 { return &v }

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
