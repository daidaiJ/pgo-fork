// This file implements the /model argument-completion popup (T7.3 S1, grok
// model-switcher alignment): while the input buffer is a "/model …" invocation
// with the argument being typed, a dropdown lists the selectable models — the
// preset catalog plus any ids fetched via /models fetch — each row "id — label
// (provider)", the active model tagged "(current)". Arrow keys navigate, Tab
// fills the highlighted id into the buffer, Enter switches immediately (the
// existing /model action runs, so the switch is live for the next turn), Esc
// keeps the current model and closes. It is the argument-stage counterpart of
// slashMenu (which owns the "/name" stage); the two never open together.
//
// Deviations from the S1 定稿 (tui-slash-ux.md §4): the qwen-style highlighted
// detail panel (context window / protocol / base URL lines) and grok's chained
// reasoning-effort submenu are deferred — the catalog's ContextWindow is shown
// inline in the row tail instead, and effort stays on /think. Registered in the
// spec's deviation log on landing.
package tui

import (
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/provider"
)

// maxModelMenuRows mirrors maxMenuRows for the model dropdown.
const maxModelMenuRows = 8

// modelPhaseKind distinguishes the two stages of the chained dropdown (grok
// model-switcher): the model list, then the effort sub-list a reasoning
// selection chains into — also reused for bare /think / /effect, whose only
// stage is the effort list.
type modelPhaseKind int

const (
	modelPhaseList modelPhaseKind = iota
	modelPhaseEffort
)

// modelMenu is the /model (and /think) argument popup state. It is inactive
// (renders as nothing) unless the buffer is one of those invocations in its
// argument stage; the effort phase keeps the same popup open with a different
// candidate list.
type modelMenu struct {
	theme    Theme
	active   bool
	phase    modelPhaseKind
	chainCmd string // effort phase: the command prefix the level appends to
	items    []modelItem
	selected int
}

// modelItem is one candidate row: a model in the list phase, an effort level
// in the effort phase (id then holds the level string).
type modelItem struct {
	id       string
	label    string
	provider string
	// desc is the config profile's one-line description (grok's row shape);
	// empty for preset/fetched rows.
	desc     string
	window   int
	current  bool
	level    agentcore.ThinkingLevel
}

// modelArgToken reports whether buffer is a "/model" invocation whose argument
// is being typed, and returns the partial argument. It fires only once the name
// stage is over — the character right after the "/model" prefix must be
// whitespace — so "/models" (a different command) and a bare "/model" still
// belong to the command-name popup.
func modelArgToken(buffer string) (partial string, ok bool) {
	trimmed := strings.TrimLeft(buffer, " \t")
	if !strings.HasPrefix(trimmed, "/model") {
		return "", false
	}
	rest := trimmed[len("/model"):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') || strings.Contains(rest, "\n") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// refresh recomputes the candidates from the current face, filtered by the
// case-insensitive partial argument. The face of record is the config's
// [models."<id>"] profile table (T7.3 实测反馈: /model 列表来源 = 配置里的
// 模型 id，grok 对齐); the preset catalog + fetched ids stay the fallback when
// no usable profile is declared. The selection resets to the top when the
// filtered set changes shape.
func (mm *modelMenu) refresh(partial string, live *cli.LiveConfig) {
	if live == nil {
		mm.close()
		return
	}
	needle := strings.ToLower(partial)
	seen := make(map[string]bool)
	var items []modelItem
	add := func(id, label, prov, desc string, window int, current bool) {
		if id == "" || seen[id] {
			return
		}
		if needle != "" && !strings.Contains(strings.ToLower(id+" "+label+" "+prov+" "+desc), needle) {
			return
		}
		seen[id] = true
		items = append(items, modelItem{id: id, label: label, provider: prov, desc: desc, window: window, current: current})
	}
	profileIDs := config.FileConfig{Models: live.ModelProfiles}.ProfileIDs()
	if len(profileIDs) > 0 {
		for _, key := range profileIDs {
			p := live.ModelProfiles[key]
			prov := p.Provider
			if prov == "" {
				prov = live.ProviderName
			}
			wire := p.WireModel(key)
			add(key, p.Label(key), prov, p.Description, p.ContextWindow,
				live.Model == wire || live.Model == key)
		}
	} else {
		for _, p := range provider.PresetCatalog {
			add(p.ID, p.Label(), p.Provider, "", p.ContextWindow, p.ID == live.Model)
		}
		for _, id := range live.FetchedModels {
			add(id, "", live.ProviderName, "", 0, id == live.Model)
		}
	}
	if len(items) == 0 {
		mm.close()
		return
	}
	if !mm.active || mm.phase != modelPhaseList || len(items) != len(mm.items) || mm.selected >= len(items) {
		mm.selected = 0
	}
	mm.phase = modelPhaseList
	mm.chainCmd = ""
	mm.items = items
	mm.active = true
}

// effortLevels lists pigo's reasoning levels in pick order (agentcore enum).
var effortLevels = []agentcore.ThinkingLevel{
	agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
	agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
	agentcore.ThinkingMax,
}

// refreshEffort swaps the popup to the effort sub-list (grok's chained
// second stage): chainCmd is the prefix the chosen level appends to —
// "/model <id> " from the model chain, or "/think " for the /think//effect
// argument stage. The current level is preselected (grok preselect_effort).
func (mm *modelMenu) refreshEffort(filter string, chainCmd string, live *cli.LiveConfig) {
	if live == nil {
		mm.close()
		return
	}
	current := live.ThinkingLevel
	if current == "" {
		current = agentcore.ThinkingOff
	}
	needle := strings.ToLower(filter)
	var items []modelItem
	for _, lvl := range effortLevels {
		if needle != "" && !strings.Contains(strings.ToLower(string(lvl)), needle) {
			continue
		}
		items = append(items, modelItem{id: string(lvl), level: lvl, current: lvl == current})
	}
	if len(items) == 0 {
		mm.close()
		return
	}
	if !mm.active || mm.phase != modelPhaseEffort || len(items) != len(mm.items) || mm.selected >= len(items) {
		mm.selected = 0
	}
	// Preselect the current level on first open (grok preselected_arg).
	if !mm.active || mm.phase != modelPhaseEffort {
		for i, it := range items {
			if it.current {
				mm.selected = i
				break
			}
		}
	}
	mm.phase = modelPhaseEffort
	mm.chainCmd = chainCmd
	mm.items = items
	mm.active = true
}

// enterEffort chains from a highlighted model row into its effort sub-list,
// exactly grok's trailing-space semantics: the buffer gains the model id and
// the trailing space, and the same popup re-lists the effort levels. pigo's
// ThinkingLevel is model-independent, so the chain opens for every model
// (grok gates on reasoning support; deviation registered in the spec).
func (mm *modelMenu) enterEffort(id string, live *cli.LiveConfig) {
	mm.refreshEffort("", "/model "+id+" ", live)
}

// thinkArgToken reports whether buffer is a /think (or /effect alias)
// invocation whose argument is being typed; prefix is always the canonical
// "/think " so both aliases share one popup grammar. Bare "/think" (no space
// yet) does not match — Enter fills the space first, mirroring the /model
// entry rule.
func thinkArgToken(buffer string) (partial string, prefix string, ok bool) {
	trimmed := strings.TrimLeft(buffer, " \t")
	for _, name := range []string{"/think", "/effect"} {
		if !strings.HasPrefix(trimmed, name) {
			continue
		}
		rest := trimmed[len(name):]
		if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		if strings.Contains(rest, "\n") {
			continue
		}
		return strings.TrimSpace(rest), "/think ", true
	}
	return "", "", false
}

// close deactivates the popup and drops its candidates.
func (mm *modelMenu) close() {
	mm.active = false
	mm.items = nil
	mm.selected = 0
}

// rows reports the popup's rendered height for relayout's row accounting.
func (mm modelMenu) rows() int {
	if !mm.active || len(mm.items) == 0 {
		return 0
	}
	if len(mm.items) > maxModelMenuRows {
		return maxModelMenuRows
	}
	return len(mm.items)
}

// current returns the highlighted candidate, or ok=false when inactive/empty.
func (mm modelMenu) current() (modelItem, bool) {
	if !mm.active || mm.selected < 0 || mm.selected >= len(mm.items) {
		return modelItem{}, false
	}
	return mm.items[mm.selected], true
}

// moveUp / moveDown cycle the highlight with wrap-around.
func (mm *modelMenu) moveUp() {
	if len(mm.items) == 0 {
		return
	}
	mm.selected--
	if mm.selected < 0 {
		mm.selected = len(mm.items) - 1
	}
}

func (mm *modelMenu) moveDown() {
	if len(mm.items) == 0 {
		return
	}
	mm.selected++
	if mm.selected >= len(mm.items) {
		mm.selected = 0
	}
}

// view renders the popup rows in the slashMenu grammar: a scrolled window of
// "id — label (provider)" lines, the highlighted row caret-accented, the live
// model tagged "(current)".
func (mm modelMenu) view(width int) string {
	if !mm.active || len(mm.items) == 0 {
		return ""
	}
	start := 0
	if n := len(mm.items); n > maxModelMenuRows {
		start = mm.selected - maxModelMenuRows + 1
		if start < 0 {
			start = 0
		}
		if start > n-maxModelMenuRows {
			start = n - maxModelMenuRows
		}
	}
	end := start + maxModelMenuRows
	if end > len(mm.items) {
		end = len(mm.items)
	}
	rowWidth := width - 2
	if rowWidth < 1 {
		rowWidth = width
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		it := mm.items[i]
		var line string
		if mm.phase == modelPhaseEffort {
			line = it.id
			if it.current {
				line += " (current)"
			}
		} else {
			line = it.id
			if it.label != "" && it.label != it.id {
				line += "  " + it.label
			}
			if it.desc != "" {
				line += "  " + it.desc
			}
			if it.current {
				line += " (current)"
			}
			tail := it.provider
			if it.window > 0 {
				tail += " · " + humanTokens(it.window) + " ctx"
			}
			if tail != "" {
				line += "  (" + tail + ")"
			}
		}
		line = TruncateToWidth(line, rowWidth)
		if i == mm.selected {
			b.WriteString(mm.theme.Accent.Render("› " + line))
		} else {
			b.WriteString(mm.theme.System.Render("  " + line))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
