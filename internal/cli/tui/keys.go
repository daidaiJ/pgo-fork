package tui

import (
	"strings"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the keys line (S13, tui-render-semantics.md C4): the
// bottom-most row of dynamic key hints — key names bright, labels dim, dim
// " | " separators (grok keys line). Content follows the shell mode: idle
// editing, autocomplete open, sub-agent panel driving, or a run in flight.

// keyBind is one advertised key: the key name and its label.
type keyBind struct{ key, label string }

// renderKeysLine draws binds right out of the mode list the model selects, in
// the grok hint grammar (views/welcome mod.rs): key bright bold, two spaces,
// dim label, then four spaces before the next pair — no pipe separators.
func renderKeysLine(theme Theme, width int, binds []keyBind) string {
	if width <= 0 || len(binds) == 0 {
		return ""
	}
	gap := "    "
	var b strings.Builder
	w := 0
	for i, kb := range binds {
		if i > 0 {
			if w+ui.Width(gap) > width {
				break
			}
			b.WriteString(gap)
			w += ui.Width(gap)
		}
		item := theme.KeyHint.Render(kb.key) + theme.Chrome.Render("  "+kb.label)
		iw := ui.Width(kb.key+"  "+kb.label)
		if w+iw > width {
			break
		}
		b.WriteString(item)
		w += iw
	}
	return b.String()
}
