package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestTodoCardSectionsGrokStyle covers the 2026-10-07 task-list pass: the
// expanded todo card renders a grok-style list (▸ active accent, ✓ done green
// with dimmed text, ◇ pending) plus a "Tasks · n/m done" heading, and the old
// generic body (Go-map-leaking input dump + duplicate checkbox text) is gone.
func TestTodoCardSectionsGrokStyle(t *testing.T) {
	c := &toolCard{name: "todo", input: map[string]any{
		"todos": []any{
			map[string]any{"content": "调研参照", "status": "completed"},
			map[string]any{"content": "实作鼠标交互", "status": "in_progress"},
			map[string]any{"content": "回归测试", "status": "pending"},
		},
	}}
	out := stripANSI(renderToolCard(todoToolRenderer{}, DefaultTheme(), 60, c))

	if !strings.Contains(out, "Tasks · 1/3 done") {
		t.Errorf("missing progress heading: %q", out)
	}
	for _, want := range []string{"✓ 调研参照", "▸ 实作鼠标交互", "◇ 回归测试"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing task row %q in: %q", want, out)
		}
	}
	if strings.Contains(out, "map[") {
		t.Errorf("Go map syntax leaked onto the card: %q", out)
	}
	if strings.Contains(out, "Input arguments") {
		t.Errorf("generic input section should be replaced by the task list: %q", out)
	}
}

// TestTodoCardEmptyFallsBackToGeneric keeps an undecodable/empty list on the
// generic layout (the response still carries the "(no tasks)" checkbox text).
func TestTodoCardEmptyFallsBackToGeneric(t *testing.T) {
	c := &toolCard{
		name:     "todo",
		input:    map[string]any{},
		response: parseToolResult("Todos: (no tasks)"),
	}
	out := stripANSI(renderToolCard(todoToolRenderer{}, DefaultTheme(), 60, c))
	if !strings.Contains(out, "Todos: (no tasks)") {
		t.Errorf("expected the generic response body, got: %q", out)
	}
}

// TestMouseClickTogglesFoldableBlocks drives the click → block hit map →
// toggle path through the model: clicking a collapsed tool row expands the
// card, clicking its title line again expands the response tree, a third
// click folds it back; a click on a non-foldable block's row starts a text
// selection instead.
func TestMouseClickTogglesFoldableBlocks(t *testing.T) {
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}), tea.WindowSizeMsg{Width: 60, Height: 20})
	m = apply(t, m, toolStartMsg{id: "t1", name: "bash", input: map[string]any{"command": "go build ./..."}})
	m = apply(t, m, toolEndMsg{id: "t1", ok: true, result: "build ok"})

	idx := -1
	for i := range m.transcript.blocks {
		if m.transcript.blocks[i].role == roleTool {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no tool block after toolStart/toolEnd")
	}
	headerY := func(tr transcript) int {
		for _, h := range tr.hits {
			if h.block == idx {
				return h.start - tr.vp.YOffset() + transcriptOriginRow
			}
		}
		t.Fatal("tool block missing from hit map")
		return 0
	}

	// Click 1: collapsed diamond row → expanded card.
	m = apply(t, m, tea.MouseClickMsg{X: 5, Y: headerY(m.transcript), Button: tea.MouseLeft})
	if got := m.transcript.blocks[idx].display; got != displayFull {
		t.Fatalf("click 1: display = %v, want displayFull", got)
	}
	// Click 2: title line → full response tree.
	m = apply(t, m, tea.MouseClickMsg{X: 5, Y: headerY(m.transcript), Button: tea.MouseLeft})
	if !m.transcript.blocks[idx].card.expanded {
		t.Fatal("click 2: response tree should be expanded")
	}
	// Click 3: folded back to the single diamond row.
	m = apply(t, m, tea.MouseClickMsg{X: 5, Y: headerY(m.transcript), Button: tea.MouseLeft})
	blk := m.transcript.blocks[idx]
	if blk.display != displayCollapsed || blk.card.expanded {
		t.Fatalf("click 3: display = %v, expanded = %v, want collapsed", blk.display, blk.card.expanded)
	}

	// A click on a non-foldable block's row must not fold: it starts a text
	// selection instead.
	m = apply(t, m, textDeltaMsg{delta: "hello selectable world"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("hello selectable world")},
	}})
	aidx := -1
	for i := range m.transcript.blocks {
		if m.transcript.blocks[i].role == roleAssistant {
			aidx = i
		}
	}
	if aidx < 0 {
		t.Fatal("no assistant block")
	}
	y := -1
	for _, h := range m.transcript.hits {
		if h.block == aidx {
			y = h.start - m.transcript.vp.YOffset() + transcriptOriginRow
		}
	}
	if y < 0 {
		t.Fatal("assistant block missing from hit map")
	}
	m = apply(t, m, tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if !m.sel.active {
		t.Error("click on a non-foldable block should start a text selection")
	}
}
