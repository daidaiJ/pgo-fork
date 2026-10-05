package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// This file implements the scrolling transcript region of the full-screen TUI
// (US-005, SPEC 5.1 transcript, FR-5/FR-10). The transcript owns a
// viewport.Model and an ordered list of rendered blocks (user / assistant /
// system turns). Streaming assistant text arrives as textDeltaMsg values that
// append to the current assistant block; turnEndMsg finalizes it. Content is
// re-flowed through the viewport with theme.WrapToWidth at the live width so CJK
// and emoji never split mid-rune. Tool cards are a later node (#389); this file
// leaves a clean seam (system lines) without building cards.
//
// Thinking blocks (T1.3) stream in as thinkingDeltaMsg values into a dimmed
// roleThinking block that renders collapsed to its first few lines; Ctrl+T
// toggles expanded/full, and once real reply text starts (or the turn ends) the
// block closes and gains a "Thought for Xs" footer.

// blockRole distinguishes the three transcript block kinds so each renders with
// its own theme style.
type blockRole int

const (
	roleUser blockRole = iota
	roleAssistant
	roleSystem
	roleTool
	roleThinking
	// roleBanner is the startup logo + config splash. Its text is pre-rendered
	// (already colored, already laid out) and emitted verbatim, so reflow neither
	// wraps it nor overrides its colors with a role style.
	roleBanner
)

// thinkingCollapsedLines is how many body lines the thinking region shows while
// collapsed (the default); the rest are summarized by a hidden-lines hint.
const thinkingCollapsedLines = 10

// transcriptBlock is one rendered turn in the transcript. text is the raw
// (unstyled, unwrapped) message body; the role selects the theme style and any
// prefix applied at render time. For roleTool blocks text is unused and card
// points at the live tool card (#389); the pointer lets a later toolEndMsg /
// Ctrl+O mutate the card in place and have it re-render on the next reflow.
// roleThinking blocks use expanded (Ctrl+T two-state view: collapsed ≤10 lines
// by default, full when toggled), and done/started/ended to time the
// "Thought for Xs" footer shown once the block closes.
type transcriptBlock struct {
	role     blockRole
	text     string
	card     *toolCard
	expanded bool
	done     bool
	started  time.Time
	ended    time.Time
}

// transcript is the scrolling message log. It wraps a viewport.Model and keeps
// the source blocks so it can re-flow on width changes. activeAssistant indexes
// the assistant block currently receiving streaming deltas, or -1 when no turn
// is streaming.
type transcript struct {
	vp    viewport.Model
	theme Theme

	// totalWidth is the full width the transcript may occupy (terminal columns
	// minus any chrome the model reserves). width (below) is the content width
	// the blocks actually wrap to: it equals totalWidth when the content fits, or
	// totalWidth-1 when it overflows and a scrollbar column must be held back.
	// reflow recomputes width from totalWidth on every content change, so the bar
	// column appears/disappears correctly even as a run streams in new lines.
	totalWidth int

	// width is the content width (terminal columns) the blocks wrap to. It is
	// separate from the viewport's own width so reflow measurements stay stable
	// even before the first size message.
	width int

	blocks          []transcriptBlock
	activeAssistant int
	// activeThinking indexes the thinking block currently receiving streaming
	// deltas, or -1 when none is open (mirrors activeAssistant).
	activeThinking int
	// lastThinking indexes the thinking block opened this turn (open or
	// already closed by arriving reply text), or -1. finalizeTurn uses it to
	// treat the final message's thinking as the authoritative body of the block
	// the turn already showed, instead of appending a duplicate.
	lastThinking int

	// follow is the stick-to-bottom intent: while true, every reflow snaps the
	// viewport to the newest line so streamed output stays visible. It is set
	// when the user submits a turn and cleared when they scroll up to read
	// history (re-armed when they scroll back to the bottom). Tracking intent
	// explicitly — rather than sampling viewport.AtBottom() inside reflow — keeps
	// auto-scroll correct across height changes (setSize resizes the viewport
	// before reflow runs, which would make an AtBottom() sample read false).
	follow bool
}

// newTranscript builds an empty transcript with the given theme. The viewport
// starts zero-sized; the model drives setSize from the first tea.WindowSizeMsg.
func newTranscript(theme Theme) transcript {
	vp := viewport.New()
	return transcript{
		vp:              vp,
		theme:           theme,
		activeAssistant: -1,
		activeThinking:  -1,
		lastThinking:    -1,
	}
}

// setSize resizes the transcript's viewport and re-flows the blocks to the new
// width. A non-positive dimension is clamped to zero so the viewport never sees
// a negative extent. width is the total space available; reflow decides whether
// to spend one column on the scrollbar based on whether the content overflows.
func (t *transcript) setSize(width, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	t.totalWidth = width
	t.vp.SetHeight(height)
	t.reflow()
}

// addUser appends a user turn and closes any streaming assistant block, then
// re-flows. Submitting a prompt is an explicit action where the user always
// wants to see their new turn and the response that follows, so it re-arms
// follow: the viewport snaps to the bottom even if the user had scrolled up
// (e.g. reading the startup banner) — otherwise the streamed reply would
// accumulate off-screen and look like nothing happened. Subsequent streaming
// deltas keep the bottom via follow, which the user can pause by scrolling up.
func (t *transcript) addUser(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleUser, text: text})
	t.activeAssistant = -1
	t.activeThinking = -1
	t.lastThinking = -1
	t.follow = true
	t.reflow()
}

// addSystem appends a system / meta notice (used for run lifecycle and other
// inline notes).
func (t *transcript) addSystem(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleSystem, text: text})
	t.reflow()
}

// addBanner appends a pre-rendered splash block (startup logo + config). It is
// emitted verbatim by renderBlock, so its colors and horizontal layout survive
// reflow untouched.
func (t *transcript) addBanner(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleBanner, text: text})
	t.reflow()
}

// addToolCard appends a rich tool-call card (#389) as an ordered block so it
// renders inline in the transcript. The card is held by pointer, so a later
// state change (toolEndMsg) or expand toggle (Ctrl+O) followed by reflow
// re-renders it in place.
func (t *transcript) addToolCard(c *toolCard) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleTool, card: c})
	t.reflow()
}

// appendThinking grows the current thinking block by delta, creating the block
// (and starting its "Thought for Xs" clock) on the first delta of a turn. The
// block stays open — dimmed and collapsed — until real reply text arrives or
// the turn ends.
func (t *transcript) appendThinking(delta string) {
	if t.activeThinking < 0 {
		t.blocks = append(t.blocks, transcriptBlock{role: roleThinking, started: time.Now()})
		t.activeThinking = len(t.blocks) - 1
		t.lastThinking = t.activeThinking
	}
	t.blocks[t.activeThinking].text += delta
	t.reflow()
}

// closeThinking seals the open thinking block: it stops the footer clock and
// detaches it as the delta target so subsequent text starts a fresh assistant
// block. A no-op when no thinking block is open.
func (t *transcript) closeThinking() {
	if t.activeThinking < 0 {
		return
	}
	blk := &t.blocks[t.activeThinking]
	blk.done = true
	blk.ended = time.Now()
	t.activeThinking = -1
	t.reflow()
}

// toggleThinking flips the collapsed/expanded two-state view of the most recent
// thinking block and re-flows (Ctrl+T). Blocks start collapsed; a later turn's
// block toggles independently, matching the tool-card Ctrl+O behavior.
func (t *transcript) toggleThinking() {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].role == roleThinking {
			t.blocks[i].expanded = !t.blocks[i].expanded
			t.reflow()
			return
		}
	}
}

// appendDelta grows the current assistant block by delta, creating the block on
// the first delta of a turn. The re-flow auto-sticks to the bottom when the user
// has not scrolled up. If the thinking block is still open this is the first
// real reply text, so it closes there first: the footer clock must stop when
// reasoning stopped, not when the turn ends.
func (t *transcript) appendDelta(delta string) {
	t.closeThinking()
	if t.activeAssistant < 0 {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant})
		t.activeAssistant = len(t.blocks) - 1
	}
	t.blocks[t.activeAssistant].text += delta
	t.reflow()
}

// finalizeTurn closes the streaming assistant block. When the final message
// carries text it becomes the block's authoritative body (covering turns that
// arrive without incremental deltas); otherwise the accumulated deltas stand.
// The thinking region is finalized the same way: an open block is closed for
// the footer, and a message that carries thinking the stream never surfaced
// (providers that only deliver the full message at turn end) gets a completed
// collapsed block so reasoning models stay visible on every transport.
func (t *transcript) finalizeTurn(msg agentcore.AssistantMessage) {
	if thinking := agentcore.ContentToThinking(msg.Content); thinking != "" {
		if t.activeThinking >= 0 {
			t.blocks[t.activeThinking].text = thinking
			t.closeThinking()
		} else if t.lastThinking >= 0 {
			// The stream already showed this turn's thinking: replace its body
			// with the authoritative final text rather than appending a
			// duplicate block.
			t.blocks[t.lastThinking].text = thinking
			t.reflow()
		} else {
			now := time.Now()
			t.blocks = append(t.blocks, transcriptBlock{
				role: roleThinking, text: thinking,
				done: true, started: now, ended: now,
			})
			t.lastThinking = len(t.blocks) - 1
			t.reflow()
		}
	} else {
		t.closeThinking()
	}
	t.lastThinking = -1
	text := agentcore.ContentToText(msg.Content)
	if t.activeAssistant >= 0 {
		if text != "" {
			t.blocks[t.activeAssistant].text = text
		}
	} else if text != "" {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant, text: text})
	}
	t.activeAssistant = -1
	t.reflow()
}

// update forwards a message (typically a key press or scroll) to the viewport so
// PgUp/PgDn/arrow scrolling works, then re-syncs the follow intent: scrolling up
// off the bottom pauses auto-scroll, and scrolling back to the bottom re-arms it.
func (t *transcript) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	t.vp, cmd = t.vp.Update(msg)
	t.follow = t.vp.AtBottom()
	return cmd
}

// scrollToRow positions the viewport so the scrollbar thumb aligns with the
// given viewport row y (0-based). It is the inverse of the thumb-position math
// in scrollbar(): pressing or dragging on row y maps that row to the matching
// scroll offset, so clicking the gutter jumps there and dragging the thumb
// tracks the cursor. It is a no-op when the content fits (nothing to scroll).
func (t *transcript) scrollToRow(y int) {
	h := t.vp.Height()
	if h <= 0 {
		return
	}
	total := t.vp.TotalLineCount()
	if total <= h {
		return
	}
	thumb := h * h / total
	if thumb < 2 {
		thumb = 2
	}
	if thumb > h {
		thumb = h
	}
	span := h - thumb // rows the thumb top can occupy
	if span <= 0 {
		return
	}
	// Center the grab on the thumb: aim its top at y minus half its body so the
	// cursor sits roughly mid-thumb, then clamp into the track.
	top := y - thumb/2
	if top < 0 {
		top = 0
	}
	if top > span {
		top = span
	}
	maxOff := total - h
	t.vp.SetYOffset(top * maxOff / span)
	t.follow = t.vp.AtBottom()
}

// viewportHeight reports the number of visible transcript rows, so the model can
// tell whether a mouse Y falls within the scrollable region.
func (t transcript) viewportHeight() int { return t.vp.Height() }

// overflowing reports whether the transcript has more content than fits in the
// viewport, i.e. there is history to scroll. relayout uses this to reserve the
// scrollbar column only when scrolling is possible, and view uses it to decide
// whether to attach the thumb at all.
func (t transcript) overflowing() bool {
	return t.vp.Height() > 0 && t.vp.TotalLineCount() > t.vp.Height()
}

// view renders the current visible slice of the transcript. When the content
// overflows the viewport a one-column vertical scrollbar is drawn down the right
// edge (FR-10): each viewport row is normalized to exactly the content width
// before the scrollbar cell is appended, so the bar sits flush against the
// terminal's right edge and a dangling SGR from Markdown rendering can never
// bleed into (and hide) the bar column. When everything fits there is nothing to
// scroll, so no bar is drawn and the viewport uses the full width (relayout
// releases the reserved column in that case).
func (t transcript) view() string {
	if !t.overflowing() {
		return t.vp.View()
	}

	bar := strings.Split(t.scrollbar(), "\n")
	body := strings.Split(t.vp.View(), "\n")

	// Fit every body line to exactly t.width columns (ANSI-aware pad/truncate),
	// terminating any open style so the bar cell renders on a clean slate.
	fit := lipgloss.NewStyle().Width(t.width).MaxWidth(t.width)

	var b strings.Builder
	for i := 0; i < len(bar); i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		line := ""
		if i < len(body) {
			line = body[i]
		}
		if t.width > 0 {
			b.WriteString(fit.Render(line))
		}
		b.WriteString(bar[i])
	}
	return b.String()
}

// scrollbar renders the one-column vertical scrollbar the height of the
// viewport. A proportional thumb marks the visible window and its position marks
// the scroll offset, so scrolling up through history moves the thumb; the
// remaining rows draw a thin groove (│). The thumb is drawn as a capsule like
// the macOS system scrollbar: a lower-half block ▄ caps the top and an upper-half
// block ▀ caps the bottom (their filled halves sit on the inner edges so the
// outer ends taper to rounded), with the full block █ filling the body rows
// between the caps. The thumb is never shorter than three rows, so the capsule
// always shows a body between its two rounded caps rather than collapsing to a
// flat blob. When the content fits (no overflow) the capsule fills the full
// height.
func (t transcript) scrollbar() string {
	h := t.vp.Height()
	if h <= 0 {
		return ""
	}
	total := t.vp.TotalLineCount()
	thumb := h
	pos := 0
	if total > h {
		thumb = h * h / total
		// Keep the capsule shape (rounded cap + body + rounded cap) by never
		// letting the thumb shrink below three rows; clamp down to the viewport
		// height when it is shorter than that.
		if thumb < 3 {
			thumb = 3
		}
		if thumb > h {
			thumb = h
		}
		maxOff := total - h
		off := t.vp.YOffset()
		if off > maxOff {
			off = maxOff
		}
		if maxOff > 0 {
			pos = off * (h - thumb) / maxOff
		}
	}
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case i < pos || i >= pos+thumb:
			b.WriteString(t.theme.ScrollTrack.Render("│"))
		case thumb >= 2 && i == pos:
			b.WriteString(t.theme.ScrollThumb.Render("▄"))
		case thumb >= 2 && i == pos+thumb-1:
			b.WriteString(t.theme.ScrollThumb.Render("▀"))
		default:
			b.WriteString(t.theme.ScrollThumb.Render("█"))
		}
	}
	return b.String()
}

// reflow re-renders every block to the current width and pushes the joined
// content into the viewport. When the follow intent is set it snaps to the
// bottom so new content auto-scrolls; otherwise the offset is preserved so
// reading history is not interrupted. follow is tracked in update/scrollToRow
// (user scroll) and addUser (new turn) rather than sampled here, because setSize
// resizes the viewport before reflow runs and an AtBottom() sample would misread.
//
// Width is decided here rather than in setSize so it stays correct as a run
// streams in new lines (which reach reflow via appendDelta/finalizeTurn, not
// setSize): the blocks are first laid out at the full width, and only if that
// overflows the viewport is one column handed back to the scrollbar and the
// blocks re-laid at totalWidth-1. When the content fits, the transcript keeps
// the full width and view() draws no bar.
func (t *transcript) reflow() {
	t.width = t.totalWidth
	t.vp.SetWidth(t.width)
	t.vp.SetContent(t.renderAll())

	// A narrower width never reduces the line count, so if the full-width layout
	// already overflows it still overflows at totalWidth-1: reserve the scrollbar
	// column and re-lay the blocks so the body never sits under the bar.
	if t.totalWidth > 0 && t.vp.TotalLineCount() > t.vp.Height() {
		t.width = t.totalWidth - 1
		t.vp.SetWidth(t.width)
		t.vp.SetContent(t.renderAll())
	}

	if t.follow {
		t.vp.GotoBottom()
	}
}

// renderAll joins every block, rendered to the current content width, into the
// transcript body string. Consecutive turns are separated by a blank line before
// a new user turn so requests read as visually distinct.
func (t *transcript) renderAll() string {
	var b strings.Builder
	for i, blk := range t.blocks {
		if i > 0 {
			b.WriteByte('\n')
			if blk.role == roleUser {
				b.WriteByte('\n')
			}
		}
		b.WriteString(t.renderBlock(blk, i == t.activeAssistant))
	}
	return b.String()
}

// renderBlock wraps a block's text to the content width and applies the role's
// theme style. Wrapping happens on the raw text (measured in display columns via
// WrapToWidth) before styling so ANSI escapes never confuse the width math and
// no double-width rune is split. A finalized assistant block is rendered as
// Markdown (fix #3, mirroring the REPL's turn-end render); the still-streaming
// block (streaming==true) stays plain text because Markdown can only be laid out
// once the whole block is known.
func (t transcript) renderBlock(blk transcriptBlock, streaming bool) string {
	if blk.role == roleTool && blk.card != nil {
		return blk.card.render(t.theme, t.width)
	}
	switch blk.role {
	case roleBanner:
		return blk.text
	case roleUser:
		return t.theme.User.Render(WrapToWidth(blk.text, t.width))
	case roleSystem:
		return t.theme.System.Render(WrapToWidth(blk.text, t.width))
	case roleThinking:
		return t.renderThinking(blk)
	default:
		if streaming {
			return t.theme.Assistant.Render(WrapToWidth(blk.text, t.width))
		}
		return renderMarkdown(blk.text, t.width)
	}
}

// renderThinking renders a reasoning-model thinking block: the body dimmed
// behind a left rule, collapsed by default to its first few lines with a
// hidden-lines hint, full when expanded (Ctrl+T). A closed block ends with the
// "Thought for Xs" footer, so a collapsed block still tells the user the model
// reasoned and for how long. The raw text renders plain (not markdown): while
// streaming the block is incomplete and markdown can only be laid out on the
// whole block, and the collapsed view truncates anyway — the quiet treatment,
// not formatting, is the point.
func (t transcript) renderThinking(blk transcriptBlock) string {
	body := WrapToWidth(blk.text, t.width-2)
	lines := strings.Split(body, "\n")
	visible := lines
	hidden := 0
	if !blk.expanded && len(lines) > thinkingCollapsedLines {
		hidden = len(lines) - thinkingCollapsedLines
		visible = lines[:thinkingCollapsedLines]
	}
	rule := t.theme.ThinkingBorder.Render("▌")
	var b strings.Builder
	for i, l := range visible {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(rule)
		if l != "" {
			b.WriteString(t.theme.Thinking.Render(l))
		}
	}
	if hidden > 0 {
		b.WriteString(t.theme.Thinking.Render(
			fmt.Sprintf("\n… %d lines hidden (ctrl+t to expand)", hidden)))
	}
	if blk.done {
		b.WriteString(t.theme.ThinkingFooter.Render(thinkingFooter(blk)))
	}
	return b.String()
}

// thinkingFooter builds the closed-state summary line: the reasoning duration
// measured from the first thinking delta to the first reply text (or turn
// end). A block whose stream never surfaced thinking has no measurable
// duration (started == ended), so the footer degrades to a plain "Thought".
func thinkingFooter(blk transcriptBlock) string {
	d := blk.ended.Sub(blk.started)
	if d <= 0 {
		return "\n✻ Thought"
	}
	return fmt.Sprintf("\n✻ Thought for %.1fs", d.Seconds())
}
