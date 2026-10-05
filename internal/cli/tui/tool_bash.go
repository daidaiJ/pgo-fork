package tui

import "fmt"

// bashToolRenderer renders the shell family (bash, bash_output, kill_bash):
// the command is the header argument and the Input arguments section is folded
// away — the output payload is what the user scans (crush bash.go). The
// response tree is relabeled "Output" to match. Shell results never carry a
// result-metadata diff (model.go strips only edit-style Details), so no Diff
// section is offered.
type bashToolRenderer struct{}

var _ toolCardRenderer = bashToolRenderer{}

func (bashToolRenderer) primaryArg(c *toolCard) string {
	if v, ok := c.input["command"]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func (bashToolRenderer) sections(c *toolCard, theme Theme, inner int, narrow bool) []string {
	return c.responseSection(theme, inner, "Output")
}
