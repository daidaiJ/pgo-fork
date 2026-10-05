package tui

// editToolRenderer renders edit cards: the path is the header argument and the
// Input arguments section is folded away — old_string/new_string are noise
// once the colored Diff section shows the actual change (crush edit.go keeps
// only the diff view).
type editToolRenderer struct{}

var _ toolCardRenderer = editToolRenderer{}

func (editToolRenderer) primaryArg(c *toolCard) string {
	return c.pathArg()
}

func (editToolRenderer) sections(c *toolCard, theme Theme, inner int, narrow bool) []string {
	var lines []string
	lines = append(lines, c.responseSection(theme, inner, "Response")...)
	lines = append(lines, c.diffSection(theme, inner)...)
	return lines
}
