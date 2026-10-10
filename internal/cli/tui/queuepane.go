package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file is the TUI queue surface (T8.3, grok QueuePane alignment): the
// prompts entered while a run streams — plus the send-now entries that jump
// ahead of them — render as "#N {first line}" rows just above the running
// line, so the queue grok-style is visible and manageable instead of a
// one-shot transcript note. Minimal face per the 2026-10-11 ruling: visible +
// selectable + deletable; reordering is deferred.

// maxQueueRows caps how many rows the pane shows at once (grok QueuePane's
// MAX_QUEUE_HEIGHT); a longer queue scrolls its window under the selection.
const maxQueueRows = 3

// queuePane holds the pane's UI state: which row the selection sits on while
// the pane's keys are armed (empty composer + visible rows). The queue data
// itself lives on Model — sendNow + queued merged on demand in start order.
type queuePane struct {
	selecting bool
	selected  int
}

// queueRows merges the send-now and plain queues in start order: send-now
// entries start first (grok interjection), so they lead the numbering.
func (m Model) queueRows() []string {
	if len(m.sendNow) == 0 {
		return m.queued
	}
	rows := make([]string, 0, len(m.sendNow)+len(m.queued))
	rows = append(rows, m.sendNow...)
	return append(rows, m.queued...)
}

// normalize clamps the selection back into range after the queue shrank
// (drain / delete) and disarms it once no rows remain.
func (p *queuePane) normalize(n int) {
	if n == 0 {
		*p = queuePane{}
		return
	}
	if p.selected >= n {
		p.selected = n - 1
	}
	if p.selected < 0 {
		p.selected = 0
	}
}

// queueLineCount reports the rows the pane reserves in relayout — the same
// count queueView renders; an empty pane reserves nothing.
func (m Model) queueLineCount() int {
	n := len(m.queueRows())
	if n > maxQueueRows {
		n = maxQueueRows
	}
	return n
}

// queueView renders the pane: one "#N {first line}" row per queued prompt in
// start order, a gray " (+N lines)" suffix on multi-line entries, and a
// window of at most maxQueueRows rows (the selection scrolls it). The armed
// selection leads with a "❯ " cursor. Returns "" when nothing is queued, so
// an empty pane contributes zero rows.
func (m Model) queueView(theme Theme, width int) string {
	rows := m.queueRows()
	if len(rows) == 0 || width <= 0 {
		return ""
	}
	m.qpane.normalize(len(rows))
	start := 0
	if len(rows) > maxQueueRows {
		if start = m.qpane.selected - maxQueueRows + 1; start < 0 {
			start = 0
		}
	}
	end := start + maxQueueRows
	if end > len(rows) {
		end = len(rows)
	}
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		first, extra := queueRowText(rows[i])
		suffix := ""
		if extra > 0 {
			suffix = fmt.Sprintf(" (+%d lines)", extra)
		}
		prefix := fmt.Sprintf("#%d ", i+1)
		text := TruncateToWidth(first, width-ui.Width(prefix)-ui.Width(suffix))
		cursor := "  "
		if m.qpane.selecting && i == m.qpane.selected {
			cursor = theme.Accent.Render("❯ ")
		}
		lines = append(lines, cursor+theme.System.Render(prefix)+
			theme.User.Render(text)+theme.System.Render(suffix))
	}
	return strings.Join(lines, "\n")
}

// queueRowText reduces a queued prompt to its display row: the first
// non-empty trimmed line and the count of lines hidden behind the pane row.
func queueRowText(text string) (first string, extra int) {
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			first = t
			break
		}
	}
	if extra = len(lines) - 1; extra < 0 {
		extra = 0
	}
	return first, extra
}

// queueSelectUp / queueSelectDown arm and move the row selection. The pane
// owns ↑/↓ only while the composer is empty and rows are visible (the
// handleKey branch gates on both), mirroring the sub-agent panel's gesture:
// the first ↓ lands on the top row, the first ↑ on the last one.
func (m *Model) queueSelectUp() {
	n := len(m.queueRows())
	m.qpane.normalize(n)
	if n == 0 {
		return
	}
	if !m.qpane.selecting {
		m.qpane.selecting = true
		m.qpane.selected = n - 1
		return
	}
	if m.qpane.selected > 0 {
		m.qpane.selected--
	}
}

func (m *Model) queueSelectDown() {
	n := len(m.queueRows())
	m.qpane.normalize(n)
	if n == 0 {
		return
	}
	if !m.qpane.selecting {
		m.qpane.selecting = true
		m.qpane.selected = 0
		return
	}
	if m.qpane.selected < n-1 {
		m.qpane.selected++
	}
}

// queueDeleteSelected removes the selected row (send-now entries first), so
// the numbering shifts up. It returns the removed text for the transcript
// note; without an armed selection it is a no-op — Del must never destroy a
// row the user has not explicitly picked.
func (m *Model) queueDeleteSelected() (string, bool) {
	n := len(m.queueRows())
	m.qpane.normalize(n)
	if n == 0 || !m.qpane.selecting {
		return "", false
	}
	i := m.qpane.selected
	var removed string
	if i < len(m.sendNow) {
		removed = m.sendNow[i]
		m.sendNow = append(m.sendNow[:i], m.sendNow[i+1:]...)
	} else {
		j := i - len(m.sendNow)
		removed = m.queued[j]
		m.queued = append(m.queued[:j], m.queued[j+1:]...)
	}
	return removed, true
}

// popQueueFront removes and returns the next prompt to start: send-now
// entries first (they jump the queue), then plain queued ones.
func (m *Model) popQueueFront() (string, bool) {
	switch {
	case len(m.sendNow) > 0:
		next := m.sendNow[0]
		m.sendNow = m.sendNow[1:]
		return next, true
	case len(m.queued) > 0:
		next := m.queued[0]
		m.queued = m.queued[1:]
		return next, true
	}
	return "", false
}

// startQueued echoes and starts the given queued prompt: a "/name" line runs
// as a slash command, anything else is echoed as a user block and run.
func (m Model) startQueued(next string) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(next, "/") {
		return m.runSlash(next)
	}
	m.transcript.addUser(next)
	m.remoteEcho("\n> " + next + "\n")
	return m.startPrompt(next)
}

// startQueuedFront is the manual trigger for a held queue (T8.3): a bare
// Enter promotes the front prompt — send-now entries first — and the queue
// resumes its normal one-per-run-end draining from there.
func (m Model) startQueuedFront() (tea.Model, tea.Cmd) {
	next, ok := m.popQueueFront()
	if !ok {
		return m, nil
	}
	mod, cmd := m.startQueued(next)
	return mod, tea.Batch(cmd, fetchGitCmd(m.cwd))
}
