package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the running status line shown while an agent run is in
// flight (S11, tui-render-semantics.md C4): a pulsing glyph + the current
// activity text + the current turn's elapsed time on the left, and the run's
// total elapsed time + cumulative output estimate + a "[停止]" stop affordance
// on the right — the grok running line's layout. The witty-phrase verb
// roulette is retired per the negative list (implementation-plan 明确不做):
// the slot carries what the agent is actually doing.

// spinnerTickMsg advances the spinner animation. The model re-issues a tick
// after each frame while a run is in flight and lets the tick lapse once the
// run ends, so the animation stops without a running goroutine.
type spinnerTickMsg time.Time

// spinnerInterval is the frame cadence. ~120ms is brisk enough to read as motion
// without churning the render loop.
const spinnerInterval = 120 * time.Millisecond

// spinnerFrames is the asterisk animation cycled one glyph per tick. The glyphs
// grow from a dim dot to a full star and back, reading as a pulsing sparkle.
var spinnerFrames = []string{"·", "✢", "✳", "∗", "✺", "✻", "✽", "✻", "✺", "∗", "✳", "✢"}

// spinner is the animated running indicator. It is a plain value held by the
// Model: begin() arms it at run start, setActivity/pin describe the current
// phase, advance() steps the frame on each tick, and view() renders the line.
type spinner struct {
	theme    Theme
	running  bool
	frame    int
	activity string // what the agent is doing right now ("Thinking", "Running bash", …)
	start    time.Time
	chars    int    // runes streamed this run (the token estimate divides this)
	pinned   string // when set, overrides the activity and stays fixed
}

// newSpinner builds an idle spinner bound to the theme.
func newSpinner(theme Theme) spinner {
	return spinner{theme: theme}
}

// begin arms the spinner for a fresh run: it records the start time, resets
// the frame and token estimate, and clears the activity text.
func (s *spinner) begin(now time.Time) {
	s.running = true
	s.frame = 0
	s.start = now
	s.chars = 0
	s.activity = ""
	s.pinned = ""
}

// setActivity describes the in-flight phase ("Thinking", "Running bash");
// used as the running line's text when nothing is pinned.
func (s *spinner) setActivity(a string) { s.activity = a }

// pin fixes the activity to a specific phase (e.g. "Compacting
// conversation") until unpin, so a long-running phase reads as one steady
// message rather than flickering with events.
func (s *spinner) pin(label string) { s.pinned = label }

// unpin restores event-driven activity after a pinned phase ends.
func (s *spinner) unpin() { s.pinned = "" }

// stop parks the spinner when a run ends so view() renders nothing.
func (s *spinner) stop() { s.running = false }

// advance steps the animation one frame.
func (s *spinner) advance() { s.frame++ }

// addTokens folds a streamed text delta into the running output-token estimate.
// The count is approximate (≈4 chars per token) — enough for a live spinner
// readout, not billing.
func (s *spinner) addTokens(delta string) {
	s.chars += len([]rune(delta))
}

// view renders the running line to exactly width columns:
// "✻ Thinking… 8.0s" left, "49s ↓26.4k [停止]" right-aligned at the edge. It
// returns "" when not running or before a width is known. turnElapsed is the
// current turn's wall time; the right side is the whole run.
func (s spinner) view(width int, turnElapsed time.Duration) string {
	if !s.running || width <= 0 {
		return ""
	}
	glyph := s.theme.Spinner.Render(spinnerFrames[s.frame%len(spinnerFrames)])
	activity := s.activity
	if s.pinned != "" {
		activity = s.pinned
	}
	if activity == "" {
		activity = "Working"
	}
	left := glyph + " " + s.theme.User.Render(activity) +
		s.theme.Chrome.Render("… "+formatElapsed(turnElapsed))

	runElapsed := formatElapsed(time.Since(s.start))
	right := s.theme.Chrome.Render(runElapsed)
	rw := ui.Width(runElapsed)
	if tokens := s.chars / 4; tokens > 0 {
		right += " " + s.theme.Chrome.Render("↓"+humanTokens(tokens))
		rw += 1 + ui.Width("↓"+humanTokens(tokens))
	}
	right += " " + s.theme.User.Render("[停止]")
	rw += 1 + ui.Width("[停止]")

	if rw >= width {
		return truncatePlain(left, width)
	}
	// Left keeps at least the glyph + activity; truncate before squeezing the gap.
	left = truncatePlain(left, width-rw-1)
	gap := width - ui.Width(left) - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// truncatePlain clips a styled string to width columns by dropping trailing
// runes whole (width measured via ui.Width, which strips ANSI), so the styling
// of the kept prefix survives.
func truncatePlain(s string, width int) string {
	if width <= 0 {
		return ""
	}
	for ui.Width(s) > width && s != "" {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// formatElapsed renders a duration compactly: "42s", "1m 54s", or "1h 2m".
func formatElapsed(d time.Duration) string {
	d = max(d, 0)
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs %= 60
	if mins < 60 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	hours := mins / 60
	mins %= 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}
