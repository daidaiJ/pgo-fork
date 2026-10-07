package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// todoToolRenderer is the todo tool's family renderer. The collapsed row keeps
// the T6.10 digest ("2/4 done · first pending item") instead of the raw todos
// array — the generic %v formatting would leak Go map syntax onto the row. The
// expanded body renders a grok-style task list (2026-10-07 interaction batch):
// one line per task with a status glyph — ▸ active (accent bold), ✓ done
// (green, text dimmed back) — and ◇ pending, so plan progress reads at a
// glance without opening anything.
type todoToolRenderer struct{}

var _ toolCardRenderer = todoToolRenderer{}

// todoItem is one decoded task from the call's input array (map[string]any
// entries as produced by JSON decoding).
type todoItem struct {
	content string
	status  string
}

// todoItemsOf decodes the todos argument; malformed entries (not an object,
// empty content) are skipped rather than rendered as broken rows.
func todoItemsOf(c *toolCard) []todoItem {
	raw, ok := c.input["todos"].([]any)
	if !ok {
		return nil
	}
	items := make([]todoItem, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		content, _ := m["content"].(string)
		status, _ := m["status"].(string)
		if strings.TrimSpace(content) == "" {
			continue
		}
		items = append(items, todoItem{content: content, status: status})
	}
	return items
}

func (todoToolRenderer) primaryArg(c *toolCard) string {
	raw, ok := c.input["todos"].([]any)
	if !ok || len(raw) == 0 {
		return c.defaultPrimaryArg()
	}
	done, first := 0, ""
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if status, _ := m["status"].(string); status == "completed" {
			done++
		}
		if first == "" {
			first, _ = m["content"].(string)
		}
	}
	if first == "" {
		return fmt.Sprintf("%d/%d done", done, len(raw))
	}
	return fmt.Sprintf("%d/%d done · %s", done, len(raw), first)
}

func (todoToolRenderer) sections(c *toolCard, theme Theme, inner int, narrow bool) []string {
	items := todoItemsOf(c)
	if len(items) == 0 {
		// No decodable list: keep the generic input/response layout — the
		// response still carries the checkbox rendering for that case.
		return genericToolRenderer{}.sections(c, theme, inner, narrow)
	}
	done := 0
	for _, it := range items {
		if it.status == "completed" {
			done++
		}
	}
	lines := []string{theme.ToolBody.Render(fmt.Sprintf("Tasks · %d/%d done", done, len(items)))}

	// Each row: glyph + space + task text, wrapped with continuation lines
	// indented under the text column (2 display columns: glyph + space).
	textWidth := inner - 2
	if textWidth < 1 {
		textWidth = 1
	}
	for _, it := range items {
		var mark string
		var markStyle, textStyle lipgloss.Style
		switch it.status {
		case "completed":
			mark, markStyle, textStyle = "✓", theme.Success, theme.Chrome
		case "in_progress":
			mark, markStyle, textStyle = "▸", theme.ToolVerbGeneric, theme.User
		default: // pending; unknown statuses degrade to it
			mark, markStyle, textStyle = "◇", theme.Chrome, theme.User
		}
		wrapped := WrapToWidth(it.content, textWidth)
		ls := strings.Split(wrapped, "\n")
		lines = append(lines, markStyle.Render(mark)+" "+textStyle.Render(ls[0]))
		for _, cont := range ls[1:] {
			lines = append(lines, theme.Chrome.Render("  "+cont))
		}
	}
	return lines
}
