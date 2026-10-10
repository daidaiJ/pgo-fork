package tui

import (
	"strings"
	"testing"

	"charm.land/bubbletea/v2"
)

// This file pins the T8.3 queue semantics: the visible pane, the send-now
// interjection (Alt+Enter), the interrupt freeze (the queue never auto-starts
// after an Esc) and the manual resume gestures.

var (
	enterKey    = tea.KeyPressMsg{Code: tea.KeyEnter}
	altEnterKey = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	delKey      = tea.KeyPressMsg{Code: tea.KeyDelete}
)

// queueTestModel builds a session-less model whose run starter records the
// prompts it receives, with a WindowSizeMsg applied so layout and View are
// live.
func queueTestModel(t *testing.T, prompts *[]string) Model {
	t.Helper()
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		*prompts = append(*prompts, prompt)
		return make(chan tea.Msg, 1), nil
	}
	return m
}

// beginQueueRun types a prompt and presses Enter so the run is in flight.
func beginQueueRun(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeKeys(t, m, text)
	m = apply(t, m, enterKey)
	if !m.running {
		t.Fatalf("run should be in flight after submitting %q", text)
	}
	return m
}

func typeKeys(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m = apply(t, m, runeKey(r))
	}
	return m
}

// TestSendNowJumpsQueue verifies the interjection ordering (grok alignment):
// Alt+Enter while a run streams starts when that run ends, ahead of
// previously queued prompts, which keep their order behind it.
func TestSendNowJumpsQueue(t *testing.T) {
	var prompts []string
	m := queueTestModel(t, &prompts)
	m = beginQueueRun(t, m, "one")
	m = typeKeys(t, m, "first-in-line")
	m = apply(t, m, enterKey)
	m = typeKeys(t, m, "urgent")
	m = apply(t, m, altEnterKey)
	if len(m.queued) != 1 || m.queued[0] != "first-in-line" {
		t.Fatalf("queued = %v, want [first-in-line]", m.queued)
	}
	if len(m.sendNow) != 1 || m.sendNow[0] != "urgent" {
		t.Fatalf("sendNow = %v, want [urgent]", m.sendNow)
	}
	if !hasSystemBlockContaining(m.transcript, "send-now") {
		t.Errorf("expected a send-now note, blocks=%v", blockTexts(m.transcript))
	}
	// Run ends: the send-now prompt starts first; the queued one stays.
	m = apply(t, m, runEndMsg{})
	if len(prompts) != 2 || prompts[1] != "urgent" {
		t.Fatalf("prompts = %v, want the send-now prompt to start first", prompts)
	}
	if len(m.queued) != 1 || m.queued[0] != "first-in-line" {
		t.Errorf("queued prompt must stay behind the send-now, got %v", m.queued)
	}
	// The send-now run ends: the plain queue drains next.
	m = apply(t, m, runEndMsg{})
	if len(prompts) != 3 || prompts[2] != "first-in-line" {
		t.Fatalf("prompts = %v, want the queued prompt last", prompts)
	}
}

// TestQueueHeldAfterInterrupt pins ruling ①: Esc interrupts the run and
// freezes the queue — runEndMsg keeps the queued prompts, and a bare Enter
// (the manual trigger) promotes the front one and unfreezes.
func TestQueueHeldAfterInterrupt(t *testing.T) {
	var prompts []string
	interrupted := false
	m := queueTestModel(t, &prompts)
	m.interruptFn = func() { interrupted = true }
	m = beginQueueRun(t, m, "one")
	m = typeKeys(t, m, "two")
	m = apply(t, m, enterKey)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !interrupted || !m.queueHeld {
		t.Fatalf("interrupt should freeze the queue, interrupted=%v held=%v", interrupted, m.queueHeld)
	}
	m = apply(t, m, runEndMsg{})
	if len(prompts) != 1 {
		t.Fatalf("held queue must not auto-start, prompts=%v", prompts)
	}
	if len(m.queued) != 1 {
		t.Errorf("queued prompt must be kept, got %v", m.queued)
	}
	if !hasSystemBlockContaining(m.transcript, "queue held") {
		t.Errorf("expected a queue-held note, blocks=%v", blockTexts(m.transcript))
	}
	m = apply(t, m, enterKey)
	if len(prompts) != 2 || prompts[1] != "two" {
		t.Fatalf("bare Enter should promote the held prompt, prompts=%v", prompts)
	}
	if m.queueHeld {
		t.Errorf("promote must unfreeze the queue")
	}
}

// TestQueueHeldResumesOnManualSubmit pins the other manual trigger: a fresh
// submit while held runs immediately and unfreezes, so the held queue drains
// normally at the following run end.
func TestQueueHeldResumesOnManualSubmit(t *testing.T) {
	var prompts []string
	m := queueTestModel(t, &prompts)
	m.interruptFn = func() {}
	m = beginQueueRun(t, m, "one")
	m = typeKeys(t, m, "two")
	m = apply(t, m, enterKey)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = apply(t, m, runEndMsg{})
	m = typeKeys(t, m, "three")
	m = apply(t, m, enterKey)
	if len(prompts) != 2 || prompts[1] != "three" {
		t.Fatalf("fresh submit should start first, prompts=%v", prompts)
	}
	if m.queueHeld {
		t.Errorf("submit must unfreeze the queue")
	}
	m = apply(t, m, runEndMsg{})
	if len(prompts) != 3 || prompts[2] != "two" {
		t.Fatalf("prompts = %v, want the held queue to drain after the manual run", prompts)
	}
}

// TestQueuePaneRowsAndDelete pins the pane's minimal face: "#N {first line}"
// rows with a " (+N lines)" suffix, Del inert without a selection, and
// ↓-armed selection + Del removing the picked row with renumbering.
func TestQueuePaneRowsAndDelete(t *testing.T) {
	var prompts []string
	m := queueTestModel(t, &prompts)
	m = beginQueueRun(t, m, "one")
	m = typeKeys(t, m, "alpha")
	m = apply(t, m, enterKey)
	m.queued = append(m.queued, "beta\nbeta2")
	rows := m.queueView(m.theme, 80)
	if !strings.Contains(stripANSI(rows), "#1 alpha") || !strings.Contains(stripANSI(rows), "#2 beta") || !strings.Contains(stripANSI(rows), "(+1 lines)") {
		t.Fatalf("queue rows = %q", rows)
	}
	// Without an armed selection Del is a no-op — it must never destroy a row
	// the user has not explicitly picked.
	m = apply(t, m, delKey)
	if len(m.queued) != 2 {
		t.Fatalf("Del without a selection must not delete, queued=%v", m.queued)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.qpane.selecting || m.qpane.selected != 0 {
		t.Fatalf("↓ should arm the selection, selecting=%v selected=%d", m.qpane.selecting, m.qpane.selected)
	}
	m = apply(t, m, delKey)
	if len(m.queued) != 1 || m.queued[0] != "beta\nbeta2" {
		t.Fatalf("queued = %v, want the unpicked row kept", m.queued)
	}
	if rows := m.queueView(m.theme, 80); !strings.Contains(stripANSI(rows), "#1 beta") {
		t.Fatalf("rows after delete = %q", rows)
	}
	if !hasSystemBlockContaining(m.transcript, "removed from queue") {
		t.Errorf("expected a removal note, blocks=%v", blockTexts(m.transcript))
	}
}

// TestQueuePaneHeldVisibleAndQuitClears pins visibility while idle-held and
// ruling ②'s exit semantics: quitting drops the whole queue.
func TestQueuePaneHeldVisibleAndQuitClears(t *testing.T) {
	var prompts []string
	m := queueTestModel(t, &prompts)
	m.interruptFn = func() {}
	m = beginQueueRun(t, m, "one")
	m = typeKeys(t, m, "two")
	m = apply(t, m, enterKey)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = apply(t, m, runEndMsg{})
	view, _ := m.renderContent()
	if !strings.Contains(stripANSI(view), "#1 two") {
		t.Fatalf("held queue must stay visible above the input, view=%q", view)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(m.queued) != 0 || len(m.sendNow) != 0 {
		t.Fatalf("quit should clear the queue, queued=%v sendNow=%v", m.queued, m.sendNow)
	}
}

// TestQueueSlashEntriesDoNotStall pins the drain's inline execution: a
// slash-command entry starts no run, so the drain keeps going until a real
// prompt starts instead of leaving the queue stuck while idle.
func TestQueueSlashEntriesDoNotStall(t *testing.T) {
	var prompts []string
	m := queueTestModel(t, &prompts)
	m = beginQueueRun(t, m, "one")
	m.queued = append(m.queued, "/definitely-not-a-command", "two")
	m = apply(t, m, runEndMsg{})
	if len(prompts) != 2 || prompts[1] != "two" {
		t.Fatalf("prompts = %v, want the drain to pass the slash entry", prompts)
	}
	if len(m.queued) != 0 {
		t.Errorf("queue should be empty, got %v", m.queued)
	}
}

// TestEnqueueExpandsPastes pins the placeholder fix: a prompt queued mid-run
// carries the real paste body (runEndMsg starts it verbatim) and the consumed
// paste store is dropped, mirroring the submit path.
func TestEnqueueExpandsPastes(t *testing.T) {
	var prompts []string
	m := queueTestModel(t, &prompts)
	m = beginQueueRun(t, m, "one")
	m = typeKeys(t, m, "[Pasted text #1 +2 lines]")
	m.pastes[1] = "real body"
	m = apply(t, m, enterKey)
	if len(m.queued) != 1 || m.queued[0] != "real body" {
		t.Fatalf("queued = %v, want the expanded paste body", m.queued)
	}
	if len(m.pastes) != 0 {
		t.Errorf("consumed pastes must be dropped, got %v", m.pastes)
	}
}
