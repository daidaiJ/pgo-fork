// This file implements the /sessions picker overlay (T7.3 S8, grok
// session-picker alignment): a modal rounded panel listing the persisted
// sessions in the shared store — title (first user prompt), relative update
// time, model, project directory and message count — with type-to-filter,
// Enter to resume the highlighted session into the TUI, and a grok-style armed
// delete (d then y confirms, anything else disarms). The frame, key grammar and
// scroll conventions follow the /context panel (contextpanel.go), the first
// consumer of the panel semantic.
//
// Deviations from the S8 定稿 (tui-slash-ux.md §4): no repo grouping and no
// content search (the filter matches title/id/model/cwd only); the title is the
// first user prompt rather than an LLM-generated summary (auto-title is a
// T7.3 S2 follow-up). Registered in the spec's deviation log on landing.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// maxSessionsRows caps how many entry rows the picker shows at once; a longer
// list scrolls a window around the selection.
const maxSessionsRows = 12

// sessionsPanel is the picker's state. It is a pure view: the entry list is
// gathered by the model on open (the store read must not live in a renderer).
type sessionsPanel struct {
	open     bool
	entries  []sessionEntry
	filter   string
	selected int
	// armed holds the id of the entry pending delete confirmation (d/y grammar).
	armed string
	// note is a transient status line (store error, delete result) rendered
	// under the rows.
	note string
}

// sessionEntry is one picker row.
type sessionEntry struct {
	id      string
	title   string
	model   string
	cwd     string
	updated time.Time
	count   int
	current bool
}

// visible filters the entries by the current filter (case-insensitive substring
// over title/id/model/cwd) and returns the filtered slice.
func (p sessionsPanel) visible() []sessionEntry {
	needle := strings.ToLower(p.filter)
	if needle == "" {
		return p.entries
	}
	var out []sessionEntry
	for _, e := range p.entries {
		if strings.Contains(strings.ToLower(e.title+" "+e.id+" "+e.model+" "+e.cwd), needle) {
			out = append(out, e)
		}
	}
	return out
}

// close shuts the picker and drops its state so a reopen starts clean.
func (p *sessionsPanel) close() {
	p.open = false
	p.entries = nil
	p.filter = ""
	p.selected = 0
	p.armed = ""
	p.note = ""
}

// moveUp / moveDown cycle the selection over the visible rows with wrap-around.
func (p *sessionsPanel) moveUp() {
	n := len(p.visible())
	if n == 0 {
		return
	}
	p.selected--
	if p.selected < 0 {
		p.selected = n - 1
	}
}

func (p *sessionsPanel) moveDown() {
	n := len(p.visible())
	if n == 0 {
		return
	}
	p.selected++
	if p.selected >= n {
		p.selected = 0
	}
}

// selectedEntry returns the highlighted visible entry, or ok=false when the
// filtered list is empty.
func (p sessionsPanel) selectedEntry() (sessionEntry, bool) {
	vis := p.visible()
	if p.selected < 0 || p.selected >= len(vis) {
		return sessionEntry{}, false
	}
	return vis[p.selected], true
}

// rows reports the picker's rendered height for relayout's row accounting (the
// panel replaces the transcript region, so it currently reserves no extra rows).
func (p sessionsPanel) rows() int { return 0 }

// view renders the picker panel to width×height: rounded box, title row, the
// entry window, the armed/note line and the footer keys.
func (p sessionsPanel) view(theme Theme, width, height int) string {
	inner := width - 4
	if inner > 96 {
		inner = 96
	}
	bodyW := inner - 2
	var b strings.Builder

	title := theme.KeyHint.Render("会话列表")
	close := theme.Chrome.Render("[X]")
	gap := bodyW - uiW("会话列表") - uiW("[X]")
	if gap < 1 {
		gap = 1
	}
	b.WriteString(title + strings.Repeat(" ", gap) + close + "\n")
	b.WriteString(theme.Chrome.Render(strings.Repeat("─", maxInt(bodyW, 1))) + "\n")

	if p.filter != "" {
		b.WriteString(theme.Chrome.Render("filter: " + p.filter) + "\n")
	}

	vis := p.visible()
	if len(vis) == 0 {
		if p.note != "" {
			b.WriteString("\n" + theme.Chrome.Render(p.note) + "\n")
		} else {
			b.WriteString("\n" + theme.Chrome.Render("(没有匹配的会话)") + "\n")
		}
	} else {
		start := 0
		if len(vis) > maxSessionsRows {
			start = p.selected - maxSessionsRows + 1
			if start < 0 {
				start = 0
			}
			if start > len(vis)-maxSessionsRows {
				start = len(vis) - maxSessionsRows
			}
		}
		end := start + maxSessionsRows
		if end > len(vis) {
			end = len(vis)
		}
		rowW := bodyW - 2
		if rowW < 1 {
			rowW = bodyW
		}
		b.WriteString("\n")
		for i := start; i < end; i++ {
			e := vis[i]
			line := sessionRowText(e)
			line = TruncateToWidth(line, rowW)
			if p.armed == e.id {
				line = theme.System.Render("⚠ " + line)
			} else if i == p.selected {
				line = theme.Accent.Render("› " + line)
			} else {
				line = theme.Chrome.Render("  " + line)
			}
			b.WriteString(line + "\n")
		}
	}

	if p.armed != "" {
		b.WriteString("\n" + theme.System.Render("删除该会话？y 确认 / 其他键取消") + "\n")
	} else if p.note != "" && len(vis) > 0 {
		b.WriteString("\n" + theme.Chrome.Render(p.note) + "\n")
	}

	b.WriteString("\n" + renderKeysLine(theme, bodyW, []keyBind{
		{"↑/↓", "选择"},
		{"Enter", "恢复"},
		{"d", "删除"},
		{"Esc", "关闭"},
	}))

	lines := strings.Split(panelBox(theme, b.String(), bodyW), "\n")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return centerPanel(strings.Join(lines, "\n"), width)
}

// sessionRowText renders one entry as a single plain line: title (or id),
// relative update time, model, project directory and message count. The row
// must stay one line — the scroll window counts terminal rows.
func sessionRowText(e sessionEntry) string {
	title := e.title
	if title == "" {
		title = "session " + shortID(e.id)
	}
	if e.current {
		title += "  ●当前"
	}
	seg := relativeTime(e.updated)
	if e.model != "" {
		seg += " · " + e.model
	}
	if e.cwd != "" {
		seg += " · " + abbreviateHome(e.cwd)
	}
	if e.count > 0 {
		seg += fmt.Sprintf(" · %d 条消息", e.count)
	}
	return title + "  " + seg
}

// relativeTime renders a coarse "x ago" label for a session's update time.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// shortID returns the first 8 characters of a session id for compact display.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// gatherSessions loads the picker's entry list from the session store: headers
// are cheap, but the title and message count need the entries, so each session
// file is read once per open (grok fetches summaries the same way).
func gatherSessions(store *session.Store, currentID string) ([]sessionEntry, string) {
	if store == nil {
		return nil, "(会话存储不可用：无活动会话)"
	}
	headers, err := store.List()
	if err != nil {
		return nil, fmt.Sprintf("(无法读取会话列表: %v)", err)
	}
	var out []sessionEntry
	for _, h := range headers {
		e := sessionEntry{
			id:      h.ID,
			model:   h.Model,
			cwd:     h.Cwd,
			updated: h.UpdatedAt,
			current: h.ID == currentID,
		}
		if _, entries, loadErr := store.LoadEntries(h.ID); loadErr == nil {
			e.count = len(entries)
			e.title = strings.TrimSpace(h.Title) // manual title wins (S2 /rename)
			if e.title == "" {
				e.title = firstUserPrompt(entries)
			}
		}
		out = append(out, e)
	}
	return out, ""
}

// firstUserPrompt returns the first user message's text as an auto title:
// newlines collapsed, capped at 60 runes (the first-prompt tier of the S2
// title chain; see termtitle.go firstUserPromptText).
func firstUserPrompt(entries []session.Entry) string {
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	return firstUserPromptText(msgs)
}
