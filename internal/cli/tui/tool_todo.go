package tui

import "fmt"

// todoToolRenderer is the todo tool's family renderer: the collapsed row shows
// a progress digest ("2/4 done · first pending item") instead of the raw todos
// array — the generic %v formatting would leak Go map syntax onto the row.
type todoToolRenderer struct{}

var _ toolCardRenderer = todoToolRenderer{}

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
	// Card body stays the generic layout: full input list + response tree.
	return genericToolRenderer{}.sections(c, theme, inner, narrow)
}
