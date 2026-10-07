package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the usage row below the input editor (S12,
// tui-render-semantics.md C4): one dim line — model name bright, then the
// run's API-call count, in/out tokens, cache hit share, and TTFT · tok/s
// throughput, segments joined by dim separators. It replaces the old
// powerline status bar: the cwd/git/context segments migrated to the page
// header (header.go, S1/S2) and the activity slot to the running line
// (spinner.go, S11), so no segment remains that needs a powerline block — the
// ribbon machinery is retired with them.
//
// The row shows only values it actually observed ("拿不到值整段隐藏"): a fresh
// session with no run yet renders just the model name. The component is a
// plain value with no I/O; the model feeds it from turnEndMsg usage payloads
// and the stream's first-token timing.

// usageStats accumulates one run's token accounting (reset at run start, kept
// frozen after run end until the next run).
type usageStats struct {
	calls     int // completed API turns
	in        int // summed prompt tokens
	out       int // summed completion tokens
	cacheRead int // summed cache-read tokens (0 when provider does not report)
	firstDelta time.Time // first streamed reply byte (TTFT anchor)
	runStart   time.Time
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

// beginRun resets the accounting for a fresh run.
func (s *usageStats) beginRun(now time.Time) {
	s.calls, s.in, s.out, s.cacheRead = 0, 0, 0, 0
	s.runStart = now
	s.firstDelta = time.Time{}
}

// markFirstDelta records the TTFT anchor on the first streamed byte.
func (s *usageStats) markFirstDelta(now time.Time) {
	if s.firstDelta.IsZero() {
		s.firstDelta = now
	}
}

// foldTurn folds one completed turn's usage payload into the totals.
func (s *usageStats) foldTurn(in, out, cacheRead int) {
	s.calls++
	s.in += in
	s.out += out
	s.cacheRead += cacheRead
}

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
	if u.calls > 0 {
		plain = append(plain, fmt.Sprintf("✓ %d", u.calls))
	}
	if u.in > 0 || u.out > 0 {
		plain = append(plain, fmt.Sprintf("in %s out %s", humanTokens(u.in), humanTokens(u.out)))
	}
	if u.cacheRead > 0 && u.in+u.cacheRead > 0 {
		pct := 100 * float64(u.cacheRead) / float64(u.in+u.cacheRead)
		plain = append(plain, fmt.Sprintf("cache %.1f%%", pct))
	}
	if ttft, rate, ok := u.timing(now); ok {
		plain = append(plain, fmt.Sprintf("%dms ttft · %s tok/s", ttft.Milliseconds(), humanTokens(int(rate))))
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

// timing derives TTFT and output rate from the recorded anchors. It reports
// ok=false until at least one streamed byte and one finished turn exist, so
// the throughput segment never shows a divide-by-zero guess.
func (s *usageStats) timing(now time.Time) (time.Duration, float64, bool) {
	if s.firstDelta.IsZero() || s.out <= 0 || !now.After(s.firstDelta) {
		return 0, 0, false
	}
	secs := now.Sub(s.firstDelta).Seconds()
	return s.firstDelta.Sub(s.runStart), float64(s.out) / secs, true
}
