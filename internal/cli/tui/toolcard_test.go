package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlKey builds a Ctrl+<letter> key press matching String()=="ctrl+<letter>".
func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// TestParseToolResult verifies depth inference from leading spaces and trailing
// blank-line trimming.
func TestParseToolResult(t *testing.T) {
	nodes := parseToolResult("root\n  child\n    grandchild\n\n")
	if len(nodes) != 3 {
		t.Fatalf("node count = %d, want 3 (trailing blank trimmed)", len(nodes))
	}
	want := []respNode{
		{text: "root", depth: 0},
		{text: "child", depth: 1},
		{text: "grandchild", depth: 2},
	}
	for i, w := range want {
		if nodes[i] != w {
			t.Errorf("node[%d] = %+v, want %+v", i, nodes[i], w)
		}
	}
}

// TestToolCardRender checks the header (name + status icon), the input section,
// and the response tree lines appear in the rendered card, and that the status
// icon reflects the state.
func TestToolCardRender(t *testing.T) {
	theme := DefaultTheme()
	cases := []struct {
		name  string
		state cardState
		icon  string
	}{
		{"running", cardRunning, "…"},
		{"success", cardSuccess, "✓"},
		{"warn", cardWarn, "!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := toolCard{
				id:       "1",
				name:     "read_file",
				input:    map[string]any{"path": "/tmp/x"},
				response: parseToolResult("line one\n  nested"),
				state:    tc.state,
			}
			out := card.render(theme, 60, true, false)
			for _, want := range []string{"read_file", tc.icon, "Input arguments", "path: /tmp/x", "Response", "line one", "nested"} {
				if !strings.Contains(out, want) {
					t.Errorf("render missing %q\n%s", want, out)
				}
			}
		})
	}
}

// TestToolCardMultiLineInputIndent verifies a multi-line input value (write's
// content) keeps continuation lines aligned under the value column instead of
// starting at column zero (tui-crush-components.md §12 小账②).
func TestToolCardMultiLineInputIndent(t *testing.T) {
	theme := DefaultTheme()
	card := toolCard{
		id:    "1",
		name:  "write",
		input: map[string]any{"path": "/tmp/x", "content": "first line\nsecond line\nthird line"},
		state: cardSuccess,
	}
	out := card.render(theme, 60, true, false)
	// lipgloss wraps every rendered line in ANSI escapes and the card frame adds
	// border columns, so compare column positions within the plain text instead
	// of matching raw substrings: each continuation line must start at the same
	// column as the first line's value ("second" under "first" after "content: ").
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	plain := ansi.ReplaceAllString(out, "")
	valueCol := -1
	for _, ln := range strings.Split(plain, "\n") {
		if i := strings.Index(ln, "first line"); i >= 0 {
			valueCol = i
			break
		}
	}
	if valueCol < 0 {
		t.Fatalf("render missing content value\n%s", plain)
	}
	for _, text := range []string{"second line", "third line"} {
		found := false
		for _, ln := range strings.Split(plain, "\n") {
			i := strings.Index(ln, text)
			if i < 0 {
				continue
			}
			found = true
			if i != valueCol {
				t.Errorf("continuation %q at column %d, want the value column %d (flush-left or misaligned)\n%s", text, i, valueCol, plain)
			}
		}
		if !found {
			t.Errorf("render missing continuation %q\n%s", text, plain)
		}
	}
}

// TestToolCardExpandTruncation verifies the collapsed card caps the response and
// shows the Ctrl+O hint, while the expanded card reveals every line.
func TestToolCardExpandTruncation(t *testing.T) {
	theme := DefaultTheme()
	var b strings.Builder
	for i := 0; i < collapsedResponseLines+3; i++ {
		b.WriteString("resp-line-")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	card := toolCard{name: "grep", response: parseToolResult(b.String()), state: cardSuccess}

	collapsed := card.render(theme, 60, true, false)
	if !strings.Contains(collapsed, "(Ctrl+O for more)") {
		t.Errorf("collapsed card should show Ctrl+O hint\n%s", collapsed)
	}
	lastLine := "resp-line-" + string(byte('a'+collapsedResponseLines+2))
	if strings.Contains(collapsed, lastLine) {
		t.Errorf("collapsed card should not show %q\n%s", lastLine, collapsed)
	}

	card.expanded = true
	expanded := card.render(theme, 60, true, false)
	if strings.Contains(expanded, "(Ctrl+O for more)") {
		t.Errorf("expanded card should not show Ctrl+O hint\n%s", expanded)
	}
	if !strings.Contains(expanded, lastLine) {
		t.Errorf("expanded card should show %q\n%s", lastLine, expanded)
	}
}

// TestModelToolCardFlow drives the model through a tool start/end and asserts the
// card is created, transitions running→success, and that a failed tool yields
// warn.
func TestModelToolCardFlow(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(toolStartMsg{id: "t1", name: "read_file", input: map[string]any{"path": "a.go"}})
	mm := next.(Model)
	card, ok := mm.toolCards["t1"]
	if !ok {
		t.Fatalf("toolStartMsg should create a card")
	}
	if card.state != cardRunning {
		t.Errorf("new card state = %v, want cardRunning", card.state)
	}

	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: "done\n  detail"})
	mm = next.(Model)
	if mm.toolCards["t1"].state != cardSuccess {
		t.Errorf("state after ok end = %v, want cardSuccess", mm.toolCards["t1"].state)
	}
	if len(mm.toolCards["t1"].response) != 2 {
		t.Errorf("response nodes = %d, want 2", len(mm.toolCards["t1"].response))
	}

	// A failed tool flips the same card to warn.
	next, _ = m.Update(toolStartMsg{id: "t2", name: "bash"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "t2", ok: false, result: "boom"})
	mm = next.(Model)
	if mm.toolCards["t2"].state != cardWarn {
		t.Errorf("state after failed end = %v, want cardWarn", mm.toolCards["t2"].state)
	}
}

// TestToolCardDiffSection verifies a card carrying a diff renders a dedicated
// Diff section whose lines carry the per-line theme colors: red removals,
// green additions, cyan @@ markers, dim headers and context (#560).
func TestToolCardDiffSection(t *testing.T) {
	theme := DefaultTheme()
	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n alpha\n-beta\n+BETA\n gamma\n"
	card := toolCard{
		name:     "edit",
		input:    map[string]any{"path": "f.txt"},
		response: parseToolResult("Edited f.txt (1 replacement(s))"),
		diff:     diff,
		state:    cardSuccess,
	}
	out := card.render(theme, 60, true, false)

	for _, want := range []string{
		"edit(f.txt)",
		"Response",
		"Edited f.txt (1 replacement(s))",
		"Diff",
		"--- a/f.txt",
		"@@ -1,3 +1,3 @@",
		"-beta",
		"+BETA",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n%s", want, out)
		}
	}
	// The diff lines are styled, not plain body text.
	for _, styled := range []string{
		theme.DiffDel.Render("  -beta"),
		theme.DiffAdd.Render("  +BETA"),
		theme.DiffHunk.Render("  @@ -1,3 +1,3 @@"),
		theme.DiffCtx.Render("  --- a/f.txt"),
		theme.DiffCtx.Render("   alpha"),
	} {
		if !strings.Contains(out, styled) {
			t.Errorf("render missing styled diff line %q\n%s", styled, out)
		}
	}
}

// TestToolCardDiffCollapseExpand verifies the Diff section obeys the same
// collapse/expand behavior as the response: capped with a Ctrl+O hint when
// collapsed, fully shown once expanded.
func TestToolCardDiffCollapseExpand(t *testing.T) {
	theme := DefaultTheme()
	var b strings.Builder
	b.WriteString("--- a/f.txt\n+++ b/f.txt\n")
	for i := 0; i < collapsedDiffLines+3; i++ {
		b.WriteString("+line\n")
	}
	card := toolCard{
		name:     "edit",
		response: parseToolResult("Edited f.txt (1 replacement(s))"),
		diff:     b.String(),
		state:    cardSuccess,
	}

	collapsed := card.render(theme, 60, true, false)
	if !strings.Contains(collapsed, "(Ctrl+O for more)") {
		t.Errorf("collapsed card should show Ctrl+O hint\n%s", collapsed)
	}
	// The cap counts all diff lines, so the 2 header lines leave room for
	// collapsedDiffLines-2 additions.
	if got := strings.Count(collapsed, "+line"); got != collapsedDiffLines-2 {
		t.Errorf("collapsed card shows %d additions, want %d\n%s", got, collapsedDiffLines-2, collapsed)
	}

	card.expanded = true
	expanded := card.render(theme, 60, true, false)
	if strings.Contains(expanded, "(Ctrl+O for more)") {
		t.Errorf("expanded card should not show Ctrl+O hint\n%s", expanded)
	}
	if got := strings.Count(expanded, "+line"); got != collapsedDiffLines+3 {
		t.Errorf("expanded card shows %d additions, want %d\n%s", got, collapsedDiffLines+3, expanded)
	}
}

// TestStripDiffTail verifies the response text keeps only its summary once the
// diff moves to its own section: the cut happens at the diff header, and a
// text without a diff passes through untouched.
func TestStripDiffTail(t *testing.T) {
	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n"
	text := "Edited f.txt (1 replacement(s))\n" + diff
	if got := stripDiffTail(text); got != "Edited f.txt (1 replacement(s))\n" {
		t.Errorf("stripDiffTail = %q, want the summary line only", got)
	}
	// A result clipped mid-diff still splits at the header.
	if got := stripDiffTail(text[:len(text)-5]); got != "Edited f.txt (1 replacement(s))\n" {
		t.Errorf("stripDiffTail on clipped text = %q, want the summary line only", got)
	}
	// Text without a diff header is left alone.
	plain := "done\nsome output\n"
	if got := stripDiffTail(plain); got != plain {
		t.Errorf("stripDiffTail on plain text = %q, want unchanged", got)
	}
}

// TestModelToolEndDiff drives a tool start/end pair whose end event carries
// edit-style Details, and verifies the model stores the diff on the card and
// keeps only the summary in the response (no duplicated diff).
func TestModelToolEndDiff(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(toolStartMsg{id: "e1", name: "edit", input: map[string]any{"path": "f.txt"}})
	mm := next.(Model)

	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n"
	details := map[string]any{"path": "f.txt", "replacements": 1, "diff": diff}
	next, _ = mm.Update(toolEndMsg{
		id:      "e1",
		ok:      true,
		result:  "Edited f.txt (1 replacement(s))\n" + diff,
		details: details,
	})
	mm = next.(Model)

	card, ok := mm.toolCards["e1"]
	if !ok {
		t.Fatalf("toolEndMsg should keep the card")
	}
	if card.diff != diff {
		t.Errorf("card.diff = %q, want the diff from Details", card.diff)
	}
	if len(card.response) != 1 || card.response[0].text != "Edited f.txt (1 replacement(s))" {
		t.Errorf("card.response = %+v, want only the summary line", card.response)
	}

	// Without Details the card renders as before: full text, no diff section.
	next, _ = m.Update(toolStartMsg{id: "e2", name: "edit"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "e2", ok: true, result: "Edited g.txt (1 replacement(s))\n" + diff})
	mm = next.(Model)
	card2 := mm.toolCards["e2"]
	if card2.diff != "" {
		t.Errorf("card without Details should carry no diff, got %q", card2.diff)
	}
	if len(card2.response) == 1 && card2.response[0].text == "Edited g.txt (1 replacement(s))" {
		// The response keeps the embedded diff text when there is no Details to
		// split on, so more than the summary should be present.
		t.Errorf("response should keep the embedded diff when Details is absent: %+v", card2.response)
	}
}

// TestModelCtrlOTogglesExpanded drives the Ctrl+O fold cycle (S5/C1): the tool
// block starts as the collapsed diamond row, the first Ctrl+O expands to the
// flat card (capped response), the second reveals the full response tree, and
// the third returns to the collapsed row.
func TestModelCtrlOTogglesExpanded(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	mm := next.(Model)

	var b strings.Builder
	for i := 0; i < collapsedResponseLines+3; i++ {
		b.WriteString("row")
		b.WriteByte(byte('0' + i))
		b.WriteByte('\n')
	}
	next, _ = mm.Update(toolStartMsg{id: "t1", name: "grep"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: b.String()})
	mm = next.(Model)

	blk := &mm.transcript.blocks[0]
	if blk.display != displayCollapsed || blk.card.expanded {
		t.Fatalf("tool block should start collapsed, display=%v expanded=%v", blk.display, blk.card.expanded)
	}
	next, _ = mm.Update(ctrlKey('o'))
	mm = next.(Model)
	blk = &mm.transcript.blocks[0]
	if blk.display != displayFull || blk.card.expanded {
		t.Errorf("first Ctrl+O should expand to the flat card, display=%v expanded=%v", blk.display, blk.card.expanded)
	}
	// Second Ctrl+O reveals the full response tree.
	next, _ = mm.Update(ctrlKey('o'))
	mm = next.(Model)
	blk = &mm.transcript.blocks[0]
	if blk.display != displayFull || !blk.card.expanded {
		t.Errorf("second Ctrl+O should reveal the full tree, display=%v expanded=%v", blk.display, blk.card.expanded)
	}
	// Third Ctrl+O returns to the collapsed row.
	next, _ = mm.Update(ctrlKey('o'))
	mm = next.(Model)
	blk = &mm.transcript.blocks[0]
	if blk.display != displayCollapsed || blk.card.expanded {
		t.Errorf("third Ctrl+O should collapse back to the row, display=%v expanded=%v", blk.display, blk.card.expanded)
	}
}

// TestToolCardRendererRegistry verifies the registry routing: known tool names
// (case-insensitively) resolve to their family renderer, unknown names fall
// back to the generic renderer.
func TestToolCardRendererRegistry(t *testing.T) {
	cases := []struct {
		name string
		want toolCardRenderer
	}{
		{"bash", bashToolRenderer{}},
		{"BASH", bashToolRenderer{}},
		{"bash_output", bashToolRenderer{}},
		{"kill_bash", bashToolRenderer{}},
		{"read", fileToolRenderer{}},
		{"write", fileToolRenderer{}},
		{"find", fileToolRenderer{}},
		{"ls", fileToolRenderer{}},
		{"grep", fileToolRenderer{}},
		{"memory_search", fileToolRenderer{}},
		{"edit", editToolRenderer{}},
		{"totally_unknown", genericToolRenderer{}},
		{"", genericToolRenderer{}},
	}
	for _, tc := range cases {
		if got := toolCardRendererFor(tc.name); got != tc.want {
			t.Errorf("toolCardRendererFor(%q) = %T, want %T", tc.name, got, tc.want)
		}
	}
}

// TestToolCardFamilyFolding verifies each family's folding detail (T2.4):
// bash folds the Input arguments section and relabels the response "Output";
// edit folds Input arguments (the Diff section carries the change); file
// family and the generic fallback keep the full layout.
func TestToolCardFamilyFolding(t *testing.T) {
	theme := DefaultTheme()
	input := map[string]any{"path": "f.txt", "command": "go vet"}
	cases := []struct {
		name      string
		wantArgs  bool
		respLabel string
	}{
		{"bash", false, "Output"},
		{"edit", false, "Response"},
		{"read", true, "Response"},
		{"mystery_tool", true, "Response"},
	}
	for _, tc := range cases {
		card := toolCard{
			name:     tc.name,
			input:    input,
			response: parseToolResult("line"),
			state:    cardSuccess,
		}
		out := card.render(theme, 60, true, false)
		hasArgs := strings.Contains(out, "Input arguments")
		if hasArgs != tc.wantArgs {
			t.Errorf("%s: Input arguments present = %v, want %v\n%s", tc.name, hasArgs, tc.wantArgs, out)
		}
		if !strings.Contains(out, tc.respLabel) {
			t.Errorf("%s: response section should be labeled %q\n%s", tc.name, tc.respLabel, out)
		}
		// Every family keeps the header (icon + name) and the response body.
		if !strings.Contains(out, tc.name) || !strings.Contains(out, "line") {
			t.Errorf("%s: lost header or response body\n%s", tc.name, out)
		}
	}
}

// TestToolCardPrimaryArgPerFamily verifies the header argument selection per
// family: bash picks "command", file/edit pick "path" (falling back to
// "file_path"), generic picks the first key in sorted order.
func TestToolCardPrimaryArgPerFamily(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"bash", map[string]any{"command": "go test", "timeout": 30}, "go test"},
		{"read", map[string]any{"path": "a.go", "limit": 5}, "a.go"},
		{"edit", map[string]any{"file_path": "b.go"}, "b.go"},
		{"edit", map[string]any{"old_string": "x"}, ""},
		{"mystery", map[string]any{"alpha": 1, "beta": 2}, "1"},
		{"bash", map[string]any{"timeout": 30}, ""},
	}
	for _, tc := range cases {
		card := toolCard{name: tc.name, input: tc.input}
		if got := toolCardRendererFor(tc.name).primaryArg(&card); got != tc.want {
			t.Errorf("%s primaryArg = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestToolCardRenderCache verifies the cachedMessageItem-style primitive:
// repeat renders at the same state are served from the cache, and a state
// change (expanded flip through the expandable interface) invalidates it.
func TestToolCardRenderCache(t *testing.T) {
	theme := DefaultTheme()
	var b strings.Builder
	for i := 0; i < collapsedResponseLines+3; i++ {
		b.WriteString("row\n")
	}
	card := &toolCard{name: "grep", response: parseToolResult(b.String()), state: cardSuccess}

	first := card.render(theme, 60, true, false)
	if !card.hasCache {
		t.Fatalf("render should populate the cache")
	}
	if again := card.render(theme, 60, true, false); again != first {
		t.Errorf("cached render differs from the first render")
	}

	// Toggling through the capability interface invalidates the cache.
	card.toggleExpanded()
	expanded := card.render(theme, 60, true, false)
	if expanded == first {
		t.Errorf("expanded render should differ after cache invalidation")
	}
	if strings.Contains(expanded, "(Ctrl+O for more)") {
		t.Errorf("expanded render still shows the truncation hint")
	}

	// A different width is a different cache identity: no stale reuse.
	card.toggleExpanded() // back to collapsed
	narrow := card.render(theme, narrowCardWidth-1, true, false)
	if narrow == "" {
		t.Errorf("narrow render should not be empty")
	}
	if card.render(theme, 60, true, false) != first {
		t.Errorf("re-render at the original width should match the original output")
	}
}
