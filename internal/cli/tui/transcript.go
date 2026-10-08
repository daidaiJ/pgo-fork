package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/ui"
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
// cycles collapsed → tail-window (only when the body exceeds
// thinkingTailWindowLines) → full, and once real reply text starts (or the turn
// ends) the block closes and gains a "Thought for Xs" footer.
//
// Streaming assistant text (T2.2) renders through a stable-prefix markdown
// cache (streaming_markdown.go) so headings/lists/code light up while the
// reply is still arriving; the turn-end renderMarkdown pass stays the final
// layout authority.

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

// displayMode is the unified fold state of a block (tui-render-semantics.md C1).
// Every foldable block kind — thinking (Ctrl+T) and tool calls (Ctrl+O) — walks
// the same three states; non-foldable kinds (user/assistant/system/banner) sit
// at displayFull permanently. The former thinkingView and the tool card's
// expanded bool are folded into this one type.
type displayMode int

const (
	displayCollapsed displayMode = iota
	displayTail
	displayFull
)

// thinkingCollapsedLines and thinkingTailWindowLines are the fold thresholds of
// a thinking block: collapsed shows only the summary footer once closed (grok
// finished_display_mode=folded; the streaming body stays visible while the
// block is open), tail shows the last N lines, full shows everything.
const (
	thinkingCollapsedLines  = 10
	thinkingTailWindowLines = 200
)

// transcriptBlock is one rendered turn in the transcript. text is the raw
// (unstyled, unwrapped) message body; the role selects the theme style and any
// prefix applied at render time. For roleTool blocks text is unused and card
// points at the live tool card; the pointer lets a later toolEndMsg / Ctrl+O
// mutate the card in place and have it re-render on the next reflow.
//
// display is the block's fold state (C1). turn records which user prompt the
// block belongs to (C2) — history tool/thinking rows (turn < current) render
// dimmed (grok brightness layering, S6). started/ended time the thinking
// footer and the block timestamps (C5). cacheKey/cacheOut memoize the
// finalized render (C3); any mutation clears the key.
type transcriptBlock struct {
	role     blockRole
	text     string
	card     *toolCard
	display  displayMode
	done     bool
	started  time.Time
	ended    time.Time
	turn     int
	cacheKey blockCacheKey
	cacheOut string
}

// blockCacheKey captures every input that can change a finalized block's
// render: content width, fold state, history dim, and a content version (text
// length plus the done/ended flags, which is exact for finalized content —
// streaming never touches the cache). tool cards keep their own richer cache.
type blockCacheKey struct {
	width   int
	display displayMode
	dim     bool
	version int
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

	// turn is the current user-prompt counter (C2): addUser increments it and
	// every block created afterwards records it, so history tool/thinking rows
	// (turn < current) render dimmed (S6) and derived faces get a stable
	// generation key.
	turn int

	// follow is the stick-to-bottom intent: while true, every reflow snaps the
	// viewport to the newest line so streamed output stays visible. It is set
	// when the user submits a turn and cleared when they scroll up to read
	// history (re-armed when they scroll back to the bottom). Tracking intent
	// explicitly — rather than sampling viewport.AtBottom() inside reflow — keeps
	// auto-scroll correct across height changes (setSize resizes the viewport
	// before reflow runs, which would make an AtBottom() sample read false).
	follow bool

	// hits is the block hit map rebuilt by every renderAll: which block owns
	// which span of transcript content lines (see blockHit). It backs the
	// mouse click → block → toggle path (toggleInBlock); line spans are cheap
	// to recompute (no rendering), so no cache invalidation is needed.
	hits []blockHit

	// streamMd caches the stable-prefix streaming renders (T2.2) keyed by
	// content width. reflow can lay the transcript out at two widths (full and
	// full-1 once the scrollbar column is reserved), and interleaved renders at
	// both widths must not thrash a single cache. Entries are dropped when a
	// turn finalizes or a fresh assistant block starts, since each turn is a
	// new markdown document.
	streamMd map[int]*streamingMarkdown
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
// The turn counter increments first (C2): every block created from now on
// belongs to the new turn.
func (t *transcript) addUser(text string) {
	t.turn++
	t.blocks = append(t.blocks, transcriptBlock{
		role: roleUser, text: text, turn: t.turn, started: time.Now(),
	})
	t.activeAssistant = -1
	t.activeThinking = -1
	t.lastThinking = -1
	t.follow = true
	t.reflow()
}

// addSystem appends a system / meta notice (used for run lifecycle and other
// inline notes).
func (t *transcript) addSystem(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleSystem, text: text, turn: t.turn})
	t.reflow()
}

// addBanner appends a pre-rendered splash block (startup logo + config). It is
// emitted verbatim by renderBlock, so its colors and horizontal layout survive
// reflow untouched.
func (t *transcript) addBanner(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleBanner, text: text, turn: t.turn})
	t.reflow()
}

// addToolCard appends a rich tool-call card (#389) as an ordered block so it
// renders inline in the transcript. The card is held by pointer, so a later
// state change (toolEndMsg) or expand toggle (Ctrl+O) followed by reflow
// re-renders it in place. New cards start collapsed (S5): the single-line
// diamond summary is the default face, Ctrl+O expands to the full card.
func (t *transcript) addToolCard(c *toolCard) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleTool, card: c, turn: t.turn})
	t.reflow()
}

// appendThinking grows the current thinking block by delta, creating the block
// (and starting its "Thought for Xs" clock) on the first delta of a turn. The
// block stays open — header line plus dimmed rail body — until real reply text
// arrives or the turn ends.
func (t *transcript) appendThinking(delta string) {
	if t.activeThinking < 0 {
		t.blocks = append(t.blocks, transcriptBlock{
			role: roleThinking, started: time.Now(), turn: t.turn,
		})
		t.activeThinking = len(t.blocks) - 1
		t.lastThinking = t.activeThinking
	}
	t.blocks[t.activeThinking].text += delta
	t.reflow()
}

// closeThinking seals the open thinking block: it stops the footer clock and
// detaches it as the delta target so subsequent text starts a fresh assistant
// block. A no-op when no thinking block is open. The closed block defaults to
// collapsed — a single "Thought for Xs" summary line (grok
// finished_display_mode=folded) — still expandable via Ctrl+T.
func (t *transcript) closeThinking() {
	if t.activeThinking < 0 {
		return
	}
	blk := &t.blocks[t.activeThinking]
	blk.done = true
	blk.ended = time.Now()
	blk.display = displayCollapsed
	t.activeThinking = -1
	t.reflow()
}

// toggleThinking cycles the three-state fold of the most recent thinking block
// and re-flows (Ctrl+T, C1): collapsed → tail-window → full → collapsed, with
// the tail-window step skipped when the raw body fits within
// thinkingTailWindowLines so short blocks keep the two-click toggle. The skip
// heuristic counts raw source lines (cheap; no re-render just to count) and
// can over-trigger on many short lines, where the tail-window render is
// visually identical to full and the cycle costs one extra press — preferred
// over failing to offer the affordance on a genuinely long block.
func (t *transcript) toggleThinking() {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].role == roleThinking {
			t.toggleBlock(i)
			return
		}
	}
}

// toggleTool cycles the most recent tool block's fold state (Ctrl+O, C1+S5):
// collapsed diamond row → card with capped response → card with the full
// response tree → back to the collapsed row.
func (t *transcript) toggleTool() {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].role == roleTool && t.blocks[i].card != nil {
			t.toggleBlock(i)
			return
		}
	}
}

// toggleBlock cycles block i's fold state — the shared core of Ctrl+O/Ctrl+T
// and of mouse clicks on a block's header row (2026-10-07 interaction batch).
// Tool blocks walk collapsed → full → expanded response tree → collapsed;
// thinking blocks walk collapsed → tail (long bodies) → full → collapsed.
// Other roles report false (nothing to fold) so the caller can fall back to
// its alternate semantics (text selection for the mouse).
func (t *transcript) toggleBlock(i int) bool {
	blk := &t.blocks[i]
	switch blk.role {
	case roleTool:
		if blk.card == nil {
			return false
		}
		switch {
		case blk.display == displayCollapsed:
			blk.display = displayFull
			blk.card.expanded = false
		case !blk.card.expanded:
			blk.card.expanded = true
		default:
			blk.display = displayCollapsed
			blk.card.expanded = false
		}
		blk.card.clearCache()
		blk.cacheKey = blockCacheKey{}
	case roleThinking:
		if !blk.done || strings.TrimSpace(blk.text) == "" {
			// A live thinking block renders the streaming body regardless of
			// its fold state, and a closed one whose body is whitespace-only
			// renders nothing at all (see renderThinking) — a toggle would be
			// a swallowed click with no visual answer in both cases, so report
			// not-foldable instead (the mouse path falls back to text
			// selection, Ctrl+T is a no-op).
			return false
		}
		switch blk.display {
		case displayCollapsed:
			if 1+strings.Count(blk.text, "\n") > thinkingTailWindowLines {
				blk.display = displayTail
			} else {
				blk.display = displayFull
			}
		case displayTail:
			blk.display = displayFull
		default:
			blk.display = displayCollapsed
		}
	default:
		return false
	}
	t.anchorReflow(i)
	return true
}

// anchorReflow re-flows after a fold toggle while keeping the toggled block's
// first line on its current screen row (grok-style scroll anchoring). A plain
// reflow snaps back to the bottom whenever follow is armed — and reading at
// the bottom is the common case — which shoves the row the user just clicked
// off target by the height delta of the toggle; the next click then lands on
// whatever scrolled into that row and the fold interaction reads as unreliable
// (2026-10-07 user report: 点击折叠/展开不能稳定触发). Blocks above i are
// untouched by the toggle, so its start line is identical in the fresh layout
// and restoring the old offset is enough to pin the row.
func (t *transcript) anchorReflow(i int) {
	start, row := -1, -1
	for _, h := range t.hits {
		if h.block == i {
			start = h.start
			row = start - t.vp.YOffset()
			break
		}
	}
	wasFollow := t.follow
	t.follow = false
	t.reflow()
	switch {
	case row >= 0 && row < t.vp.Height():
		// The row was on screen: pin it (SetYOffset clamps to the new range).
		t.vp.SetYOffset(start - row)
	case wasFollow:
		// The block start sat above the viewport: keep the old bottom-stick
		// behavior rather than silently scrolling the content under the cursor.
		t.vp.GotoBottom()
	}
	t.follow = t.vp.AtBottom()
}

// appendDelta grows the current assistant block by delta, creating the block on
// the first delta of a turn. The re-flow auto-sticks to the bottom when the user
// has not scrolled up. If the thinking block is still open this is the first
// real reply text, so it closes there first: the footer clock must stop when
// reasoning stopped, not when the turn ends. A freshly created block starts a
// new markdown document, so any stale streaming caches from a previous turn are
// dropped.
func (t *transcript) appendDelta(delta string) {
	t.closeThinking()
	if t.activeAssistant < 0 {
		// Providers commonly emit whitespace-only text around tool calls; a
		// block seeded with only that whitespace would render as a run of blank
		// lines, so the block waits for the first real content instead.
		if strings.TrimSpace(delta) == "" {
			return
		}
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant, turn: t.turn, started: time.Now()})
		t.activeAssistant = len(t.blocks) - 1
		t.streamMd = nil
	}
	blk := &t.blocks[t.activeAssistant]
	blk.text += delta
	blk.cacheKey = blockCacheKey{} // streaming content: never serve the cache
	t.reflow()
}

// streamRender renders the still-streaming assistant body through the
// stable-prefix markdown cache for the given width, creating the per-width
// cache entry on first use (reflow lays the transcript out at two widths, so
// entries must coexist; see the streamMd field comment).
func (t *transcript) streamRender(text string, width int) string {
	if t.streamMd == nil {
		t.streamMd = map[int]*streamingMarkdown{}
	}
	sm := t.streamMd[width]
	if sm == nil {
		sm = &streamingMarkdown{}
		t.streamMd[width] = sm
	}
	return renderMarkdownStreaming(sm, text, width)
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
			blk := &t.blocks[t.lastThinking]
			blk.text = thinking
			blk.cacheKey = blockCacheKey{}
			t.reflow()
		} else {
			now := time.Now()
			t.blocks = append(t.blocks, transcriptBlock{
				role: roleThinking, text: thinking, turn: t.turn,
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
		if strings.TrimSpace(t.blocks[t.activeAssistant].text) == "" {
			// A reply that never carried real text (whitespace around tool
			// calls) is dropped rather than rendered as blank lines.
			t.blocks = append(t.blocks[:t.activeAssistant], t.blocks[t.activeAssistant+1:]...)
		} else {
			blk := &t.blocks[t.activeAssistant]
			blk.done = true
			blk.ended = time.Now()
			blk.cacheKey = blockCacheKey{} // the authoritative body may differ in content
		}
	} else if strings.TrimSpace(text) != "" {
		t.blocks = append(t.blocks, transcriptBlock{
			role: roleAssistant, text: text, turn: t.turn,
			done: true, started: time.Now(), ended: time.Now(),
		})
	}
	t.activeAssistant = -1
	t.streamMd = nil
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
// a new user turn so requests read as visually distinct. While joining it also
// rebuilds the hit map (hits): each rendered block's content-line span, so a
// mouse click on a visible row can be resolved back to its block.
func (t *transcript) renderAll() string {
	var b strings.Builder
	line := 0
	t.hits = t.hits[:0]
	for i := range t.blocks {
		out := t.renderBlock(&t.blocks[i], i == t.activeAssistant)
		if out == "" {
			continue // dropped block contributes no lines and no separator
		}
		if b.Len() > 0 {
			// Joining newline: terminates the previous block's last line, so
			// it consumes no row of its own — counting it shifted every hit
			// after the first block boundary by one line per boundary, and
			// clicks (which resolve real viewport rows) landed inside the
			// previous block's body instead of on the header row
			// (2026-10-07 user report: 点击折叠不能稳定触发).
			b.WriteByte('\n')
			if t.blocks[i].role == roleUser {
				// Blank separator line before a user turn: this one is a real
				// viewport row.
				b.WriteByte('\n')
				line++
			}
		}
		n := strings.Count(out, "\n") + 1
		t.hits = append(t.hits, blockHit{block: i, start: line, end: line + n})
		line += n
		b.WriteString(out)
	}
	return b.String()
}

// blockHit maps one block onto its span of transcript content lines
// ([start, end), 0-based) at the width of the last renderAll. reflow renders
// up to twice (full width, then minus the scrollbar column) and only the last
// pass matches what the viewport shows, so hits always describe the visible
// layout.
type blockHit struct {
	block      int
	start, end int
}

// transcriptOriginRow is the screen row (0-based) of the transcript's first
// viewport row: renderContent paints exactly one header line above it. A mouse
// click maps screenY → content line as (screenY - origin) + viewport offset.
const transcriptOriginRow = 1

// lineAt resolves a screen row (0-based) to a transcript content line, and
// reports false when the row falls outside the viewport (header above,
// input/status below) or before the first size message.
func (t *transcript) lineAt(screenY int) (int, bool) {
	row := screenY - transcriptOriginRow
	if row < 0 || row >= t.vp.Height() {
		return 0, false
	}
	return row + t.vp.YOffset(), true
}

// toggleInBlock toggles the foldable block occupying content line line — any
// row inside the block's span counts, matching the grok prototype's
// double-click fold (selection.rs: member rows double-click fold like any
// other entry; only the X axis never participates in block hits). It reports
// false when the line falls between blocks or inside a non-foldable block.
func (t *transcript) toggleInBlock(line int) bool {
	for _, h := range t.hits {
		if line < h.start {
			break
		}
		if line >= h.end {
			continue
		}
		return t.toggleBlock(h.block)
	}
	return false
}

// renderBlock renders one block to the current content width. Finalized
// non-streaming blocks serve repeat renders from the per-block cache (C3) —
// key = width + fold state + history dim + content version — so scrolling and
// resizing a long conversation no longer re-renders every settled block.
// Streaming assistant blocks bypass the cache and render through the
// stable-prefix streaming markdown path (T2.2).
func (t *transcript) renderBlock(blk *transcriptBlock, streaming bool) string {
	dim := t.historyDim(blk)
	switch blk.role {
	case roleTool:
		if blk.card == nil {
			return ""
		}
		return blk.card.render(t.theme, t.width, blk.display == displayFull, dim)
	case roleBanner:
		return t.cached(blk, centerLines(blk.text, t.width), dim)
	}

	if streaming {
		return t.streamRender(blk.text, t.width)
	}

	// Seeded history can carry whitespace-only assistant messages (providers
	// emit newlines around tool calls); they render as nothing rather than a
	// run of blank lines.
	if blk.role == roleAssistant && strings.TrimSpace(blk.text) == "" {
		return ""
	}

	key := blockCacheKey{
		width:   t.width,
		display: blk.display,
		dim:     dim,
		version: len(blk.text),
	}
	if blk.done {
		key.version++
	}
	if blk.role == roleThinking && blk.display == displayCollapsed && blk.done {
		key.version += 2 // closed-collapsed renders the footer line only
	}
	if blk.cacheKey == key && blk.cacheOut != "" {
		return blk.cacheOut
	}
	var out string
	switch blk.role {
	case roleUser:
		out = t.renderUserBand(blk)
	case roleSystem:
		out = t.theme.System.Render(WrapToWidth(blk.text, t.width))
	case roleThinking:
		out = t.renderThinking(*blk, dim)
	default:
		out = renderMarkdown(blk.text, t.width)
		if blk.done && !blk.ended.IsZero() {
			// Block timestamp (C5/S10): right-aligned dim, on the last content
			// line when it fits, else on its own line (grok assistant block).
			out = withRightMeta(out, t.width, timestamp(blk.ended), t.theme.Chrome)
		}
	}
	blk.cacheKey = key
	blk.cacheOut = out
	return out
}

// cached returns the verbatim render for a non-wrapping block (banner) through
// the same cache machinery.
func (t *transcript) cached(blk *transcriptBlock, out string, dim bool) string {
	key := blockCacheKey{width: t.width, dim: dim, version: len(blk.text)}
	if blk.cacheKey == key && blk.cacheOut != "" {
		return blk.cacheOut
	}
	blk.cacheKey = key
	blk.cacheOut = out
	return out
}

// historyDim reports whether a block renders dimmed as past activity (S6):
// tool and thinking rows from earlier user turns drop to the chrome gray while
// the current turn's activity stays bright. User/assistant/system blocks are
// never dimmed (grok keeps the assistant body bright).
func (t *transcript) historyDim(blk *transcriptBlock) bool {
	return blk.turn < t.turn && (blk.role == roleTool || blk.role == roleThinking)
}

// renderUserBand draws the user turn as a full-width background band one shade
// above the terminal background: "❯" prompt at the left edge, the wrapped body,
// and the block's timestamp right-aligned on the first line (S3/S4, grok user
// band). Every line is padded to the full width so the band reads as one bar.
func (t *transcript) renderUserBand(blk *transcriptBlock) string {
	const prompt = "❯ "
	body := WrapToWidth(blk.text, max(t.width-ui.Width(prompt), 1))
	lines := strings.Split(body, "\n")
	ts := timestamp(blk.started)
	tsW := ui.Width(ts)
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		content := prompt + l
		cw := ui.Width(content)
		// First line carries the right-aligned timestamp; on a too-narrow
		// terminal the timestamp drops rather than overlapping the text.
		if i == 0 && t.width-cw >= tsW+2 {
			content += strings.Repeat(" ", t.width-cw-tsW) + ts
		} else if cw < t.width {
			content += strings.Repeat(" ", t.width-cw)
		}
		out = append(out, t.theme.UserBand.Render(content))
	}
	return strings.Join(out, "\n")
}

// renderThinking renders a reasoning-model thinking block (S9, grok alignment):
// while streaming it shows the "◇ Thinking…" activity header above the dimmed
// rail body; once closed, the collapsed state is a single purple
// "◆ Thought for Xs" summary line (grok finished_display_mode=folded) and the
// tail/full states show the summary header above the dimmed rail body — title
// on top, content below (2026-10-07 user report: footer-below-body read
// upside-down against grok). The raw text renders plain (not markdown): while
// streaming the block is incomplete and markdown can only be laid out on the
// whole block, and the collapsed view truncates anyway — the quiet treatment,
// not formatting, is the point.
func (t transcript) renderThinking(blk transcriptBlock, dim bool) string {
	headStyle, bodyStyle := t.theme.ToolVerbThink, t.theme.Thinking
	rail := t.theme.ThinkingBorder.Render("▌")
	if dim {
		headStyle, bodyStyle = t.theme.Chrome, t.theme.Chrome
		rail = t.theme.Chrome.Render("▌")
	}

	footer := ""
	if blk.done {
		footer = thinkingFooter(blk, headStyle)
	}
	// Closed with no real reasoning body: render nothing — a "◆ Thought"
	// footer over an empty rail would read as a thinking row that never
	// expands when clicked (2026-10-07 user report). Providers that only
	// deliver the full message at turn end routinely carry empty thinking.
	if blk.done && strings.TrimSpace(blk.text) == "" {
		return ""
	}
	// Closed and collapsed: the summary line is the whole render (grok card).
	if blk.done && blk.display == displayCollapsed {
		return footer
	}

	var b strings.Builder
	if blk.done {
		// Expanded closed state: the summary header sits above the body —
		// same row grammar as the streaming header and the grok card
		// (title top, content below).
		b.WriteString(footer)
		b.WriteByte('\n')
	} else {
		b.WriteString(t.theme.Chrome.Render("◇ Thinking…"))
		b.WriteByte('\n')
	}
	body := WrapToWidth(blk.text, max(t.width-2, 1))
	lines := strings.Split(body, "\n")
	visible := lines
	var head []string
	switch {
	case blk.done && blk.display == displayTail && len(lines) > thinkingTailWindowLines:
		hidden := len(lines) - thinkingTailWindowLines
		visible = lines[hidden:]
		head = []string{fmt.Sprintf("… %d earlier lines hidden (ctrl+t for full)", hidden)}
	case !blk.done && len(lines) > thinkingCollapsedLines:
		// While streaming, cap the live body so a long reasoning run does not
		// push the reply off-screen; the newest reasoning is what matters.
		hidden := len(lines) - thinkingCollapsedLines
		visible = lines[hidden:]
		head = []string{fmt.Sprintf("… %d earlier lines hidden", hidden)}
	}
	for _, l := range head {
		b.WriteString(bodyStyle.Render(l))
		b.WriteByte('\n')
	}
	for i, l := range visible {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(rail)
		if l != "" {
			b.WriteString(bodyStyle.Render(l))
		}
	}
	return b.String()
}

// thinkingFooter builds the closed-state summary line: the reasoning duration
// measured from the first thinking delta to the first reply text (or turn
// end). A block whose stream never surfaced thinking has no measurable
// duration (started == ended), so the footer degrades to a plain "Thought".
func thinkingFooter(blk transcriptBlock, style lipgloss.Style) string {
	d := blk.ended.Sub(blk.started)
	diamond := style.Render("◆ ")
	if d <= 0 {
		return diamond + style.Render("Thought")
	}
	return diamond + style.Render("Thought") + timestring(d)
}

// timestring renders " for Xs" in the dim chrome style (footer tail).
func timestring(d time.Duration) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGray)).
		Render(fmt.Sprintf(" for %.1fs", d.Seconds()))
}

// timestamp formats a block's wall-clock time the way the grok reference does:
// 12-hour "2:19 PM".
func timestamp(t time.Time) string { return t.Format("3:04 PM") }

// withRightMeta appends meta right-aligned onto the last non-empty line of a
// rendered block when at least two columns of separation fit, else onto its
// own right-aligned line. ANSI styling of the base render is preserved; the
// meta text arrives pre-styled.
func withRightMeta(rendered string, width int, meta string, metaStyle lipgloss.Style) string {
	lines := strings.Split(rendered, "\n")
	tw := ui.Width(meta)
	if tw >= width {
		return rendered
	}
	for i := len(lines) - 1; i >= 0; i-- {
		lw := ui.Width(lines[i])
		if lw == 0 {
			continue
		}
		if lw+2+tw <= width {
			lines[i] = lines[i] + strings.Repeat(" ", width-lw-tw) + metaStyle.Render(meta)
			return strings.Join(lines, "\n")
		}
		break
	}
	pad := ""
	if width > tw {
		pad = strings.Repeat(" ", width-tw)
	}
	return rendered + "\n" + pad + metaStyle.Render(meta)
}
