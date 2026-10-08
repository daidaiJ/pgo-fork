package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// TestToolVerbFamilies pins the family identity table: read-only tools get the
// hollow diamond and the cyan read style, executing tools the filled diamond,
// and unknown tools the generic accent style with a title-cased verb.
func TestToolVerbFamilies(t *testing.T) {
	theme := DefaultTheme()
	cases := []struct {
		name     string
		verb     string
		readonly bool
	}{
		{"read", "Read", true},
		{"grep", "Grep", true},
		{"websearch", "Search", true},
		{"bash", "Run", false},
		{"write", "Write", false},
		{"edit", "Edit", false},
		{"task", "Task", false},
	}
	for _, c := range cases {
		fam := toolVerb(c.name)
		if fam.verb != c.verb || fam.readonly != c.readonly {
			t.Errorf("toolVerb(%q) = %q readonly=%v, want %q readonly=%v",
				c.name, fam.verb, fam.readonly, c.verb, c.readonly)
		}
	}
	if fam := toolVerb("custom_thing"); fam.verb != "Custom thing" {
		t.Errorf("unknown tool verb = %q, want title-cased %q", fam.verb, "Custom thing")
	}
	if fam := toolVerb("read"); fam.style(theme).GetForeground() != theme.ToolVerbRead.GetForeground() {
		t.Errorf("read family should use the cyan read style")
	}
	if fam := toolVerb("bash"); fam.style(theme).GetForeground() != theme.ToolVerbRun.GetForeground() {
		t.Errorf("bash family should use the green run style")
	}
}

// TestRenderToolRow verifies the collapsed activity row grammar: diamond +
// family verb + argument summary, running ellipsis, warn marker, and history
// dimming (S5–S7).
func TestRenderToolRow(t *testing.T) {
	theme := DefaultTheme()
	card := &toolCard{name: "bash", input: map[string]any{"command": "go test ./..."}, state: cardRunning}

	row := stripANSI(renderToolRow(card, theme, 80, false))
	if !strings.Contains(row, "◆ Run") {
		t.Errorf("running bash row %q should carry ◆ Run", row)
	}
	if !strings.Contains(row, "go test ./...") {
		t.Errorf("running bash row %q should carry the command summary", row)
	}
	if !strings.HasSuffix(row, "…") {
		t.Errorf("running row %q should end with the ellipsis", row)
	}

	card.state = cardSuccess
	if row := stripANSI(renderToolRow(card, theme, 80, false)); strings.HasSuffix(row, "…") {
		t.Errorf("completed row %q should drop the ellipsis", row)
	}

	card.state = cardWarn
	if row := stripANSI(renderToolRow(card, theme, 80, false)); !strings.Contains(row, "✗") {
		t.Errorf("failed row %q should carry the warn marker", row)
	}
	card.state = cardSuccess

	readCard := &toolCard{name: "read", input: map[string]any{"path": "/x/y"}, state: cardSuccess}
	if row := stripANSI(renderToolRow(readCard, theme, 80, false)); !strings.Contains(row, "◇ Read") {
		t.Errorf("read row %q should carry ◇ Read", row)
	}

	// History dim: the row text survives but drops family styling.
	dimRow := renderToolRow(card, theme, 80, true)
	if dim := stripANSI(dimRow); !strings.Contains(dim, "◆ Run") {
		t.Errorf("dim row %q lost content", dim)
	}
	if !strings.Contains(dimRow, theme.Chrome.Render("◆ Run")) {
		t.Errorf("dim row should use the chrome style: %q", dimRow)
	}
}

// TestRenderToolRowTruncates verifies the row never exceeds the content width.
func TestRenderToolRowTruncates(t *testing.T) {
	theme := DefaultTheme()
	card := &toolCard{
		name:  "bash",
		input: map[string]any{"command": strings.Repeat("x", 200)},
		state: cardSuccess,
	}
	for _, width := range []int{10, 20, 40} {
		row := renderToolRow(card, theme, width, false)
		if w := ui.Width(stripANSI(row)); w > width {
			t.Errorf("width %d: row width %d overflows: %q", width, w, stripANSI(row))
		}
	}
}

// TestToggleToolFoldCycle drives the transcript-level Ctrl+O cycle directly
// through the transcript API (collapsed → card → full tree → collapsed).
func TestToggleToolFoldCycle(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 10)
	card := &toolCard{name: "bash", state: cardSuccess}
	tr.addToolCard(card)

	if tr.blocks[0].display != displayCollapsed {
		t.Fatalf("fresh tool block display = %v, want collapsed", tr.blocks[0].display)
	}
	tr.toggleTool()
	if tr.blocks[0].display != displayFull || tr.blocks[0].card.expanded {
		t.Fatalf("first toggle: display=%v expanded=%v, want full/capped", tr.blocks[0].display, tr.blocks[0].card.expanded)
	}
	tr.toggleTool()
	if !tr.blocks[0].card.expanded {
		t.Fatalf("second toggle should reveal the full tree")
	}
	tr.toggleTool()
	if tr.blocks[0].display != displayCollapsed || tr.blocks[0].card.expanded {
		t.Fatalf("third toggle should collapse back to the row")
	}
}

// TestRenderUserBand verifies the user turn renders as a full-width band with
// the "❯" prefix and a right-aligned timestamp on the first line (S3/S4).
func TestRenderUserBand(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 10)
	tr.addUser("hello")
	content := stripANSI(tr.renderAll())

	lines := strings.Split(content, "\n")
	if !strings.HasPrefix(lines[0], "❯ hello") {
		t.Errorf("band first line = %q, want ❯ prefix + text", lines[0])
	}
	if w := ui.Width(lines[0]); w != 40 {
		t.Errorf("band line width = %d, want the full 40 columns", w)
	}
	if !strings.Contains(lines[0], "AM") && !strings.Contains(lines[0], "PM") {
		t.Errorf("band first line missing the timestamp: %q", lines[0])
	}
}

// TestTranscriptBlockCache verifies the C3 render cache: a finalized block
// serves a repeat render from the cache, and a content mutation invalidates it.
func TestTranscriptBlockCache(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 10)
	tr.addUser("hi")

	blk := &tr.blocks[0]
	first := tr.renderBlock(blk, false)
	if !strings.Contains(stripANSI(first), "❯ hi") {
		t.Fatalf("unexpected band render %q", stripANSI(first))
	}
	if blk.cacheOut == "" || blk.cacheKey.width != 40 {
		t.Fatalf("render should populate the block cache, key %+v", blk.cacheKey)
	}
	if again := tr.renderBlock(blk, false); again != first {
		t.Errorf("cached render differs from the first render")
	}

	// A width change is a different cache identity.
	if wide := tr.renderBlock(blk, false); true {
		old := blk.cacheKey.width
		_ = wide
		tr.setSize(50, 10)
		blk2 := &tr.blocks[0]
		_ = tr.renderBlock(blk2, false)
		if blk2.cacheKey.width != 50 {
			t.Errorf("cache key should track the new width, got %d (old %d)", blk2.cacheKey.width, old)
		}
	}
}

// TestWithRightMeta verifies the timestamp helper appends on the last content
// line when it fits and on its own right-aligned line when it does not (S10).
func TestWithRightMeta(t *testing.T) {
	base := "hello"
	out := stripANSI(withRightMeta(base, 20, "2:19 PM", DefaultTheme().Chrome))
	if !strings.HasPrefix(out, "hello") || !strings.HasSuffix(out, "2:19 PM") {
		t.Errorf("same-line meta = %q, want hello…2:19 PM", out)
	}
	if ui.Width(out) != 20 {
		t.Errorf("same-line meta width = %d, want 20", ui.Width(out))
	}

	// No room: the meta drops to its own line, still right-aligned.
	narrow := stripANSI(withRightMeta(strings.Repeat("x", 20), 20, "2:19 PM", DefaultTheme().Chrome))
	lines := strings.Split(narrow, "\n")
	if len(lines) != 2 {
		t.Fatalf("own-line meta should add one line, got %d", len(lines))
	}
	if !strings.HasSuffix(lines[1], "2:19 PM") {
		t.Errorf("own-line meta not right-aligned: %q", lines[1])
	}
}

// TestHistoryDim verifies the S6 brightness rule: tool and thinking rows from
// earlier turns dim, the current turn's stay bright, and user/assistant blocks
// never dim.
func TestHistoryDim(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 10)
	tr.addUser("one") // turn 1
	tr.addToolCard(&toolCard{name: "bash", state: cardSuccess})
	tr.addUser("two") // turn 2

	if tr.historyDim(&tr.blocks[0]) {
		t.Errorf("user block must never dim")
	}
	if !tr.historyDim(&tr.blocks[1]) {
		t.Errorf("previous-turn tool row should dim")
	}
	if tr.historyDim(&tr.blocks[2]) {
		t.Errorf("user block must never dim")
	}
}

// TestKeysLineModes verifies the S13 keys line follows the shell mode.
func TestKeysLineModes(t *testing.T) {
	theme := DefaultTheme()

	idle := stripANSI(renderKeysLine(theme, 100, []keyBind{
		{"Enter", "发送"}, {"Ctrl+O", "工具"}, {"Ctrl+T", "思考"}, {"Ctrl+C", "退出"},
	}))
	// grok hint grammar: key + two spaces + label, pairs joined by four spaces.
	for _, want := range []string{"Enter  发送", "Ctrl+O  工具", "Ctrl+T  思考", "Ctrl+C  退出"} {
		if !strings.Contains(idle, want) {
			t.Errorf("idle keys line missing %q: %q", want, idle)
		}
	}
	if !strings.Contains(idle, "发送    Ctrl+O") {
		t.Errorf("idle keys line missing the wide pair gap: %q", idle)
	}

	running := stripANSI(renderKeysLine(theme, 100, []keyBind{{"Ctrl+C", "停止"}}))
	if !strings.Contains(running, "Ctrl+C  停止") || strings.Contains(running, "发送") {
		t.Errorf("running keys line = %q, want only the stop bind", running)
	}

	if got := renderKeysLine(theme, 0, []keyBind{{"Enter", "发送"}}); got != "" {
		t.Errorf("zero width should render empty, got %q", got)
	}
}

// TestContextPanelToggleAndKeys verifies the overlay is modal: its keys are
// consumed while open, tab switches tabs, esc closes, and a closed panel
// consumes nothing.
func TestContextPanelToggleAndKeys(t *testing.T) {
	p := contextPanel{}
	if p.handleKey("esc") {
		t.Fatal("closed panel must not consume keys")
	}
	p.toggle()
	if !p.open {
		t.Fatal("toggle should open the panel")
	}
	if p.tab != 0 {
		t.Fatalf("fresh panel tab = %d, want 0", p.tab)
	}
	if !p.handleKey("tab") || p.tab != 1 {
		t.Errorf("tab should switch to the session tab")
	}
	if !p.handleKey("esc") || p.open {
		t.Errorf("esc should close the panel")
	}
}

// TestContextPanelRender verifies the context tab draws the budget line, the
// diamond grid, and the footer keys; the session tab shows the report body.
func TestContextPanelRender(t *testing.T) {
	p := contextPanel{}
	p.toggle()
	d := contextData{
		modelName:   "glm-5.3 (volcengine)",
		tokens:      3000,
		window:      1_000_000,
		toolCount:   27,
		toolTokens:  2700,
		skillCount:  54,
		skillTokens: 1300,
	}
	out := stripANSI(p.render(DefaultTheme(), d, 80, 30))
	for _, want := range []string{
		"上下文用量", "会话信息", "[X]", "Context",
		"3.0K / 1.0M tokens (0.30%)", "glm-5.3 (volcengine)",
		"Tool definitions", "· 27 tools", "Skills", "· 54 skills",
		"Tab", "复制会话 ID",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("context panel missing %q", want)
		}
	}

	p.tab = 1
	p.scroll = 0
	d.sessionReport = "session: xyz"
	out = stripANSI(p.render(DefaultTheme(), d, 80, 30))
	if !strings.Contains(out, "session: xyz") {
		t.Errorf("session tab should render the report, got:\n%s", out)
	}
}

// TestModelContextPanelOpenDrive verifies the /context slash interception
// opens the modal panel and esc returns to the transcript.
func TestModelContextPanelOpenDrive(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	// The interception lives in runSlash; drive it directly.
	next, _ := m.runSlash("/context")
	mm := next.(Model)
	if !mm.ctxPanel.open {
		t.Fatalf("/context should open the panel")
	}
	view, _ := mm.renderContent()
	if !strings.Contains(stripANSI(view), "上下文用量") {
		t.Errorf("panel should replace the transcript region:\n%s", stripANSI(view))
	}
}

// TestShellNeverOverflowsWidth verifies the C4 page assembly: at any terminal
// width every shell row (idle, running, context panel open) fits the width —
// no region may spill past the terminal's own wrap boundary.
func TestShellNeverOverflowsWidth(t *testing.T) {
	base := NewModel(Options{Model: "test-model", ProviderName: "prov", Approve: true})
	base = apply(t, base, tea.WindowSizeMsg{Width: 60, Height: 20})
	base = apply(t, base, toolStartMsg{id: "t1", name: "bash", input: map[string]any{"command": "go test"}})
	base = apply(t, base, toolEndMsg{id: "t1", ok: true, result: "ok"})

	next, _ := base.runSlash("/context")
	panel := next.(Model)

	cases := []struct {
		name string
		m    Model
	}{
		{"idle", base},
		{"panel", panel},
	}
	for _, width := range []int{20, 40, 60, 100} {
		for _, c := range cases {
			m := c.m
			upd, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
			m = upd.(Model)
			content, _ := m.renderContent()
			for i, line := range strings.Split(stripANSI(content), "\n") {
				if w := ui.Width(line); w > width {
					t.Errorf("%s width %d: row %d display width %d overflows: %q",
						c.name, width, i, w, line)
				}
			}
		}
	}
}

// TestToolRowTodoDigest verifies the todo row shows a progress digest instead
// of the raw todos array (user report: Go map syntax leaked onto the row).
func TestToolRowTodoDigest(t *testing.T) {
	c := &toolCard{name: "todo", input: map[string]any{
		"todos": []any{
			map[string]any{"content": "Check files", "status": "completed"},
			map[string]any{"content": "Identify artifacts", "status": "pending"},
		},
	}}
	row := stripANSI(renderToolRow(c, DefaultTheme(), 100, false))
	if !strings.Contains(row, "1/2 done · Check files") {
		t.Errorf("todo row should show the digest, got %q", row)
	}
	if strings.Contains(row, "map[") {
		t.Errorf("todo row leaked Go map syntax: %q", row)
	}
}

// TestDefaultPrimaryArgJSON verifies the generic fallback serializes composite
// values as compact JSON — never Go's %v map syntax.
func TestDefaultPrimaryArgJSON(t *testing.T) {
	c := &toolCard{name: "mystery", input: map[string]any{
		"opts": map[string]any{"depth": 3, "mode": "fast"},
	}}
	arg := c.defaultPrimaryArg()
	if strings.Contains(arg, "map[") {
		t.Errorf("primary arg leaked Go map syntax: %q", arg)
	}
	if arg != `{"depth":3,"mode":"fast"}` {
		t.Errorf("primary arg = %q, want compact JSON", arg)
	}
	if got := formatArgValue("plain text"); got != "plain text" {
		t.Errorf("string value = %q, want verbatim", got)
	}
}
