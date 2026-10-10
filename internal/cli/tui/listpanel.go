package tui

import (
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/toolrules"
)

// listPanel is the interactive picker behind bare /skills and /mcp (T7.3
// user redesign: a submitted command opens an interactive panel — grok
// ExtensionsModal alignment — instead of the arg+text-echo shape, which
// stays as the parameter path for power users). Rows carry a source tag so
// their origin is readable; Enter (and, on the MCP panel, Space) toggles the
// highlighted row's enabled state through the session's surface deps; Esc
// closes. The panel is presentation-only: data and toggles come from the
// gather/toggle closures the key handler wires up.
//
// The MCP panel is two-level (T7.3 实测反馈: server 和 tool 级别都要能看、
// 能启停，grok /mcps 对齐): a server row expands in place into its tool rows
// (Enter 展开/收起), and toggles work at both levels (Space, or Enter on a
// tool row). The skills panel stays one-level with Enter toggling.
type listPanel struct {
	open     bool
	title    string
	hint     string
	rows     []listRow
	selected int
	filter   string
	note     string
}

// listRow is one panel line: a name, a description, and a bracketed tag
// ("[skill]" / "[disabled]" / state markers) that keeps the row's origin
// readable at a glance.
type listRow struct {
	title string
	desc  string
	tag   string
	// off is the row's current disabled state; toggling flips it.
	off bool
	// server/tool identify an MCP row: a server row has server set and tool
	// empty; a tool row carries both (server = owning server). Both empty on
	// skills rows, which have no tree.
	server string
	tool   string
	// children hold an MCP server row's tool rows; expanded shows them.
	children []listRow
	expanded bool
}

// maxListRows caps the visible row window (sessionsPanel's modal grammar).
const maxListRows = 12

func (p *listPanel) close() {
	p.open = false
	p.rows = nil
	p.selected = 0
	p.filter = ""
	p.note = ""
}

// setRows swaps the row set, re-applying the expanded-server marks so a
// toggle's regather does not collapse the tree the user opened.
func (p *listPanel) setRows(rows []listRow) {
	expanded := make(map[string]bool)
	for _, r := range p.rows {
		if r.server != "" && r.tool == "" && r.expanded {
			expanded[r.server] = true
		}
	}
	for i := range rows {
		if rows[i].server != "" && rows[i].tool == "" && expanded[rows[i].server] {
			rows[i].expanded = true
		}
	}
	p.rows = rows
}

// toggleExpand flips a server row's expanded state (the row is re-found by
// identity so a stale copy from the filtered view cannot miss).
func (p *listPanel) toggleExpand(r listRow) {
	for i := range p.rows {
		if p.rows[i].server == r.server && p.rows[i].tool == "" {
			p.rows[i].expanded = !p.rows[i].expanded
			return
		}
	}
}

// flatten walks the rows, splicing in an expanded server's tool rows so
// navigation/filtering/rendering see one sequence.
func flattenRows(rows []listRow) []listRow {
	var out []listRow
	for _, r := range rows {
		out = append(out, r)
		if r.expanded {
			out = append(out, r.children...)
		}
	}
	return out
}

// visible filters the flattened rows by substring over title/desc/tag,
// case-insensitive.
func (p listPanel) visible() []listRow {
	needle := strings.ToLower(p.filter)
	var out []listRow
	for _, r := range flattenRows(p.rows) {
		if needle == "" || strings.Contains(strings.ToLower(r.title+" "+r.desc+" "+r.tag), needle) {
			out = append(out, r)
		}
	}
	return out
}

// selectedRow resolves the highlighted row within the filtered set.
func (p listPanel) selectedRow() (listRow, bool) {
	vis := p.visible()
	if p.selected < 0 || p.selected >= len(vis) {
		return listRow{}, false
	}
	return vis[p.selected], true
}

func (p *listPanel) moveUp() {
	if n := len(p.visible()); n > 0 {
		p.selected = (p.selected - 1 + n) % n
	}
}

func (p *listPanel) moveDown() {
	if n := len(p.visible()); n > 0 {
		p.selected = (p.selected + 1) % n
	}
}

// view renders the modal in the sessionsPanel grammar: rounded panelBox,
// title row, filter echo when typing, a scrolled row window, note line. Each
// row is the grok shape (T7.3 实测反馈): 名称 + 一部分描述 — the description
// is clipped to the row's remaining width so a long blurb never wraps the
// panel (the full text stays on /skills info <name>); tool rows are indented
// under their server.
func (p listPanel) view(theme Theme, width, height int) string {
	inner := width - 4
	if inner > 96 {
		inner = 96
	}
	bodyW := inner - 2

	var b strings.Builder
	title := theme.KeyHint.Render(p.title)
	hint := p.hint
	if hint == "" {
		hint = "↑↓ 选择 · Enter 切换 · Esc 关闭"
	}
	hint = theme.System.Render(hint)
	gap := bodyW - uiW(title) - uiW(hint)
	if gap < 1 {
		gap = 1
	}
	b.WriteString(title + strings.Repeat(" ", gap) + hint + "\n")

	if p.filter != "" {
		b.WriteString(theme.KeyHint.Render("过滤: "+p.filter) + "\n")
	}

	vis := p.visible()
	if len(vis) == 0 {
		if p.note != "" {
			b.WriteString(theme.System.Render(p.note) + "\n")
		} else {
			b.WriteString(theme.System.Render("(无匹配行)") + "\n")
		}
		return panelBox(theme, strings.TrimRight(b.String(), "\n"), bodyW)
	}
	start := 0
	if len(vis) > maxListRows {
		start = p.selected - maxListRows/2
		if start < 0 {
			start = 0
		}
		if start > len(vis)-maxListRows {
			start = len(vis) - maxListRows
		}
	}
	end := min(start+maxListRows, len(vis))
	for i := start; i < end; i++ {
		r := vis[i]
		indent := "  "
		if r.server != "" && r.tool != "" {
			indent = "    "
		}
		line := indent + r.title
		if r.tag != "" {
			line += "  " + r.tag
		}
		if r.desc != "" {
			if d := clipDesc(r.desc, bodyW, uiW(line)); d != "" {
				line += "  " + d
			}
		}
		if i == p.selected {
			b.WriteString(theme.Accent.Render("› "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	if p.note != "" {
		b.WriteString(theme.System.Render(p.note) + "\n")
	}
	return panelBox(theme, strings.TrimRight(b.String(), "\n"), bodyW)
}

// clipDesc truncates a row description to the width left inside the panel
// after the row prefix (caret/indent), the title/tag columns already used,
// and the two separator columns, collapsing newlines first. grok's row
// shape: the blurb is clipped, the name and tag stay readable.
func clipDesc(desc string, bodyW, usedW int) string {
	budget := bodyW - 2 - usedW - 2
	if budget <= 0 {
		return ""
	}
	d := strings.ReplaceAll(desc, "\n", " ")
	return TruncateToWidth(d, budget)
}

// gatherSkillRows maps the surface's skill rows to panel rows. Enabled skills
// carry their invocation mode tag; config-disabled ones are dimmed-by-tag so
// the panel shows the whole population (the loaded view filters them out).
func gatherSkillRows(s *runSession) ([]listRow, string) {
	if s == nil {
		return nil, "(技能面板不可用：无活动会话)"
	}
	skillRows := s.surface.SkillRows()
	if len(skillRows) == 0 {
		return nil, "(没有已加载的技能：把 SKILL.md 放进 ~/.agents/skills/ 后 /skills reload)"
	}
	out := make([]listRow, 0, len(skillRows))
	for _, r := range skillRows {
		if r.Disabled {
			out = append(out, listRow{title: r.Name, desc: "文件保留在磁盘，Enter 重新启用", tag: "[disabled]", off: true})
			continue
		}
		out = append(out, listRow{title: r.Name, desc: r.Description, tag: "[" + r.Mode + "]", off: false})
	}
	return out, ""
}

// gatherMCPRows maps the manager's server status to the panel's two-level
// rows: each server row carries its advertised tools as children (T7.3 实测
// 反馈: server 和 tool 级别都可见可切，grok /mcps 对齐). Expansion state is
// applied by setRows at swap time.
func gatherMCPRows(s *runSession) ([]listRow, string) {
	if s == nil {
		return nil, "(MCP 面板不可用：无活动会话)"
	}
	serverRows := s.surface.MCPServerRows()
	if len(serverRows) == 0 {
		return nil, "(未配置 MCP 服务器：config.toml 的 [[mcp.servers]])"
	}
	out := make([]listRow, 0, len(serverRows))
	for _, r := range serverRows {
		desc := fmt.Sprintf("%d tools", r.Tools)
		if r.Hidden > 0 {
			desc += fmt.Sprintf(", %d hidden", r.Hidden)
		}
		if r.Info != "" {
			desc += " — " + r.Info
		}
		if r.Error != "" {
			desc += " · error: " + r.Error
		}
		tag := "[connected]"
		if !r.Enabled {
			tag = "[disabled]"
		} else if r.State != "connected" {
			tag = "[not connected]"
		}
		row := listRow{title: r.Name, desc: desc, tag: tag, off: !r.Enabled, server: r.Name}
		if tools, ok := s.surface.MCPToolRows(r.Name); ok {
			for _, t := range tools {
				child := listRow{title: t.Name, desc: t.Description, server: r.Name, tool: t.Name, off: t.Disabled}
				if t.Disabled {
					child.tag = "[disabled]"
				} else {
					child.tag = "[on]"
				}
				row.children = append(row.children, child)
			}
		}
		out = append(out, row)
	}
	return out, ""
}

// gatherShellRows maps the shell backend table to the panel's flat rows
// (T8.4): the live backend carries the [current] tag, every row's desc is
// its program + flag prefix, and the note states the switch semantics.
func gatherShellRows(s *runSession) ([]listRow, string) {
	if s == nil {
		return nil, "(Shell 面板不可用：无活动会话)"
	}
	if s.surface.Bash == nil {
		return nil, "(bash 工具不可用：--no-tools 或策略移除)"
	}
	current := s.surface.Bash.ShellKind()
	out := make([]listRow, 0, len(agenttool.ShellBackendNames()))
	for _, name := range agenttool.ShellBackendNames() {
		spec, err := agenttool.ShellSpecFor(name)
		if err != nil {
			continue
		}
		row := listRow{title: name, desc: fmt.Sprintf("%s %s", spec.Program, strings.Join(spec.Args, " "))}
		if name == current {
			row.tag = "[current]"
		}
		out = append(out, row)
	}
	return out, "切换写入 config.toml [shell] 并即时生效（下一条命令）；非 bash 后端跳过 shellguard 静态分析"
}

// gatherModeRows maps the approval postures to the panel's flat rows (T7.6):
// the live posture carries the [current] tag, every row's desc states what
// the posture does, and the note states the switch semantics (session-scoped,
// never persisted).
func gatherModeRows(s *runSession) ([]listRow, string) {
	if s == nil || s.approval == nil {
		return nil, "(mode 面板不可用：无活动会话)"
	}
	current := s.approval.Mode()
	out := make([]listRow, 0, 3)
	for _, m := range []toolrules.ApprovalMode{toolrules.ModeAsk, toolrules.ModePlan, toolrules.ModeAll} {
		row := listRow{title: m.String(), desc: prompts.ApprovalModeDesc(m)}
		if m == current {
			row.tag = "[current]"
		}
		out = append(out, row)
	}
	return out, "切换只作用于本会话（不落盘）；shift+tab 循环切换；plan 模式拦截全部 effect 调用"
}

// gatherLSPRows maps the LSP manager's status to the panel's two-level rows
// (T8.2, /mcp panel alignment): the configured server row carries its state,
// live diagnostics load and serverInfo; expanding it shows the deferred tool
// family with each row's face state — off rows are excluded from the global
// [lsp.gopls] tools allow-list (batch 2 per-tool switches).
func gatherLSPRows(s *runSession) ([]listRow, string) {
	if s == nil {
		return nil, "(LSP 面板不可用：无活动会话)"
	}
	if s.surface.LSP == nil {
		note := "(LSP 未启用：config.toml [lsp] enabled = true，或项目 ./.pigo/config.json 写 " + `{"lsp": {"enabled": true}}` + ")"
		if s.surface.LSPConfigured != nil && s.surface.LSPConfigured() {
			note += "\n(项目层已有 lsp 段：目录未信任时不生效)"
		}
		return nil, note
	}
	filter := map[string]bool{}
	if s.surface.LSPToolsList != nil {
		for _, t := range s.surface.LSPToolsList() {
			bare := strings.ToLower(strings.TrimSpace(t))
			filter[strings.TrimPrefix(bare, "lsp_")] = true
		}
	}
	out := make([]listRow, 0)
	for _, st := range s.surface.LSP.Status() {
		desc := "LSP 服务器"
		if st.ServerInfo != "" {
			desc = st.ServerInfo
		}
		if st.DiagFiles > 0 {
			desc += fmt.Sprintf(" — %d 诊断 / %d 文件", st.DiagTotal, st.DiagFiles)
		} else {
			desc += " — 无诊断"
		}
		if st.Error != "" {
			desc += " · error: " + st.Error
		}
		tag := "[" + st.State + "]"
		if !st.Enabled {
			tag = "[disabled]"
		}
		row := listRow{title: st.Name, desc: desc, tag: tag, off: !st.Enabled, server: st.Name}
		for _, name := range s.surface.LSPToolRows() {
			off := len(filter) > 0 && !filter[strings.TrimPrefix(strings.ToLower(name), "lsp_")]
			desc := "search_tools 认领后进声明面"
			if off {
				desc = "已移出工具面（config [lsp.gopls] tools；下会话生效）"
			}
			row.children = append(row.children, listRow{
				title: name, desc: desc, tag: "[deferred]", off: off,
				server: st.Name, tool: name,
			})
		}
		out = append(out, row)
	}
	return out, ""
}
