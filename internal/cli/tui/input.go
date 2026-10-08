package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the prompt input field of the full-screen TUI (US-007,
// FR-11/13/14). It wraps charm.land/bubbles/v2/textarea into a small `input`
// component so the model can embed a real multi-line editor instead of the
// throwaway string buffer the skeleton shipped with.
//
// Why textarea rather than a hand-rolled buffer: textarea edits by grapheme /
// rune, so CJK and emoji are inserted and deleted whole. This is exactly the
// class of bug the old REPL input had — it keyed on byte length (len==1) and
// silently dropped the trailing bytes of every multi-byte rune. We deliberately
// delegate all character handling to textarea and never touch bytes ourselves.
//
// Shift+Enter inserts a newline so the editor is a true multi-line composer;
// plain Enter submits (intercepted by the model, never reaching textarea). The
// default InsertNewline binding (Enter) is therefore rebound to Shift+Enter. See
// model.handleKey.

// maxInputRows caps how tall the editor grows as the user adds lines. Past this
// the buffer keeps growing but textarea scrolls its own viewport, so the shell's
// row accounting stays bounded and the transcript never collapses to nothing.
const maxInputRows = 6

// input is the prompt editor. It embeds a textarea.Model and exposes just the
// surface the root model needs: value/clear, focus state (the composer stays
// focused across runs — only submit is gated; the historical blur-during-run
// behavior reset the Windows IME to English), a width setter driven by
// tea.WindowSizeMsg, a render string for View, and the real-terminal-cursor
// position for tea.View.Cursor (see Cursor).
type input struct {
	ta textarea.Model
	// width is the full editor width (terminal columns) last set via SetWidth. It
	// is the span of the top/bottom rules drawn around the editor (Claude-Code
	// style), kept separately because textarea's own Width() reports only the
	// inner text area (prompt column excluded).
	width int
}

// newInput builds a focused editor. It starts one row tall and grows with the
// buffer (up to maxInputRows) as the user inserts newlines with Enter. The
// buffer itself is unbounded; beyond maxInputRows textarea scrolls internally.
func newInput() input {
	ta := textarea.New()
	ta.Prompt = "❯ "
	// Keep the placeholder short enough to never wrap at the minimum editor
	// width: the textarea's DynamicHeight re-wrap is lazy (it settles on the
	// next Update/View, not on SetWidth), and a wrapped placeholder would
	// desync the shell's first-frame row accounting.
	ta.Placeholder = "Type a message… (Enter to send)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// Let the textarea own its own height: DynamicHeight grows/shrinks it to the
	// content between MinHeight (1) and MaxHeight (maxInputRows), and — critically
	// — fixes the viewport scroll offset in the same pass. Doing it manually (an
	// after-the-fact SetHeight in syncHeight) left a stale scroll offset: inserting
	// a newline scrolled the cursor into view while the editor was still 1 row
	// tall, pushing the first line off the top, and the later SetHeight never
	// scrolled it back — so a two-line buffer rendered as two blank lines.
	ta.MinHeight = 1
	ta.MaxHeight = maxInputRows
	ta.DynamicHeight = true
	// textarea.New starts at defaultHeight (6). DynamicHeight only recomputes on
	// edits, so pin the empty editor to one row up front — otherwise the shell
	// would reserve six rows before the user has typed anything.
	ta.SetHeight(1)
	// Rebind InsertNewline from its default (Enter) to the newline keys, since
	// plain Enter is the model's submit key (handleKey intercepts it before
	// textarea sees it). Shift+Enter is the primary, advertised binding: Bubble
	// Tea v2 already enables the Kitty keyboard protocol's disambiguate flag
	// (flag 1) on every View, so capable terminals (kitty, ghostty, wezterm,
	// recent iTerm2) report Shift+Enter as a distinct CSI-u sequence rather than
	// a bare CR. Crucially this is flag 1, NOT flag 8 (ReportAllKeysAsEscapeCodes)
	// — flag 8 broke IME / CJK input because it strips associated text, whereas
	// flag 1 only disambiguates special keys and leaves text entry untouched.
	// On terminals without the protocol (macOS Terminal.app, tmux by default)
	// Shift+Enter arrives byte-identical to Enter and would submit, so Ctrl+J (a
	// literal LF, always distinct from Enter's CR) and Alt+Enter (ESC-prefixed,
	// always distinct) are kept as silent fallbacks — a newline is guaranteed to
	// work everywhere. All three split the line at the cursor and keep typed text.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j", "alt+enter"),
		key.WithHelp("shift+enter", "insert newline"),
	)
	// Keep textarea's REAL cursor reporting (the default): the model surfaces
	// the position through tea.View.Cursor so the terminal drives a real
	// blinking cursor at the caret. This matters for IME on Windows — with the
	// cursor drawn as a fake string character (SetVirtualCursor(true)) the
	// terminal never learns where the caret is, and the IME composition window
	// loses its anchor, which knocks the input method back to English
	// mid-session. With the real cursor anchored in the composer, composition
	// sticks to the caret.
	// Drop the default cursor-line background highlight so the composer is framed
	// only by the top/bottom rules (see View), matching Claude Code — no fill.
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.Focus()
	return input{ta: ta}
}

// Update forwards a message (typically a key press) to the underlying textarea
// and returns the updated component. The model calls this only for keys it does
// not intercept itself (submit / interrupt / quit), so textarea sees ordinary
// editing keys — including Enter (newline) and CJK / emoji runes, which it
// inserts whole. Height is owned by textarea's DynamicHeight (see newInput), so
// there is nothing to re-sync here.
func (in input) Update(msg tea.Msg) (input, tea.Cmd) {
	var cmd tea.Cmd
	in.ta, cmd = in.ta.Update(msg)
	return in, cmd
}

// Height reports the current visible row count of the editor so the model can
// reserve that many rows in its View layout. It includes the two rule rows (top
// and bottom) drawn around the textarea.
func (in input) Height() int { return in.ta.Height() + 2 }

// Value returns the current buffer contents, including any embedded newlines.
func (in input) Value() string { return in.ta.Value() }

// SetValue replaces the buffer contents and moves the cursor to the end. It is
// used by slash autocomplete (Tab) to complete the buffer to the chosen command.
func (in *input) SetValue(s string) {
	in.ta.SetValue(s)
}

// Clear empties the buffer and resets the cursor to the start.
func (in *input) Clear() {
	in.ta.Reset()
}

// Focus enables editing and returns the cursor-blink Cmd.
func (in *input) Focus() tea.Cmd { return in.ta.Focus() }

// Blur disables editing (used while a run is in flight).
func (in *input) Blur() { in.ta.Blur() }

// Focused reports whether the editor currently accepts input.
func (in input) Focused() bool { return in.ta.Focused() }

// Cursor returns the caret position for tea.View.Cursor, relative to the
// textarea's own rendered view (the model adds the frame's border offset).
// It is nil while the editor is blurred, which hides the terminal cursor.
func (in input) Cursor() *tea.Cursor { return in.ta.Cursor() }

// Line reports the zero-based index of the line the cursor is on, and LineCount
// the total number of lines in the buffer. The model uses them to decide whether
// ↑/↓ should walk the prompt history (caret on the first / last line) or move the
// caret within a multi-line draft.
func (in input) Line() int      { return in.ta.Line() }
func (in input) LineCount() int { return in.ta.LineCount() }

// SetWidth resizes the editor to the terminal width so wrapping and the prompt
// column line up with the rest of the shell. Same-width calls are no-ops: the
// model re-runs relayout on every streaming delta, and each SetWidth triggers a
// full textarea re-wrap — churn that shows as flicker around the caret for no
// benefit.
func (in *input) SetWidth(w int) {
	if w < 0 {
		w = 0
	}
	if w == in.width {
		return
	}
	in.width = w
	in.ta.SetWidth(w)
}

// View renders the editor to a string for embedding in the model's View. The
// textarea is framed with a full-width rounded border (S14, grok composer): a
// closed rounded box rather than the old pair of horizontal rules. label, when
// non-empty, is embedded into the bottom border's right end in the bright
// style (grok's "model · approval" tag) — the border fill is cut to make room
// so the label reads as part of the frame.
func (in input) View(label string, theme Theme) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorGray))
	if in.width > 0 {
		style = style.Width(in.width - 2)
	}
	box := style.Render(in.ta.View())
	if label == "" || in.width <= 0 {
		return box
	}
	lines := strings.Split(box, "\n")
	last := len(lines) - 1
	lw := ui.Width(label)
	inner := in.width - 2 // columns between the two border corners
	if lw+1 > inner || last < 1 || ui.Width(lines[last]) != in.width {
		return box // too narrow for the tag (or frame shape unexpected): plain box
	}
	// Rebuild the bottom border: "╰" + fill ─ + bright label + "╯".
	fill := inner - lw
	lines[last] = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGray)).
		Render("╰"+strings.Repeat("─", fill)) +
		theme.User.Render(label) +
		lipgloss.NewStyle().Foreground(lipgloss.Color(colorGray)).Render("╯")
	return strings.Join(lines, "\n")
}
