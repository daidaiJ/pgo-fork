package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestFormatElapsed checks the compact duration formatting across the second,
// minute, and hour ranges.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{114 * time.Second, "1m 54s"},
		{60 * time.Minute, "1h 0m"},
		{62 * time.Minute, "1h 2m"},
		{-3 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestSpinnerViewActivity verifies a running spinner renders its activity text
// with the turn elapsed on the left and the run total + output estimate +
// [停止] on the right, and that a stopped spinner renders nothing.
func TestSpinnerViewActivity(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now().Add(-114 * time.Second))
	s.setActivity("Thinking")
	s.chars = 968 // 968/4 = 242 estimated tokens

	view := stripANSI(s.view(120, 8*time.Second))
	if !strings.Contains(view, "Thinking") {
		t.Errorf("view %q should contain the activity text", view)
	}
	for _, want := range []string{"8s", "1m 54s", "↓242", "[停止]"} {
		if !strings.Contains(view, want) {
			t.Errorf("view %q missing stat %q", view, want)
		}
	}

	s.stop()
	if got := s.view(120, 0); got != "" {
		t.Errorf("stopped spinner should render nothing, got %q", got)
	}
}

// TestSpinnerDefaultActivity verifies the activity falls back to "Working"
// when no event has described the phase yet.
func TestSpinnerDefaultActivity(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now())
	if view := stripANSI(s.view(80, 0)); !strings.Contains(view, "Working") {
		t.Errorf("view %q should show the default activity", view)
	}
}

// TestSpinnerPinOverridesActivity verifies a pinned phase label replaces the
// event-driven activity until unpinned.
func TestSpinnerPinOverridesActivity(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now())
	s.setActivity("Thinking")
	s.pin("Compacting conversation")

	if view := stripANSI(s.view(120, 0)); !strings.Contains(view, "Compacting conversation") {
		t.Errorf("pinned spinner view %q should show the pinned label", view)
	}
	if view := stripANSI(s.view(120, 0)); strings.Contains(view, "Thinking") {
		t.Errorf("pinned spinner view %q should hide the activity text", view)
	}

	s.unpin()
	if got := stripANSI(s.view(120, 0)); strings.Contains(got, "Compacting conversation") {
		t.Errorf("after unpin, view %q should not show the pinned label", got)
	}
}

// TestSpinnerViewNoTokenEstimateWithoutDeltas verifies the ↓ estimate stays
// hidden until some output has streamed.
func TestSpinnerViewNoTokenEstimateWithoutDeltas(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now())
	if view := stripANSI(s.view(80, 0)); strings.Contains(view, "↓0") {
		t.Errorf("view %q should not show a zero token estimate", view)
	}
}

// TestSpinnerAdvanceStepsFrame verifies the animation frame advances.
func TestSpinnerAdvanceStepsFrame(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now())
	if s.frame != 0 {
		t.Fatalf("fresh spinner frame = %d, want 0", s.frame)
	}
	for i := 0; i < 40; i++ {
		s.advance()
	}
	if s.frame != 40 {
		t.Errorf("frame after 40 advances = %d", s.frame)
	}
}

// TestModelRunningShowsSpinnerRow verifies that while a run is in flight the
// running line occupies its own row above the input and the shell still fills
// exactly the terminal height (relayout shrinks the transcript by that row).
func TestModelRunningShowsSpinnerRow(t *testing.T) {
	m := apply(t, NewModel(Options{Model: "test-model"}), tea.WindowSizeMsg{Width: 60, Height: 10})
	m.running = true
	m.spinner.begin(time.Now())
	m.relayout()

	view, _ := m.renderContent()
	if got := strings.Count(view, "\n"); got != 9 {
		t.Errorf("running newline count = %d, want 9 (10 rows)", got)
	}
	if !strings.Contains(stripANSI(view), "Working") {
		t.Errorf("running view should contain the activity text, got:\n%s", stripANSI(view))
	}
}
