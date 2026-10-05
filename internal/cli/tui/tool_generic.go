package tui

// genericToolRenderer is the fallback for tools without a registered family
// (toolCardRendererFor): it shows every section the card carries — the full
// Input arguments list, the Response tree and, when the tool reported one, the
// colored Diff section. This is the pre-T2.4 card layout, kept verbatim.
type genericToolRenderer struct{}

var _ toolCardRenderer = genericToolRenderer{}

func (genericToolRenderer) primaryArg(c *toolCard) string {
	return c.defaultPrimaryArg()
}

func (genericToolRenderer) sections(c *toolCard, theme Theme, inner int, narrow bool) []string {
	var lines []string
	lines = append(lines, c.inputSection(theme, inner, narrow)...)
	lines = append(lines, c.responseSection(theme, inner, "Response")...)
	lines = append(lines, c.diffSection(theme, inner)...)
	return lines
}
