package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// thinkingMsgs builds N single-line thinking deltas, each unique by its x-run
// prefix ("x…x line"), so a given line's presence proves how much is rendered.
func thinkingMsgs(n int) []thinkingDeltaMsg {
	msgs := make([]thinkingDeltaMsg, n)
	for i := range msgs {
		msgs[i] = thinkingDeltaMsg{delta: strings.Repeat("x", i) + " line\n"}
	}
	return msgs
}

// lastThinkingLine returns the unique body of the n-th delta (0-based).
func lastThinkingLine(n int) string { return strings.Repeat("x", n-1) + " line" }

// TestTranscriptThinkingFlow streams thinking then reply text, asserting the
// fold semantics: the thinking block closes (footer clock stops) on the first
// real text delta, the reply starts a fresh assistant block, and the closed
// block's collapsed face is the single "Thought for" summary line (grok
// finished_display_mode=folded).
func TestTranscriptThinkingFlow(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, thinkingDeltaMsg{delta: "reasoning hard"})
	m = apply(t, m, thinkingDeltaMsg{delta: " about the problem"})
	m = apply(t, m, textDeltaMsg{delta: "the answer"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("reasoning hard about the problem"),
			agentcore.NewTextContent("the answer"),
		},
	}})

	if n := len(m.transcript.blocks); n != 2 {
		t.Fatalf("block count = %d, want 2 (thinking + assistant)", n)
	}
	thinking := m.transcript.blocks[0]
	if thinking.role != roleThinking || !thinking.done {
		t.Fatalf("block[0] = role %v done %v, want closed thinking", thinking.role, thinking.done)
	}
	if thinking.display != displayCollapsed {
		t.Errorf("closed thinking display = %v, want collapsed", thinking.display)
	}
	if m.transcript.blocks[1].role != roleAssistant || m.transcript.blocks[1].text != "the answer" {
		t.Fatalf("block[1] = %+v, want assistant text %q", m.transcript.blocks[1], "the answer")
	}
	if m.transcript.activeThinking != -1 {
		t.Errorf("activeThinking = %d after turn end, want -1", m.transcript.activeThinking)
	}
	// Assert on renderAll (the full rendered body), not View(): the viewport
	// only shows a height-limited slice and would clip the assertions.
	content := stripANSI(m.transcript.renderAll())
	if !strings.Contains(content, "Thought") {
		t.Errorf("rendered view missing 'Thought for' summary; got:\n%s", content)
	}
	if !strings.Contains(content, "the answer") {
		t.Errorf("rendered view missing reply text; got:\n%s", content)
	}
}

// TestTranscriptThinkingStreamsLiveBody verifies the in-progress block shows
// the "◇ Thinking…" header above the dimmed live body (grok running face),
// and that the body disappears into the summary line once closed.
func TestTranscriptThinkingStreamsLiveBody(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 12})

	m = apply(t, m, thinkingDeltaMsg{delta: "reasoning hard\n"})
	content := stripANSI(m.transcript.renderAll())
	if !strings.Contains(content, "Thinking…") {
		t.Errorf("streaming view missing the Thinking header; got:\n%s", content)
	}
	if !strings.Contains(content, "reasoning hard") {
		t.Errorf("streaming view should show the live body; got:\n%s", content)
	}

	m = apply(t, m, textDeltaMsg{delta: "answer"})
	content = stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "reasoning hard") {
		t.Errorf("closed collapsed view should fold the body away; got:\n%s", content)
	}
	if !strings.Contains(content, "Thought") {
		t.Errorf("closed collapsed view missing the summary; got:\n%s", content)
	}
}

// TestTranscriptThinkingCollapsedAndExpand covers the fold machine on a body
// longer than the collapsed cap: closed+collapsed renders only the summary,
// and Ctrl+T (short body skips the tail step) expands to the full text.
func TestTranscriptThinkingCollapsedAndExpand(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	lines := 15
	for _, msg := range thinkingMsgs(lines) {
		m = apply(t, m, msg)
	}
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("done")},
	}})

	content := stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "lines hidden") {
		t.Fatalf("collapsed view should fold the body entirely; got:\n%s", content)
	}
	if strings.Contains(content, lastThinkingLine(1)) {
		t.Errorf("collapsed view leaks body line %q", lastThinkingLine(1))
	}
	if !strings.Contains(content, "Thought") {
		t.Errorf("collapsed view missing the summary line")
	}

	m = apply(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	content = stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "lines hidden") {
		t.Errorf("expanded view still shows hidden-lines hint; got:\n%s", content)
	}
	// Expanded: the body is visible including the tail.
	if !strings.Contains(content, lastThinkingLine(lines)) {
		t.Errorf("expanded view missing tail line %q; got:\n%s", lastThinkingLine(lines), content)
	}
}

// TestTranscriptThinkingExpandedHeaderAboveBody pins the expanded layout:
// title on top, content below (grok card grammar). A 2026-10-07 user report
// found the footer-below-body face reading upside-down against grok.
func TestTranscriptThinkingExpandedHeaderAboveBody(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, thinkingDeltaMsg{delta: "reasoning hard\n"})
	m = apply(t, m, thinkingDeltaMsg{delta: " more\n"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("reasoning hard\nmore"),
			agentcore.NewTextContent("the answer"),
		},
	}})
	m = apply(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}) // expand

	content := stripANSI(m.transcript.renderAll())
	summary := strings.Index(content, "Thought")
	body := strings.Index(content, "reasoning hard")
	if summary < 0 || body < 0 {
		t.Fatalf("expanded view missing summary or body; got:\n%s", content)
	}
	if summary > body {
		t.Errorf("expanded view puts the summary below the body; got:\n%s", content)
	}
	if strings.HasSuffix(content, "Thought") || strings.Contains(content, "Thought\nreasoning hard\nmore\n\n") {
		t.Errorf("unexpected trailing summary; got:\n%s", content)
	}
}

// TestTranscriptThinkingFinalizeOnly covers providers that deliver thinking
// solely in the final message (no streaming deltas): finalizeTurn must create a
// completed collapsed thinking block so reasoning models stay visible.
func TestTranscriptThinkingFinalizeOnly(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("silent reasoning"),
			agentcore.NewTextContent("answer"),
		},
	}})

	if n := len(m.transcript.blocks); n != 2 {
		t.Fatalf("block count = %d, want 2 (thinking + assistant)", n)
	}
	thinking := m.transcript.blocks[0]
	if thinking.role != roleThinking || !thinking.done || thinking.text != "silent reasoning" {
		t.Fatalf("block[0] = %+v, want completed thinking block", thinking)
	}
}

// TestThinkingFooterVariants pins the summary wording: a measurable duration
// reports seconds; a block with no measurable duration (thinking delivered
// only at finalize) degrades to a plain "Thought".
func TestThinkingFooterVariants(t *testing.T) {
	now := time.Now()
	style := DefaultTheme().ToolVerbThink
	if got := stripANSI(thinkingFooter(transcriptBlock{done: true, started: now, ended: now.Add(2300 * time.Millisecond)}, style)); got != "◆ Thought for 2.3s" {
		t.Errorf("timed footer = %q, want %q", got, "◆ Thought for 2.3s")
	}
	if got := stripANSI(thinkingFooter(transcriptBlock{done: true, started: now, ended: now}, style)); got != "◆ Thought" {
		t.Errorf("instant footer = %q, want %q", got, "◆ Thought")
	}
}

// TestTranscriptThinkingTailWindow covers the three-state cycle on a body
// longer than the tail-window cap: collapsed (summary only) → tail-window
// (only the tail shown, the earlier-lines hint on top) → full → collapsed.
func TestTranscriptThinkingTailWindow(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	// Short, unique marker lines: the thinkingMsgs x-run prefix grows with the
	// line index, and lines past the 40-column width wrap mid-run, breaking
	// substring assertions.
	lines := thinkingTailWindowLines + 10
	m = apply(t, m, thinkingDeltaMsg{delta: "head-unique-line\n"})
	for i := 0; i < lines-2; i++ {
		m = apply(t, m, thinkingDeltaMsg{delta: fmt.Sprintf("filler line %03d\n", i)})
	}
	m = apply(t, m, thinkingDeltaMsg{delta: "tail-unique-line\n"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("done")},
	}})

	// Collapsed: summary only, no body.
	content := stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "head-unique-line") {
		t.Fatalf("collapsed view leaks the body; got:\n%s", content)
	}
	if !strings.Contains(content, "Thought") {
		t.Fatalf("collapsed view missing the summary line")
	}

	// First Ctrl+T on a long body lands on the tail window: the newest
	// reasoning is shown, the earlier lines are summarized on top.
	m = apply(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	content = stripANSI(m.transcript.renderAll())
	if !strings.Contains(content, "earlier lines hidden (ctrl+t for full)") {
		t.Fatalf("tail-window view missing earlier-lines hint; got:\n%s", content)
	}
	if !strings.Contains(content, "tail-unique-line") {
		t.Errorf("tail-window view missing the newest line")
	}
	if strings.Contains(content, "head-unique-line") {
		t.Errorf("tail-window view leaks the earliest line")
	}

	// Second Ctrl+T promotes to full: everything, no hints.
	m = apply(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	content = stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "hidden") {
		t.Errorf("full view still shows a hidden-lines hint; got:\n%s", content)
	}
	if !strings.Contains(content, "head-unique-line") || !strings.Contains(content, "tail-unique-line") {
		t.Errorf("full view missing head or tail line")
	}

	// Third Ctrl+T returns to collapsed.
	m = apply(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	content = stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "head-unique-line") {
		t.Errorf("third Ctrl+T did not return to the collapsed view; got:\n%s", content)
	}
}

// TestTranscriptStreamingMarkdownCacheLifecycle pins the T2.2 wiring at the
// transcript level: the streaming assistant block owns per-width stable-prefix
// caches, and they are dropped at finalize (a new turn is a new markdown
// document, so stale prefixes must never leak into it).
func TestTranscriptStreamingMarkdownCacheLifecycle(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 12)
	tr.addUser("hi")

	tr.appendDelta("# Heading\n\nbody ")
	if len(tr.streamMd) == 0 {
		t.Fatal("streaming render did not seed a cache entry")
	}
	tr.appendDelta("more")
	if len(tr.streamMd) == 0 {
		t.Fatal("subsequent delta dropped the cache mid-turn")
	}

	tr.finalizeTurn(agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("# Heading\n\nbody more")},
	})
	if tr.streamMd != nil {
		t.Fatal("streaming cache survived finalizeTurn")
	}

	// The next turn starts a fresh document with fresh caches.
	tr.addUser("next")
	tr.appendDelta("fresh doc")
	if tr.streamMd == nil {
		t.Fatal("new turn did not seed fresh streaming caches")
	}
}

// TestLiveThinkingFold covers the 2026-10-10 real-terminal report ("它思考时连
// 展开都不能展开"): while the model was still thinking, the block ignored its
// fold state and reported itself not-foldable, so Ctrl+T and a double click
// changed nothing. A live block now walks the same three states, with collapsed
// rendering as the activity header alone, and a fold chosen while streaming
// survives the turn ending.
func TestLiveThinkingFold(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	tr.addUser("hi")
	for i := 0; i < 13; i++ {
		tr.appendThinking(fmt.Sprintf("live filler line %02d\n", i))
	}
	tr.appendThinking("live newest line\n")
	idx := len(tr.blocks) - 1

	// Default live face: the tail window (newest visible, earliest hidden).
	out := stripANSI(tr.renderAll())
	if !strings.Contains(out, "◇ Thinking…") {
		t.Errorf("live block missing the activity header; got:\n%s", out)
	}
	if !strings.Contains(out, "live newest line") {
		t.Errorf("live tail face should show the newest reasoning; got:\n%s", out)
	}
	if strings.Contains(out, "live filler line 00") {
		t.Errorf("live tail face leaked the earliest reasoning; got:\n%s", out)
	}
	if !strings.Contains(out, "earlier lines hidden (ctrl+t for full)") {
		t.Errorf("live tail face missing the expansion hint; got:\n%s", out)
	}

	// The live block is foldable: Ctrl+T (here the shared toggle core) expands
	// the full body.
	if !tr.toggleBlock(idx) {
		t.Fatal("a live thinking block with a body must be foldable")
	}
	out = stripANSI(tr.renderAll())
	if !strings.Contains(out, "live filler line 00") || !strings.Contains(out, "live newest line") {
		t.Errorf("live full face should show the whole body; got:\n%s", out)
	}
	if strings.Contains(out, "hidden") {
		t.Errorf("live full face still shows a hidden-lines hint; got:\n%s", out)
	}

	// The next toggle collapses it to the header line alone.
	if !tr.toggleBlock(idx) {
		t.Fatal("the live block should keep folding")
	}
	out = stripANSI(tr.renderAll())
	if !strings.Contains(out, "◇ Thinking…") {
		t.Errorf("live collapsed face lost the activity header; got:\n%s", out)
	}
	if strings.Contains(out, "live newest line") {
		t.Errorf("live collapsed face should hide the body; got:\n%s", out)
	}

	// A fold chosen while streaming survives the turn ending (the block does
	// not snap back to the summary line the user just left).
	tr.closeThinking()
	if got := tr.blocks[idx].display; got != displayCollapsed {
		t.Errorf("closeThinking overrode the user's live fold: display = %v", got)
	}

	// Untouched live blocks still fold to the summary line at turn end.
	tr2 := newTranscript(DefaultTheme())
	tr2.setSize(60, 20)
	tr2.addUser("hi")
	tr2.appendThinking("some reasoning\n")
	tr2.closeThinking()
	if got := tr2.blocks[len(tr2.blocks)-1].display; got != displayCollapsed {
		t.Errorf("default live block display after close = %v, want collapsed", got)
	}
	if out := stripANSI(tr2.renderAll()); !strings.Contains(out, "Thought") {
		t.Errorf("closed default block missing the summary line; got:\n%s", out)
	}
}

// TestEmptyLiveThinkingNotFoldable keeps the empty-body guard: a live block
// whose reasoning stream has not produced text yet has nothing to expand, so
// the toggle reports not-foldable and the mouse path falls back to selection.
func TestEmptyLiveThinkingNotFoldable(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	tr.addUser("hi")
	tr.appendThinking("   \n")
	idx := len(tr.blocks) - 1
	if tr.toggleBlock(idx) {
		t.Error("an empty live thinking block must not report foldable")
	}
}

// TestEmptyThinkingRendersNothingNotFoldable covers the 2026-10-07 click
// stability report: providers that deliver only empty/whitespace thinking must
// not produce a "◆ Thought" row whose expanded view is visually identical
// (empty rail + footer) — that row read as a thinking block that never opens
// when clicked. It renders nothing and reports not-foldable, so a click falls
// back to text selection instead of being swallowed.
func TestEmptyThinkingRendersNothingNotFoldable(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("  \n\t "),
			agentcore.NewTextContent("answer"),
		},
	}})

	content := stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "Thought") {
		t.Errorf("whitespace-only thinking should not render a footer; got:\n%s", content)
	}
	if !strings.Contains(content, "answer") {
		t.Errorf("assistant reply missing; got:\n%s", content)
	}
	foldable := false
	for i := range m.transcript.blocks {
		if m.transcript.blocks[i].role == roleThinking && m.transcript.toggleBlock(i) {
			foldable = true
		}
	}
	if foldable {
		t.Error("whitespace-only thinking block should not be foldable")
	}
}

// TestHitMapLinesMatchViewport pins the hit-map accounting: the joining
// newline between blocks terminates the previous block's last line and must
// not consume a row of its own — counting it shifted every hit after the
// first block boundary, so clicks resolved real viewport rows against an
// inflated map and landed inside the previous block's body.
func TestHitMapLinesMatchViewport(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 30)
	tr.addUser("prompt")
	tr.appendDelta("reply body")
	tr.finalizeTurn(agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent("reply body")}})
	card := &toolCard{name: "bash", state: cardSuccess, input: map[string]any{"command": "ls"}}
	tr.addToolCard(card)

	total := tr.vp.TotalLineCount()
	var last *blockHit
	for i := range tr.hits {
		if tr.hits[i].block == len(tr.blocks)-1 {
			last = &tr.hits[i]
		}
	}
	if last == nil {
		t.Fatal("tool block missing from hit map")
	}
	if last.end != total {
		t.Fatalf("last hit end = %d, viewport total = %d — hit map desynced", last.end, total)
	}
	if got := tr.toggleInBlock(last.start); !got {
		t.Fatal("click resolved at the hit start line should toggle the tool block")
	}
}
