package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the context usage panel (grok context-structure
// reference, 2026-10-07): an overlay rounded panel over the transcript area
// showing the context budget — used / window with a diamond grid, a breakdown
// of where the tokens sit, and the declaration-face costs counted above the
// line — plus a second tab with the session info report. It is the first
// consumer of the panel semantic; the grok Dashboard's remaining tabs
// (hooks / plugins / skills / workflows / MCP servers) anchor future work on
// the same frame (see tui-render-semantics.md §9).

// contextPanel is the overlay's state: open/closed, active tab, and the scroll
// offset for body content taller than the panel.
type contextPanel struct {
	open   bool
	tab    int
	scroll int
}

// The panel's tabs, in grok's usage-modal order (ContextUsage, UsageLimit,
// SessionInfo): /context opens at tabContext, /usage at tabUsage.
const (
	tabContext = iota
	tabUsage
	tabSession
)

// contextTabs are the tab labels; the active one renders bright, the rest dim.
var contextTabs = []string{"上下文用量", "用量上限", "会话信息"}

// panelBodyRows is the content height budget the panel aims for; the body
// scrolls past it. The panel otherwise sizes to its content.
const panelBodyRows = 20

// toggle opens/closes the panel (resetting the view so a reopen starts clean).
func (p *contextPanel) toggle() {
	p.open = !p.open
	p.tab = tabContext
	p.scroll = 0
}

// openAt opens the panel on a specific tab (/usage lands on the plan-quota
// tab, /context on the context tab — grok's per-command entry points).
func (p *contextPanel) openAt(tab int) {
	p.open = true
	if tab < 0 || tab >= len(contextTabs) {
		tab = tabContext
	}
	p.tab = tab
	p.scroll = 0
}

// close shuts the panel.
func (p *contextPanel) close() { p.open = false }

// handleKey consumes the panel's keys when it is open (the panel is modal):
// tab switches, up/down scroll, esc/q/x close, everything else is swallowed.
// "c" (copy session id) is left to the model, which owns the clipboard Cmd.
// It reports whether the key was consumed.
func (p *contextPanel) handleKey(key string) bool {
	if !p.open {
		return false
	}
	switch key {
	case "tab":
		p.tab = (p.tab + 1) % len(contextTabs)
		p.scroll = 0
	case "shift+tab":
		p.tab = (p.tab - 1 + len(contextTabs)) % len(contextTabs)
		p.scroll = 0
	case "up":
		if p.scroll > 0 {
			p.scroll--
		}
	case "down":
		p.scroll++
	case "esc", "q", "x":
		p.close()
	}
	return true
}

// contextData is the snapshot the panel renders, gathered by the model from
// the live session (no I/O here, so the panel stays a pure renderer).
type contextData struct {
	modelName string
	tokens    int
	window    int

	sysPromptTokens int
	toolCount       int
	toolTokens      int
	skillCount      int
	skillTokens     int
	sessionID       string
	sessionReport   string // tab 3 body (renderSession output)
	// usageReport is tab 2's body (the /usage report: this session's tallies
	// plus the provider plan quota), pre-rendered by the model; usageWaiting
	// marks a quota lookup still in flight.
	usageReport  string
	usageWaiting bool
}

// render draws the overlay to width×height: a rounded bordered box, horizontally
// centered, vertically anchored to the top of the transcript region. The box
// keeps a two-column margin from each terminal edge so lipgloss's width math
// never meets the terminal's own wrap boundary.
func (p contextPanel) render(theme Theme, d contextData, width, height int) string {
	inner := width - 4
	if inner > 96 {
		inner = 96
	}
	bodyW := inner - 2
	body := p.body(theme, d, bodyW)
	box := panelBox(theme, body, bodyW)
	lines := strings.Split(box, "\n")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	pad := (width - inner) / 2
	if pad > 0 {
		for i, l := range lines {
			lines[i] = strings.Repeat(" ", pad) + l
		}
	}
	return strings.Join(lines, "\n")
}

// body assembles the active tab's content: title row, main body, footer keys.
func (p contextPanel) body(theme Theme, d contextData, width int) string {
	var b strings.Builder

	// Title row: tabs + [X] close affordance, grok panel header.
	var tabs strings.Builder
	for i, t := range contextTabs {
		if i > 0 {
			tabs.WriteString("  ")
		}
		if i == p.tab {
			tabs.WriteString(theme.KeyHint.Render(t))
		} else {
			tabs.WriteString(theme.Chrome.Render(t))
		}
	}
	title := tabs.String()
	close := theme.Chrome.Render("[X]")
	gap := width - uiW(title) - uiW("[X]")
	if gap < 1 {
		gap = 1
	}
	b.WriteString(title + strings.Repeat(" ", gap) + close + "\n")
	b.WriteString(theme.Chrome.Render(strings.Repeat("─", maxInt(width, 1))) + "\n\n")

	switch p.tab {
	case tabUsage:
		b.WriteString(scrollLines(p.usageBody(theme, d, width), p.scroll))
	case tabSession:
		b.WriteString(scrollLines(p.sessionBody(theme, d, width), p.scroll))
	default:
		b.WriteString(scrollLines(p.contextBody(theme, d, width), p.scroll))
	}

	// Footer keys (grok panel footer).
	b.WriteString("\n" + renderKeysLine(theme, width, []keyBind{
		{"Tab", "切换"},
		{"↑/↓", "滚动"},
		{"c", "复制会话 ID"},
		{"Esc", "关闭"},
	}))
	return b.String()
}

// scrollLines drops the first n lines of a rendered body (the panel's
// ↑/↓ scrolling); n is clamped so at least one line remains visible.
func scrollLines(body string, n int) string {
	lines := strings.Split(body, "\n")
	if n >= len(lines) {
		n = len(lines) - 1
	}
	if n <= 0 {
		return body
	}
	return strings.Join(lines[n:], "\n")
}

// contextBody renders tab 1: the budget line, diamond grid, and breakdown rows.
func (p contextPanel) contextBody(theme Theme, d contextData, width int) string {
	var b strings.Builder
	b.WriteString(theme.User.Render("Context") + "\n")
	if d.window > 0 {
		pct := 100 * float64(d.tokens) / float64(d.window)
		pctStr := fmt.Sprintf("%.0f%%", pct)
		if pct < 10 {
			pctStr = fmt.Sprintf("%.2f%%", pct)
		}
		b.WriteString(theme.User.Render(fmt.Sprintf("%s / %s tokens (%s)",
			humanTokens(d.tokens), humanTokens(d.window), pctStr)) + "\n")
	}
	if d.modelName != "" {
		b.WriteString(theme.Chrome.Render(d.modelName) + "\n")
	}
	b.WriteString("\n" + diamondGrid(theme, d.tokens, d.window) + "\n\n")

	// Breakdown: what is inside the used budget (filled diamonds) plus the
	// free headroom (hollow). Estimates carry the same ≈4 chars/token heuristic
	// as the spinner's token readout.
	type row struct {
		dim     bool
		label   string
		tokens  int
		hideVal bool
	}
	rows := []row{
		{false, "System prompt", d.sysPromptTokens, d.sysPromptTokens <= 0},
		{false, "Messages", maxInt(d.tokens-d.sysPromptTokens, 0), d.tokens <= 0},
		{true, "Free", maxInt(d.window-d.tokens, 0), d.window <= 0},
	}
	for _, r := range rows {
		b.WriteString(breakdownRow(theme, r.dim, r.label, r.tokens, d.window, r.hideVal, ""))
	}

	if d.toolCount > 0 || d.skillCount > 0 {
		b.WriteString("\n" + theme.Chrome.Render("Already counted above") + "\n")
		if d.toolCount > 0 {
			b.WriteString(breakdownRow(theme, true, "Tool definitions", d.toolTokens, d.window, false,
				fmt.Sprintf(" · %d tools", d.toolCount)))
		}
		if d.skillCount > 0 {
			b.WriteString(breakdownRow(theme, true, "Skills", d.skillTokens, d.window, false,
				fmt.Sprintf(" · %d skills", d.skillCount)))
		}
	}
	return b.String()
}

// usageBody renders tab 2 (grok's "Usage limit"): the /usage report — the
// provider's plan windows first, then this session's tallies — with a waiting
// line while the quota lookup is still in flight.
func (p contextPanel) usageBody(theme Theme, d contextData, width int) string {
	var b strings.Builder
	if d.usageReport != "" {
		b.WriteString(theme.Chrome.Render(d.usageReport))
	}
	if d.usageWaiting {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(theme.KeyHint.Render("套餐余量：查询中…"))
	}
	if b.Len() == 0 {
		return theme.Chrome.Render("(用量不可用：无活动会话)")
	}
	return b.String()
}

// sessionBody renders tab 3: the shared session report (same content /session
// prints), pre-rendered plain and passed in as sessionReport.
func (p contextPanel) sessionBody(theme Theme, d contextData, width int) string {
	if d.sessionReport == "" {
		return theme.Chrome.Render("(会话信息不可用：无活动会话)")
	}
	return theme.Chrome.Render(d.sessionReport)
}

// diamondGrid draws the token-budget grid: five rows of sixteen cells, filled
// proportionally to usage (grok's context panel).
func diamondGrid(theme Theme, tokens, window int) string {
	const cols, rows = 16, 5
	filled := 0
	if window > 0 {
		filled = tokens * cols * rows / window
		if filled > cols*rows {
			filled = cols * rows
		}
	}
	var b strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteByte('\n')
		}
		for c := 0; c < cols; c++ {
			if c > 0 {
				b.WriteByte(' ')
			}
			if r*cols+c < filled {
				b.WriteString(theme.Chrome.Render("◆"))
			} else {
				b.WriteString(theme.Chrome.Render("◇"))
			}
		}
	}
	return b.String()
}

// breakdownRow renders one "◆ Label  N tokens (P%)" line with aligned value
// columns and an optional dim tail ("· 27 tools").
func breakdownRow(theme Theme, dim bool, label string, tokens, window int, hideVal bool, tail string) string {
	diamond := theme.Chrome.Render("◆")
	if dim {
		diamond = theme.Chrome.Render("◇")
	}
	labelStyle := theme.User
	valStyle := theme.Chrome
	if dim {
		labelStyle = theme.Chrome
	}
	labelCol := fmt.Sprintf("%-18s", label)
	if uiW(label) > 18 {
		labelCol = label + " "
	}
	val := ""
	if !hideVal {
		pct := ""
		if window > 0 {
			p := 100 * float64(tokens) / float64(window)
			if p < 10 {
				pct = fmt.Sprintf("(%.1f%%)", p)
			} else {
				pct = fmt.Sprintf("(%.0f%%)", p)
			}
		}
		val = fmt.Sprintf("%8s tokens  %s", humanTokens(tokens), pct)
	}
	line := diamond + " " + labelStyle.Render(labelCol) + valStyle.Render(val)
	if tail != "" {
		line += theme.Chrome.Render(tail)
	}
	return line + "\n"
}

// panelBox wraps a panel body in the shared rounded-border frame (lipgloss v2's
// Width spans the border inclusive, crush 排版规则). It is the frame both
// overlay panels render through so /context and /sessions read as one family.
func panelBox(theme Theme, body string, bodyW int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorGray)).
		Width(bodyW + 2).
		Render(body)
}

// uiW measures display width via the ui helper (local alias keeps the call
// sites short in this file).
func uiW(s string) int { return ui.Width(s) }

// maxInt is the int max (named distinctly from the builtin to avoid shadowing
// package-wide; this file needs it only on ints).
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
