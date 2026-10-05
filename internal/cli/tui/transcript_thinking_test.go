package tui

import (
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
// two-state view: the thinking block closes (footer clock stops) on the first
// real text delta, the reply starts a fresh assistant block, and the finished
// view carries the "Thought for" footer.
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
	if m.transcript.blocks[1].role != roleAssistant || m.transcript.blocks[1].text != "the answer" {
		t.Fatalf("block[1] = %+v, want assistant text %q", m.transcript.blocks[1], "the answer")
	}
	if m.transcript.activeThinking != -1 {
		t.Errorf("activeThinking = %d after turn end, want -1", m.transcript.activeThinking)
	}
	// Assert on renderAll (the full rendered body), not View(): the viewport
	// only shows a height-limited slice and would clip the assertions.
	content := stripANSI(m.transcript.renderAll())
	if !strings.Contains(content, "✻ Thought") {
		t.Errorf("rendered view missing 'Thought for' footer; got:\n%s", content)
	}
	if !strings.Contains(content, "the answer") {
		t.Errorf("rendered view missing reply text; got:\n%s", content)
	}
}

// TestTranscriptThinkingCollapsedAndExpand covers the two-state view machine:
// a block longer than the collapsed cap renders its first lines plus a
// hidden-lines hint, and Ctrl+T expands to the full reasoning text.
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
	if !strings.Contains(content, "lines hidden (ctrl+t to expand)") {
		t.Fatalf("collapsed view missing hidden-lines hint; got:\n%s", content)
	}
	// Collapsed shows the first cap lines but not the tail.
	if !strings.Contains(content, lastThinkingLine(1)) {
		t.Errorf("collapsed view missing first thinking line")
	}
	if strings.Contains(content, lastThinkingLine(lines)) {
		t.Errorf("collapsed view leaks the tail line %q", lastThinkingLine(lines))
	}

	m = apply(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	content = stripANSI(m.transcript.renderAll())
	if strings.Contains(content, "lines hidden") {
		t.Errorf("expanded view still shows hidden-lines hint; got:\n%s", content)
	}
	// Expanded: the tail line (hidden while collapsed) is now visible.
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

// TestThinkingFooterVariants pins the footer wording: a measurable duration
// reports seconds; a block with no measurable duration (thinking delivered
// only at finalize) degrades to a plain "Thought".
func TestThinkingFooterVariants(t *testing.T) {
	now := time.Now()
	if got := thinkingFooter(transcriptBlock{done: true, started: now, ended: now.Add(2300 * time.Millisecond)}); got != "\n✻ Thought for 2.3s" {
		t.Errorf("timed footer = %q, want %q", got, "\n✻ Thought for 2.3s")
	}
	if got := thinkingFooter(transcriptBlock{done: true, started: now, ended: now}); got != "\n✻ Thought" {
		t.Errorf("instant footer = %q, want %q", got, "\n✻ Thought")
	}
}
