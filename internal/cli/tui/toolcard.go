package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file is the base of the tool-call card family (US-006, SPEC 3.2,
// FR-6/7/8; registry split per T2.4, tui-crush-components.md §3.4). A toolCard
// is the data + lifecycle holder: created on toolStartMsg, completed on
// toolEndMsg, toggled between a capped and a full response view with Ctrl+O
// (see model.go). How a card is laid out is decided by a per-tool-family
// renderer registered in toolCardRenderers (tool_bash.go / tool_file.go /
// tool_edit.go); unknown tools fall back to genericToolRenderer
// (tool_generic.go). This file keeps the shared frame: header with status
// icon, the section builders, the narrow-width degradation and the render
// cache primitive. All width math goes through ui.Width / WrapToWidth /
// TruncateToWidth so CJK and emoji (two columns) never split.

// cardState is the lifecycle of a tool card: running while the tool executes,
// then success or warn once it finishes (warn covers a reported tool error).
type cardState int

const (
	cardRunning cardState = iota
	cardSuccess
	cardWarn
)

// respNode is one line of a tool's response, with depth giving the tree indent
// level (each level is rendered as two leading spaces).
type respNode struct {
	text  string
	depth int
}

// toolCard is a single tool invocation rendered as a bordered card. input holds
// the decoded call arguments (nil when the args were not a JSON object);
// response is the parsed result tree, populated on completion; diff holds a
// unified diff the tool reported in its result metadata (edit today), rendered
// as its own colored section instead of plain response text. expanded flips
// the response between a capped preview and the full tree. cacheKey/cacheOut
// hold the last render so steady-state frames (scroll, resize of unrelated
// regions) skip re-wrapping the card body.
type toolCard struct {
	id       string
	name     string
	input    map[string]any
	response []respNode
	diff     string
	state    cardState
	expanded bool

	hasCache bool
	cacheKey cardCacheKey
	cacheOut string
}

// collapsedResponseLines is how many response lines a card shows before it is
// expanded; past this the preview is truncated and a Ctrl+O hint is appended.
const collapsedResponseLines = 5

// collapsedDiffLines is the same cap for the Diff section. It is larger than
// the response cap because a hunk spends lines on file headers, the @@ marker
// and context around the actual change.
const collapsedDiffLines = 12

// narrowCardWidth is the content width below which a card degrades to a flat
// layout (spec §3.3 窄宽度回退): the rounded border costs two of the scarce
// columns and the Input arguments section crowds out the response payload, so
// below the threshold the border is dropped and arguments are omitted while
// the header and response stay.
const narrowCardWidth = 20

// cardCacheKey captures every input that can change a card's rendered output.
// The response and diff bodies are written exactly once (on toolEndMsg), so
// their line counts identify the content; state, expanded and width cover the
// remaining axes. input and name are fixed at creation.
type cardCacheKey struct {
	width    int
	state    cardState
	expanded bool
	nInput   int
	nResp    int
	nDiff    int
}

func (c *toolCard) cacheKeyNow(width int) cardCacheKey {
	return cardCacheKey{
		width:    width,
		state:    c.state,
		expanded: c.expanded,
		nInput:   len(c.input),
		nResp:    len(c.response),
		nDiff:    len(c.diff),
	}
}

// getCachedRender returns the cached render for key, if the card has one.
func (c *toolCard) getCachedRender(key cardCacheKey) (string, bool) {
	if c.hasCache && c.cacheKey == key {
		return c.cacheOut, true
	}
	return "", false
}

// setCachedRender stores a render result under key.
func (c *toolCard) setCachedRender(key cardCacheKey, out string) {
	c.hasCache = true
	c.cacheKey = key
	c.cacheOut = out
}

// clearCache drops the cached render (used when the expanded flag is toggled
// through the capability interface rather than a field write).
func (c *toolCard) clearCache() {
	c.hasCache = false
	c.cacheOut = ""
}

// expandable is the opt-in capability interface for transcript items whose
// detail view can be toggled (Ctrl+O). toolCard implements it; future block
// types adopt the same pattern instead of the key handler reaching into
// fields. Highlightable/Focusable follow the same opt-in convention when a
// consumer appears (tui-crush-components.md §3.4).
type expandable interface {
	toggleExpanded() bool
}

var _ expandable = (*toolCard)(nil)

// toggleExpanded flips the capped/full view and invalidates the render cache.
func (c *toolCard) toggleExpanded() bool {
	c.expanded = !c.expanded
	c.clearCache()
	return c.expanded
}

// toolCardRenderer lays out one family of tool-call cards. Registered by tool
// name in toolCardRenderers; each family decides its header argument and which
// sections the card body shows (T2.4, tui-crush-components.md §3.4).
type toolCardRenderer interface {
	// primaryArg returns the most salient call argument to inline in the card
	// header (e.g. Bash(cd /x && git add -A)), or "" when the call carried no
	// arguments.
	primaryArg(c *toolCard) string
	// sections assembles the card body below the header, in order. narrow
	// reports the flat (no-border) layout, where argument sections are omitted.
	sections(c *toolCard, theme Theme, inner int, narrow bool) []string
}

// toolCardRenderers maps a tool name to its family renderer. Unregistered
// names fall back to genericToolRenderer (see toolCardRendererFor).
var toolCardRenderers = map[string]toolCardRenderer{
	"bash":          bashToolRenderer{},
	"bash_output":   bashToolRenderer{},
	"kill_bash":     bashToolRenderer{},
	"read":          fileToolRenderer{},
	"write":         fileToolRenderer{},
	"find":          fileToolRenderer{},
	"ls":            fileToolRenderer{},
	"grep":          fileToolRenderer{},
	"memory_search": fileToolRenderer{},
	"edit":          editToolRenderer{},
}

// toolCardRendererFor resolves the renderer for a tool call name,
// case-insensitively, with the generic fallback for unknown tools.
func toolCardRendererFor(name string) toolCardRenderer {
	if r, ok := toolCardRenderers[strings.ToLower(name)]; ok {
		return r
	}
	return genericToolRenderer{}
}

// statusIcon returns the header status glyph for the card's state. Running is a
// spinner-like ellipsis, success a check, warn a bang.
func (c toolCard) statusIcon() string {
	switch c.state {
	case cardSuccess:
		return "✓"
	case cardWarn:
		return "!"
	default:
		return "…"
	}
}

// styledIcon renders the status glyph with the state's theme color: gray while
// running, green on success, yellow/red on warn.
func (c toolCard) styledIcon(theme Theme) string {
	icon := c.statusIcon()
	switch c.state {
	case cardSuccess:
		return theme.Success.Render(icon)
	case cardWarn:
		return theme.Warn.Render(icon)
	default:
		return theme.System.Render(icon)
	}
}

// render draws the card at the given content width through its family
// renderer, serving repeat calls (same width/state/expanded/body) from the
// cache. This is the transcript's per-frame entry point (transcript.go).
func (c *toolCard) render(theme Theme, width int) string {
	if width < 4 {
		width = 4
	}
	key := c.cacheKeyNow(width)
	if out, ok := c.getCachedRender(key); ok {
		return out
	}
	out := renderToolCard(toolCardRendererFor(c.name), theme, width, c)
	c.setCachedRender(key, out)
	return out
}

// renderToolCard draws the shared frame — header line, the family's sections,
// then either the flat layout or the rounded border — around one renderer's
// section list.
func renderToolCard(r toolCardRenderer, theme Theme, width int, c *toolCard) string {
	// Below the narrow threshold the card renders flat: no border (its two
	// columns are reclaimed) and the whole width is usable content.
	narrow := width < narrowCardWidth
	// The rounded border consumes one column on each side; wrap everything to the
	// inner width so nothing overflows the frame.
	inner := width - 2
	if narrow {
		inner = width
	}

	lines := []string{c.headerLine(r, theme, inner)}
	lines = append(lines, r.sections(c, theme, inner, narrow)...)

	if narrow {
		return strings.Join(lines, "\n")
	}

	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorGray)).
		Width(inner)
	return border.Render(strings.Join(lines, "\n"))
}

// headerLine builds the card header: status icon + tool name, with the
// family's primary argument inlined as Name(arg).
func (c *toolCard) headerLine(r toolCardRenderer, theme Theme, inner int) string {
	icon := c.styledIcon(theme)
	nameBudget := inner - ui.Width(icon) - 1
	if nameBudget < 1 {
		nameBudget = 1
	}
	header := c.name
	if arg := r.primaryArg(c); arg != "" {
		header = c.name + "(" + arg + ")"
	}
	header = TruncateToWidth(header, nameBudget)
	return icon + " " + theme.ToolHeader.Render(header)
}

// inputSection renders the "Input arguments" section: one wrapped key: value
// line per argument in sorted-key order. Empty when the call carried no
// arguments or the layout is narrow.
func (c toolCard) inputSection(theme Theme, inner int, narrow bool) []string {
	if len(c.input) == 0 || narrow {
		return nil
	}
	lines := []string{theme.ToolBody.Render("Input arguments")}
	for _, k := range sortedKeys(c.input) {
		kv := "  " + k + ": " + fmt.Sprintf("%v", c.input[k])
		lines = append(lines, theme.ToolBody.Render(WrapToWidth(kv, inner)))
	}
	return lines
}

// responseSection renders the parsed result tree under the given heading,
// capped to collapsedResponseLines with a Ctrl+O hint unless expanded.
func (c toolCard) responseSection(theme Theme, inner int, heading string) []string {
	if len(c.response) == 0 {
		return nil
	}
	lines := []string{theme.ToolBody.Render(heading)}
	resp := c.response
	truncated := false
	if !c.expanded && len(resp) > collapsedResponseLines {
		resp = resp[:collapsedResponseLines]
		truncated = true
	}
	for _, n := range resp {
		indent := strings.Repeat("  ", n.depth)
		lines = append(lines, theme.ToolBody.Render(WrapToWidth(indent+n.text, inner)))
	}
	if truncated {
		lines = append(lines, theme.System.Render("(Ctrl+O for more)"))
	}
	return lines
}

// diffSection renders the card's unified diff (edit) as a colored section,
// capped to collapsedDiffLines with a Ctrl+O hint unless expanded.
func (c toolCard) diffSection(theme Theme, inner int) []string {
	if c.diff == "" {
		return nil
	}
	lines := []string{theme.ToolBody.Render("Diff")}
	dl := strings.Split(strings.TrimRight(c.diff, "\n"), "\n")
	truncated := false
	if !c.expanded && len(dl) > collapsedDiffLines {
		dl = dl[:collapsedDiffLines]
		truncated = true
	}
	for _, ln := range dl {
		lines = append(lines, diffLineStyle(theme, ln).Render(WrapToWidth("  "+ln, inner)))
	}
	if truncated {
		lines = append(lines, theme.System.Render("(Ctrl+O for more)"))
	}
	return lines
}

// defaultPrimaryArg is the generic fallback: the first argument in sorted-key
// order. Returns "" when the call carried no arguments.
func (c toolCard) defaultPrimaryArg() string {
	if len(c.input) == 0 {
		return ""
	}
	keys := sortedKeys(c.input)
	return fmt.Sprintf("%v", c.input[keys[0]])
}

// pathArg is the file-family primary argument: the "path" key, falling back to
// the Claude-style "file_path" key callers sometimes use. Returns "" when
// neither is present.
func (c toolCard) pathArg() string {
	for _, key := range []string{"path", "file_path"} {
		if v, ok := c.input[key]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// sortedKeys returns the map keys in a stable (sorted) order so the input
// section renders deterministically instead of in Go's random map order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// diffLineStyle picks the theme style for one unified-diff line, mirroring
// ui.RenderDiffLine's compact-REPL coloring: dim file headers and context,
// cyan @@ hunk markers, red removals, green additions.
func diffLineStyle(theme Theme, line string) lipgloss.Style {
	switch {
	case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
		return theme.DiffCtx
	case strings.HasPrefix(line, "@@"):
		return theme.DiffHunk
	case strings.HasPrefix(line, "-"):
		return theme.DiffDel
	case strings.HasPrefix(line, "+"):
		return theme.DiffAdd
	default:
		return theme.DiffCtx
	}
}

// stripDiffTail removes the trailing unified diff from a tool result text so
// the card's Response section keeps only the summary line; the diff itself
// renders in the colored Diff section. It cuts at the diff's "--- a/" header
// rather than matching the exact suffix, so a result clipped mid-diff still
// splits cleanly. The cut is only applied when the tool reported a diff
// (card.diff != ""), so e.g. bash output of git diff is never mangled.
func stripDiffTail(text string) string {
	if i := strings.Index(text, "\n--- a/"); i >= 0 {
		return text[:i+1]
	}
	return text
}

// parseToolResult splits a tool's textual result into response tree nodes,
// inferring depth from leading whitespace (every two leading spaces is one
// level). Trailing empty lines are trimmed so the card does not render blank
// tail rows.
func parseToolResult(result string) []respNode {
	lines := strings.Split(result, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	nodes := make([]respNode, 0, len(lines))
	for _, ln := range lines {
		leading := len(ln) - len(strings.TrimLeft(ln, " "))
		nodes = append(nodes, respNode{text: ln[leading:], depth: leading / 2})
	}
	return nodes
}
