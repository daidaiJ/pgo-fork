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
