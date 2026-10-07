package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/ui"
)

// TestHeaderRendersBothSides verifies the page header shows branch + cwd on
// the left and the context budget on the right (S1).
func TestHeaderRendersBothSides(t *testing.T) {
	h := header{gitOK: true, branch: "dev", cwd: "D:\\C\\ai\\pgo-fork", tokens: 26000, window: 1_000_000}
	out := stripANSI(h.render(DefaultTheme(), 120, 0))
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
	out := stripANSI(h.render(DefaultTheme(), 80, 0))
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
		out := h.render(theme, width, 0)
		if w := ui.Width(stripANSI(out)); w != width {
			t.Errorf("width %d: header width = %d", width, w)
		}
	}
}

// TestHeaderTooNarrowKeepsLeft verifies the left side survives a too-narrow
// terminal with the budget dropped.
func TestHeaderTooNarrowKeepsLeft(t *testing.T) {
	h := header{gitOK: true, branch: "long-branch-name", cwd: "/tmp", tokens: 1, window: 1}
	out := stripANSI(h.render(DefaultTheme(), 20, 0))
	if !strings.Contains(out, "long-branch-name") {
		t.Errorf("narrow header should keep the left side: %q", out)
	}
}

// TestHeaderRightInsetYieldsScrollbarColumn verifies the 2026-10-07 user
// report fix: when the transcript overflows, its one-column scrollbar owns the
// right edge below the header, so the right-aligned budget readout must stop
// one column short instead of crossing the bar. With no inset the readout is
// flush right again.
func TestHeaderRightInsetYieldsScrollbarColumn(t *testing.T) {
	h := header{gitOK: true, branch: "dev", cwd: "/tmp/p", tokens: 26000, window: 1_000_000}
	theme := DefaultTheme()

	flush := stripANSI(h.render(theme, 80, 0))
	inset := stripANSI(h.render(theme, 80, 1))
	// The flush header fills 80; the inset header stops one column short — the
	// empty trailing column IS the yield, so it may not be back-padded to 80.
	if ui.Width(flush) != 80 {
		t.Fatalf("flush header must fill the exact width: %d", ui.Width(flush))
	}
	if ui.Width(inset) != 79 {
		t.Fatalf("inset header must stop one column short: %d", ui.Width(inset))
	}
	if !strings.HasSuffix(flush, "26K / 1.0M") {
		t.Errorf("flush header should end with the budget: %q", flush)
	}
	// stripANSI cannot show the invisible trailing column; the 79-vs-80 width
	// assertions above carry the yield semantics.
	if !strings.Contains(inset, "26K / 1.0M") {
		t.Errorf("inset header should still show the budget: %q", inset)
	}
}

// TestHeaderSeededOnSessionBind covers the 2026-10-07 user report: the
// context-budget readout used to stay hidden until the end-of-run TelemetryEvent
// arrived. Binding a session must seed it immediately — window from the live
// config, tokens from the same live estimate the /context panel falls back to.
func TestHeaderSeededOnSessionBind(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{
		Model:        "seed-model",
		ProviderName: "seed-provider",
	})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	s.agentCtx.SystemPrompt = "you are a helpful assistant doing token-worthy work"
	s.agentCtx.Messages = append(s.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello, world")}})

	m := NewModel(Options{}).withSession(s, nil)
	if m.header.window <= 0 {
		t.Fatalf("header window not seeded: %d", m.header.window)
	}
	if m.header.tokens <= 0 {
		t.Fatalf("header tokens not seeded from live estimate: %d", m.header.tokens)
	}
	out := stripANSI(m.header.render(DefaultTheme(), 120, 0))
	if !strings.Contains(out, "/") {
		t.Errorf("header should show the budget readout right after bind: %q", out)
	}
}

// TestHeaderRefreshesPerTurn verifies the per-turn usage fold keeps the header
// readout current during a long agentic run, not only at run end.
func TestHeaderRefreshesPerTurn(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("ok")},
		Usage: &agentcore.Usage{
			InputTokens:      1000,
			OutputTokens:     200,
			CacheReadTokens:  300,
			CacheWriteTokens: 50,
		},
	}})
	if want := 1000 + 300 + 50 + 200; m.header.tokens != want {
		t.Errorf("header tokens = %d, want %d (input+cache read+cache write+output)", m.header.tokens, want)
	}
}
