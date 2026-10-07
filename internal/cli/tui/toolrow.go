package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the collapsed single-line tool activity row (S5–S7,
// tui-render-semantics.md C1): a diamond marker + a family-colored verb + a
// one-line argument summary, grok-style — no border, no argument section, the
// card body only a Ctrl+O away. The diamond encodes the effect family (S7):
// "◇" marks read-only observation, "◆" marks calls that act (execute, write,
// dispatch); the closed thinking summary shares the same row grammar through
// its purple "◆ Thought" footer (transcript.go).

// toolVerbStyle is the display identity of one tool family: the human verb,
// whether the call is read-only (◇) or acting (◆), and the theme style that
// colors both diamond and verb.
type toolVerbStyle struct {
	verb     string
	readonly bool
	style    func(Theme) lipgloss.Style
}

// toolVerbs maps pigo's built-in tool names to their row identity. Unknown
// tools fall back to the title-cased tool name on the generic accent style,
// classified as acting (◆) — fail loud over silently claiming read-only.
var toolVerbs = map[string]toolVerbStyle{
	"read":          {verb: "Read", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"ls":            {verb: "List", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"find":          {verb: "Find", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"grep":          {verb: "Grep", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"memory_search": {verb: "Search", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"search_tools":  {verb: "Search", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"webfetch":      {verb: "Fetch", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"websearch":     {verb: "Search", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},
	"todo":          {verb: "Todo", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRead }},

	"bash":         {verb: "Run", style: func(t Theme) lipgloss.Style { return t.ToolVerbRun }},
	"bash_output":  {verb: "Run", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbRun }},
	"kill_bash":    {verb: "Kill", style: func(t Theme) lipgloss.Style { return t.ToolVerbRun }},
	"write":        {verb: "Write", style: func(t Theme) lipgloss.Style { return t.ToolVerbEdit }},
	"edit":         {verb: "Edit", style: func(t Theme) lipgloss.Style { return t.ToolVerbEdit }},
	"context_edit": {verb: "Edit", style: func(t Theme) lipgloss.Style { return t.ToolVerbEdit }},

	"task":          {verb: "Task", style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
	"ask_user":      {verb: "Ask", style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
	"schedule_create": {verb: "Schedule", style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
	"schedule_delete": {verb: "Unschedule", style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
	"schedule_list":   {verb: "Schedules", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
	"goal_blocked":    {verb: "Goal", readonly: true, style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
	"goal_complete":   {verb: "Goal", style: func(t Theme) lipgloss.Style { return t.ToolVerbTask }},
}

// toolVerb resolves a tool call name (case-insensitive) to its row identity.
func toolVerb(name string) toolVerbStyle {
	if v, ok := toolVerbs[strings.ToLower(name)]; ok {
		return v
	}
	return toolVerbStyle{
		verb: titleVerb(name),
		style: func(t Theme) lipgloss.Style { return t.ToolVerbGeneric },
	}
}

// titleVerb converts an arbitrary tool name to a display verb: lower-cased,
// underscores spaced, first rune upper-cased ("memory_put" → "Memory put").
func titleVerb(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "_", " ")
	if name == "" {
		return "Tool"
	}
	r := []rune(name)
	upper := strings.ToUpper(string(r[0]))
	return upper + string(r[1:])
}

// renderToolRow draws the collapsed activity line for one tool call:
// "◇ Read 1 file" / "◆ Run 核对 git 现场：…". While running the summary gains a
// trailing dim ellipsis; a failed call appends a warn marker. Styling happens
// segment-wise on plain text (width math must never see ANSI), and everything
// truncates to the content width; dim (history) rows drop family colors for
// the chrome gray (S6).
func renderToolRow(c *toolCard, theme Theme, width int, dim bool) string {
	fam := toolVerb(c.name)
	diamond := "◆"
	if fam.readonly {
		diamond = "◇"
	}
	headStyle := fam.style(theme)
	if dim {
		headStyle = theme.Chrome
	}
	head := diamond + " " + fam.verb
	used := ui.Width(head)

	arg := toolRowArg(c)
	suffix := ""
	if c.state == cardRunning {
		suffix = "…"
	}
	if c.state == cardWarn {
		suffix += " ✗"
	}
	suffixW := ui.Width(suffix)

	if arg != "" && used+1+suffixW < width {
		arg = TruncateToWidth(arg, width-used-1-suffixW)
		used += 1 + ui.Width(arg)
	} else {
		arg = ""
	}
	out := headStyle.Render(head)
	if arg != "" {
		out += " " + theme.User.Render(arg)
	}
	if suffix != "" {
		out += theme.Chrome.Render(suffix)
	}
	return out
}

// toolRowArg extracts the single-line argument summary: the family renderer's
// primary argument, flattened to one line. The card's input map is the decoded
// JSON object (nil when the args were not an object); the generic path falls
// back to a compact key list so unknown tools still summarize.
func toolRowArg(c *toolCard) string {
	arg := toolCardRendererFor(c.name).primaryArg(c)
	arg = strings.ReplaceAll(arg, "\r\n", " ")
	arg = strings.ReplaceAll(arg, "\n", " ")
	return strings.TrimSpace(arg)
}
