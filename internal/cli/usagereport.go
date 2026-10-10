// This file holds the shared usage renderers (O1 / T7.3c): /usage reads the
// session's cumulative accounting and /stats aggregates every session's ledger
// over a time window. Both front-ends render through these functions —
// WriteUsageReport and WriteStatsReport return plain text, so the REPL prints
// them and the TUI folds them into a system block, exactly as /status and
// /session already do.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/statline"
	"github.com/smallnest/pigo/internal/usage"
)

// UsageReportOptions carries the session display context for /usage. Quota is
// the provider plan-quota section (T6.3), resolved by the caller before the
// report is rendered: nil leaves the report as it was.
type UsageReportOptions struct {
	// SessionID / Model, when non-empty, are shown in the report header.
	SessionID string
	Model     string
	Quota     *QuotaSection
}

// WriteUsageReport renders the session's cumulative usage: the grok-aligned
// tallies (✓/✗ calls, retries, cache misses), the token totals with the cache
// and think shares, the last turn's perf, and the sub-agent share. Values that
// were never observed are omitted (zero-hide, matching the status line).
//
// When the caller resolved a provider plan quota it follows as its own
// section: the same command reports what this session spent and what the
// subscription has left (usage-ledger.md D-6).
func WriteUsageReport(out io.Writer, s statline.Stats, opts UsageReportOptions) {
	color := ui.Enabled()
	title := "session usage"
	if opts.Model != "" {
		title += " · " + opts.Model
	}
	fmt.Fprintln(out, ui.Colorize(color, ui.Bold, title))
	if opts.SessionID != "" {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, opts.SessionID))
	}
	if s.Calls == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "no accounted turns yet"))
		writeQuotaSection(out, opts.Quota)
		return
	}

	var calls strings.Builder
	fmt.Fprintf(&calls, "✓ %d", s.OK())
	if s.Errors > 0 {
		fmt.Fprintf(&calls, "  ✗ %d", s.Errors)
	}
	if s.Retries > 0 {
		fmt.Fprintf(&calls, "  retries ↻ %d", s.Retries)
	}
	if s.CacheMisses > 0 {
		fmt.Fprintf(&calls, "  cache misses %d", s.CacheMisses)
	}
	fmt.Fprintf(out, "  %-9s %s\n", "calls", calls.String())

	fmt.Fprintf(out, "  %-9s in %s  out %s", "tokens", humanTokens(s.Input), humanTokens(s.Output))
	if pct, ok := s.CachePercent(); ok {
		fmt.Fprintf(out, "  cache %.1f%%", pct)
	}
	if pct, ok := s.ThinkPercent(); ok {
		fmt.Fprintf(out, "  think %.1f%%", pct)
	}
	fmt.Fprintln(out)

	if s.LastTTFTMs > 0 || s.LastDurationMs > 0 {
		fmt.Fprintf(out, "  %-9s last turn", "perf")
		if s.LastTTFTMs > 0 {
			fmt.Fprintf(out, " %dms ttft", s.LastTTFTMs)
		}
		if tps, ok := s.LastTPS(); ok {
			fmt.Fprintf(out, " · %s tok/s", humanTokens(int(tps)))
		}
		fmt.Fprintln(out)
	}
	if s.SubagentCalls > 0 {
		fmt.Fprintf(out, "  %-9s %d calls", "subagents", s.SubagentCalls)
		if s.SubagentIncomplete > 0 {
			fmt.Fprintf(out, " · %d incomplete", s.SubagentIncomplete)
		}
		fmt.Fprintln(out)
	}
	writeQuotaSection(out, opts.Quota)
}

// writeQuotaSection appends the plan-quota block after the session block,
// separated by a blank line, and writes nothing when the caller probed no
// quota (or when the provider has no quota source).
func writeQuotaSection(out io.Writer, sec *QuotaSection) {
	if sec == nil {
		return
	}
	if sec.Err != nil && errors.Is(sec.Err, usage.ErrUnsupported) && sec.Snapshot == nil {
		return
	}
	fmt.Fprintln(out)
	WriteQuotaReport(out, *sec, time.Now())
}

// UsageLedger loads every session's usage ledger under root (the sessions
// directory), returning all records — the cross-session view /stats aggregates.
// Unreadable ledgers are skipped so one corrupt file never hides the rest.
func UsageLedger(root string) []statline.Record {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var all []statline.Record
	for _, e := range entries {
		if e.IsDir() || !statline.IsUsageFileName(e.Name()) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), statline.UsageSuffix)
		recs, err := statline.LoadUsage(root, id)
		if err != nil {
			continue
		}
		all = append(all, recs...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	return all
}

// UsageWindow names a /stats time window. It is the runtime contract's
// vocabulary (the Parse face validates the same keywords); the helpers here
// resolve it to a lower bound and a display label.
type UsageWindow = runtime.UsageWindow

// since resolves the window's lower bound relative to now (zero for "all").
func usageWindowSince(w UsageWindow, now time.Time) time.Time {
	switch w {
	case runtime.UsageWindowDay:
		return now.Add(-24 * time.Hour)
	case runtime.UsageWindowAll:
		return time.Time{}
	default:
		return now.Add(-7 * 24 * time.Hour)
	}
}

// label renders the window for the report header.
func usageWindowLabel(w UsageWindow) string {
	switch w {
	case runtime.UsageWindowDay:
		return "last 24h"
	case runtime.UsageWindowAll:
		return "all time"
	default:
		return "last 7 days"
	}
}

// WriteStatsReport renders the cross-session ledger aggregate: one row per model
// over the window, plus the window total. An empty ledger renders a dim notice
// rather than an empty table.
func WriteStatsReport(out io.Writer, recs []statline.Record, window UsageWindow, now time.Time) {
	color := ui.Enabled()
	since := usageWindowSince(window, now)
	inWindow := make([]statline.Record, 0, len(recs))
	for _, r := range recs {
		if r.At.IsZero() || !since.IsZero() && r.At.Before(since) {
			continue
		}
		inWindow = append(inWindow, r)
	}
	fmt.Fprintln(out, ui.Colorize(color, ui.Bold, fmt.Sprintf("usage ledger · %s · %d turns", usageWindowLabel(window), len(inWindow))))
	if len(inWindow) == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "no accounted turns in this window"))
		return
	}

	byModel := map[string][]statline.Record{}
	var order []string
	for _, r := range inWindow {
		name := r.Model
		if name == "" {
			name = "(unknown model)"
		}
		if _, seen := byModel[name]; !seen {
			order = append(order, name)
		}
		byModel[name] = append(byModel[name], r)
	}
	type row struct {
		model string
		stats statline.Stats
	}
	rows := make([]row, 0, len(order))
	for _, name := range order {
		rows = append(rows, row{model: name, stats: statline.Aggregate(byModel[name])})
	}
	// Heaviest first: prompt tokens are what the ledger's cost tracks.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].stats.Input != rows[j].stats.Input {
			return rows[i].stats.Input > rows[j].stats.Input
		}
		return rows[i].model < rows[j].model
	})

	fmt.Fprintf(out, "  %-32s %6s %9s %9s %8s\n", "model", "turns", "in", "out", "cache")
	for _, r := range rows {
		cache := "--"
		if pct, ok := r.stats.CachePercent(); ok {
			cache = fmt.Sprintf("%.1f%%", pct)
		}
		fmt.Fprintf(out, "  %-32s %6d %9s %9s %8s\n",
			truncateModel(r.model), r.stats.Calls, humanTokens(r.stats.Input), humanTokens(r.stats.Output), cache)
	}
	total := statline.Aggregate(inWindow)
	fmt.Fprintf(out, "  %-32s %6d %9s %9s\n", "total", total.Calls, humanTokens(total.Input), humanTokens(total.Output))
}

// truncateModel keeps the ledger table's model column narrow enough that a long
// custom id does not push the numeric columns out of alignment.
func truncateModel(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// humanTokens renders a token count the grok way ("3.0K" below 10K with one
// decimal, "26K"/"150K" above, "1.0M" for millions). Kept local to the cli
// renderers so they do not depend on the TUI package.
func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
