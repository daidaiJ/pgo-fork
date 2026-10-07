package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the page header line (S1, tui-render-semantics.md C4):
// one dim full-width line at the very top — left the git branch and working
// directory, right the context usage as tokens / window. The information
// migrates here from the old powerline status bar (S2), which becomes the
// usage row below the input (statusbar.go). Data arrives via the same setters
// the status bar used; the component is a plain value with no I/O.

// header holds the page-header state fed by the async git probe and the
// telemetry stream.
type header struct {
	branch string
	dirty  int
	ahead  int
	gitOK  bool
	cwd    string

	tokens int
	window int
}

// setGit stores the latest probe result.
func (h *header) setGit(g gitInfoMsg) {
	h.gitOK = g.ok
	h.branch = g.branch
	h.dirty = g.dirty
	h.ahead = g.ahead
}

// setTelemetry stores the latest context-window readout.
func (h *header) setTelemetry(tokens, window int) {
	h.tokens, h.window = tokens, window
}

// render draws the header to exactly width columns: "dev D:\C\ai\pgo-fork"
// left, "3.0K / 1.0M" right, all dim chrome. Segments with no data (no git
// repo, unknown window) drop silently. rightInset reserves trailing columns on
// the right side: the transcript's one-column scrollbar (FR-10) occupies that
// edge directly below the header, so the right-aligned readout stops short of
// the bar column instead of visually crossing it.
func (h header) render(theme Theme, width int, rightInset int) string {
	if width <= 0 {
		return ""
	}
	rightInset = min(rightInset, width-1)
	if rightInset < 0 {
		rightInset = 0
	}
	var left strings.Builder
	if h.gitOK && h.branch != "" {
		left.WriteString(h.branch)
	}
	if h.cwd != "" {
		if left.Len() > 0 {
			left.WriteByte(' ')
		}
		left.WriteString(h.cwd)
	}
	right := ""
	if h.window > 0 {
		right = fmt.Sprintf("%s / %s", humanTokens(h.tokens), humanTokens(h.window))
	}

	lw := ui.Width(left.String())
	rw := ui.Width(right)
	if lw+rw+1 > width-rightInset {
		// Too narrow for both sides: keep the left only, truncated.
		return theme.Chrome.Render(TruncateToWidth(left.String(), width))
	}
	gap := width - rightInset - lw - rw
	return theme.Chrome.Render(left.String()) + strings.Repeat(" ", gap) + theme.Chrome.Render(right)
}

// humanTokens renders a token count the grok way: "3.0K" below 10K (one
// decimal), "26K"/"150K" above (whole thousands), "1.0M" for millions.
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

// humanizeInt renders n with thousands separators, e.g. 90866 → "90,866"
// (kept from the old status bar; the sub-agent panel's token readout uses it).
func humanizeInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// abbreviateHome replaces a leading $HOME in path with "~" so the header stays
// compact. It leaves paths outside $HOME untouched and never fails.
func abbreviateHome(path string) string {
	home := homeDir()
	if home == "" || path == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// homeDir returns the user's home directory, or "" when it cannot be
// determined. Kept as a tiny wrapper so abbreviateHome stays testable.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
