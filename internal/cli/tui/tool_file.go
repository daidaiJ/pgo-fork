package tui

// fileToolRenderer renders the file-scanning family (read, write, find, ls,
// grep, memory_search): the path is the header argument and the card keeps the
// full layout — the argument list still carries the keys that shape the scan
// (limit, offset, pattern), so only the primary-argument choice differs from
// the generic fallback.
type fileToolRenderer struct{}

var _ toolCardRenderer = fileToolRenderer{}

func (fileToolRenderer) primaryArg(c *toolCard) string {
	return c.pathArg()
}

func (fileToolRenderer) sections(c *toolCard, theme Theme, inner int, narrow bool) []string {
	var lines []string
	lines = append(lines, c.inputSection(theme, inner, narrow)...)
	lines = append(lines, c.responseSection(theme, inner, "Response")...)
	lines = append(lines, c.diffSection(theme, inner)...)
	return lines
}
