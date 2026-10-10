package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/statline"
)

// This file implements the usage row below the input editor (S12,
// tui-render-semantics.md C4): one dim line — model name bright, then the
// session's API-call tally, retries, in/out tokens, cache hit share, cache
// misses, think share, and the last turn's TTFT · tok/s, segments joined by dim
// separators. It replaces the old powerline status bar: the cwd/git/context
// segments migrated to the page header (header.go, S1/S2) and the activity slot
// to the running line (spinner.go, S11), so no segment remains that needs a
// powerline block — the ribbon machinery is retired with them.
//
// O1 (2026-10-08) changed the accounting source, not the look: the numbers are
// session cumulative (grok's default status line semantics) read from the
// session usage ledger, rather than a per-run tally frozen in this component.
// The two grok segments pigo lacked — ApiRetries (`↻ n`) and CacheMisses
// (`miss n`) — are added, both hidden while zero.
//
// The row shows only values it actually observed ("拿不到值整段隐藏"): a session
// with no accounted turn renders just the model name.
type usageStats struct {
	// stats is the session-cumulative aggregate (O1): seeded from the session
	// usage ledger at bind and refreshed after every settled turn.
	stats statline.Stats

	// The remaining fields are the in-flight turn's live anchors, reset at run
	// start and at every turn end: streamed characters feed the live think
	// share (the ledger's own character counts are exact per settled turn), and
	// firstDelta anchors the live TTFT shown before the turn's record lands.
	streamChars int
	thinkChars  int
	firstDelta  time.Time
	runStart    time.Time
}

// statusBar renders the usage row. The historical type name stays (call sites
// and tests refer to it); its semantics are now the S12 usage line.
type statusBar struct {
	theme Theme

	// model is the display name of the active model (model · provider).
	model string

	// usage is the accumulated run accounting rendered as segments.
	usage usageStats
}

// newStatusBar builds the usage row from the theme and resolved Options.
func newStatusBar(theme Theme, opts Options, cwd string) statusBar {
	_ = cwd // historically part of the seam; cwd now lives in the header
	name := opts.Model
	if opts.ProviderName != "" {
		name += " (" + opts.ProviderName + ")"
	}
	return statusBar{theme: theme, model: name}
}

// SetModel updates the displayed model name after a /model switch.
func (s *statusBar) SetModel(model string) {
	name := model
	if s.model != "" {
		if _, prov, ok := strings.Cut(s.model, " ("); ok {
			name = model + " (" + prov
		}
	}
	s.model = name
}

// seed replaces the session-cumulative aggregate (bind, and after each settled
// turn). It leaves the in-flight anchors alone so a mid-run refresh cannot
// reset the live TTFT/character tallies.
func (s *usageStats) seed(stats statline.Stats) {
	s.stats = stats
}

// beginRun resets the in-flight turn anchors for a fresh run. The session
// totals are deliberately kept (O1: the row is session cumulative).
func (s *usageStats) beginRun(now time.Time) {
	s.resetTurn()
	s.runStart = now
}

// endTurn retires the settled turn's live anchors: its numbers now live in the
// ledger record the row reads back.
func (s *usageStats) endTurn() {
	s.resetTurn()
}

// resetTurn clears the per-turn live anchors.
func (s *usageStats) resetTurn() {
	s.streamChars, s.thinkChars = 0, 0
	s.firstDelta = time.Time{}
}

// addChars folds one streamed delta into the live think-share basis.
func (s *usageStats) addChars(delta string, thinking bool) {
	if thinking {
		s.thinkChars += len(delta)
		return
	}
	s.streamChars += len(delta)
}

// markFirstDelta records the TTFT anchor on the first streamed byte.
func (s *usageStats) markFirstDelta(now time.Time) {
	if s.firstDelta.IsZero() {
		s.firstDelta = now
	}
}

// foldTurn is deliberately absent: a session-bound model re-aggregates the
// ledger after each settled turn (seed), and a session-less model has no
// accounting to show — there is no second bookkeeping path to keep in sync.

// Render lays the usage row out to exactly width columns: the model name in
// bright white, then the observed segments dim, joined by " | ". Segments with
// no data drop; a too-narrow terminal drops whole trailing segments (the model
// name always stays) rather than mid-sequence truncating through ANSI.
func (s statusBar) Render(width int, now time.Time) string {
	if width <= 0 {
		return ""
	}
	plain := []string{s.model}
	u := s.usage
	st := u.stats
	if st.Calls > 0 {
		calls := fmt.Sprintf("✓ %d", st.OK())
		if st.Errors > 0 {
			calls += fmt.Sprintf(" ✗ %d", st.Errors)
		}
		plain = append(plain, calls)
	}
	// Grok's retry and cache-miss segments: hidden while zero.
	if st.Retries > 0 {
		plain = append(plain, fmt.Sprintf("↻ %d", st.Retries))
	}
	if st.Input > 0 || st.Output > 0 {
		plain = append(plain, fmt.Sprintf("in %s out %s", humanTokens(st.Input), humanTokens(st.Output)))
	}
	// Cache hit share: prompt tokens served from the provider cache. The
	// segment stays live for every turn with observed prompt usage — cache 0%
	// is the honest readout when the gateway reports no cached tokens, so a
	// cold cache is visible rather than silently hidden.
	if pct, ok := st.CachePercent(); ok {
		plain = append(plain, fmt.Sprintf("cache %.0f%%", pct))
	}
	if st.CacheMisses > 0 {
		plain = append(plain, fmt.Sprintf("miss %d", st.CacheMisses))
	}
	// Thinking share: streamed reasoning characters over all streamed
	// characters (≈4 chars/token on both sides, so the ratio is token-honest).
	// The in-flight turn's live characters join the settled totals so the
	// segment moves while a turn streams.
	thinkChars := st.ThinkChars + u.thinkChars
	streamChars := st.StreamChars + u.streamChars
	if thinkChars+streamChars > 0 {
		pct := 100 * float64(thinkChars) / float64(thinkChars+streamChars)
		plain = append(plain, fmt.Sprintf("think %.0f%%", pct))
	}
	if perf, ok := u.perf(" "); ok {
		plain = append(plain, perf)
	}

	// Drop trailing segments until the joined line fits (model name never drops).
	keep := len(plain)
	for keep > 1 {
		w := 0
		for i, seg := range plain[:keep] {
			if i > 0 {
				w += ui.Width(" | ")
			}
			w += ui.Width(seg)
		}
		if w <= width {
			break
		}
		keep--
	}

	var b strings.Builder
	for i, seg := range plain[:keep] {
		if i > 0 {
			b.WriteString(s.theme.Chrome.Render(" | "))
		}
		if i == 0 {
			b.WriteString(s.theme.User.Render(seg))
		} else {
			b.WriteString(s.theme.Chrome.Render(seg))
		}
	}
	out := b.String()
	if ui.Width(stripANSI(out)) > width {
		// Even the model name alone overflows: hard-truncate on the plain text
		// so the row never spills past the terminal edge.
		plainAll := strings.Join(plain[:keep], " | ")
		return s.theme.User.Render(TruncateToWidth(plainAll, width))
	}
	return out
}

// perf renders the perf segment: the last settled turn's TTFT and output rate,
// or — before any turn has settled — the in-flight turn's elapsed TTFT alone
// (the output rate needs the turn's final token count). It reports ok=false when
// neither is known, so the segment is hidden rather than showing a guess.
func (s *usageStats) perf(sep string) (string, bool) {
	if s.stats.LastTTFTMs > 0 || s.stats.LastDurationMs > 0 {
		parts := make([]string, 0, 2)
		if s.stats.LastTTFTMs > 0 {
			parts = append(parts, fmt.Sprintf("%dms ttft", s.stats.LastTTFTMs))
		}
		if tps, ok := s.stats.LastTPS(); ok {
			parts = append(parts, humanTokens(int(tps))+" tok/s")
		}
		if len(parts) > 0 {
			return strings.Join(parts, sep), true
		}
	}
	if s.firstDelta.IsZero() || s.runStart.IsZero() || !s.firstDelta.After(s.runStart) {
		return "", false
	}
	return fmt.Sprintf("%dms ttft", s.firstDelta.Sub(s.runStart).Milliseconds()), true
}
