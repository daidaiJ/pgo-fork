package tui

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// TestHeaderRendersBothSides verifies the page header shows branch + cwd on
// the left and the context budget on the right (S1).
func TestHeaderRendersBothSides(t *testing.T) {
	h := header{gitOK: true, branch: "dev", cwd: "D:\\C\\ai\\pgo-fork", tokens: 26000, window: 1_000_000}
	out := stripANSI(h.render(DefaultTheme(), 120))
	for _, want := range []string{"dev", "D:\\C\\ai\\pgo-fork", "26K / 1.0M"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q; got %q", want, out)
		}
	}
}

// TestHeaderHidesMissingData verifies segments with no data drop silently: no
// git repo drops the branch, no telemetry drops the budget.
func TestHeaderHidesMissingData(t *testing.T) {
	h := header{gitOK: false, cwd: "/tmp/project"}
	out := stripANSI(h.render(DefaultTheme(), 80))
	if strings.Contains(out, "undefined") {
		t.Errorf("header should not render a placeholder for a missing branch: %q", out)
	}
	if !strings.Contains(out, "/tmp/project") {
		t.Errorf("cwd should still render: %q", out)
	}
	if strings.Contains(out, "/") && strings.Contains(out, "K") {
		t.Errorf("budget should be hidden without telemetry: %q", out)
	}
}

// TestHeaderExactWidth verifies the header fills exactly the given width so
// the shell's row accounting stays honest.
func TestHeaderExactWidth(t *testing.T) {
	h := header{gitOK: true, branch: "dev", cwd: "/tmp/project", tokens: 3000, window: 1_000_000}
	theme := DefaultTheme()
	for _, width := range []int{40, 60, 120} {
		out := h.render(theme, width)
		if w := ui.Width(stripANSI(out)); w != width {
			t.Errorf("width %d: header width = %d", width, w)
		}
	}
}

// TestHeaderTooNarrowKeepsLeft verifies the left side survives a too-narrow
// terminal with the budget dropped.
func TestHeaderTooNarrowKeepsLeft(t *testing.T) {
	h := header{gitOK: true, branch: "long-branch-name", cwd: "/tmp", tokens: 1, window: 1}
	out := stripANSI(h.render(DefaultTheme(), 20))
	if !strings.Contains(out, "long-branch-name") {
		t.Errorf("narrow header should keep the left side: %q", out)
	}
}
