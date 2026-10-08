package tui

// Regression pin for the phantom slash commands (T7.7, spec
// wiki/port/slash-command-surface.md §4.7): the shared registry advertises 15
// REPL-intercepted built-ins as stubs whose Action returns "" (internal/cli/
// prompts/registry.go, "registered here only so /help lists them"). The TUI
// intercepts five of those names (/exit /quit /session /remote-control
// /rewind); the remaining ten fall through runSlash's intercept chain to
// registry resolution and silently do nothing: the line is echoed, the input
// clears, and neither a status message nor a run follows. These tests pin the
// defect exactly so the T7.7 contract refactor replaces it with execution or
// an explicit rejection without the advertised surface regressing unnoticed —
// flip the assertions when the phantom commands gain behavior.

import (
	"strings"
	"testing"
)

// phantomLines is one representative invocation per advertised-but-inert TUI
// command, in the form a user would type. replDoes notes what the REPL does
// for the same line — the interception the TUI is missing. Derived from the
// stub table in prompts/registry.go against the full fall-through of
// runSlash (model.go).
var phantomLines = []struct{ line, replDoes string }{
	{"/compact", "run a manual compaction over the live context"},
	{"/fork 2", "branch a new session from historical message 2"},
	{"/clone", "duplicate the current session at its current leaf"},
	{"/tree 2", "print the branch tree / switch the active branch"},
	{"/export out.jsonl", "write the session transcript to a file"},
	{"/import in.jsonl", "import a JSONL export as a new session"},
	{"/copy", "copy the last assistant reply to the clipboard"},
	{"/goal --tokens 100 ship it", "drive the autonomous goal loop"},
	{"/btw why pointers", "ask a side question in a hidden thread"},
	{"/dream --dry-run", "consolidate memory (dry-run preview)"},
}

// phantomNames returns the "/name" of each phantom line.
func phantomNames() []string {
	names := make([]string, len(phantomLines))
	for i, p := range phantomLines {
		names[i] = strings.Fields(p.line)[0]
	}
	return names
}

// TestPhantomCommandsAdvertised verifies the inert commands still reach the
// user: the completion menu lists all of them and /help prints every name.
// When the stub registrations are deleted (T7.7 slice 2) this test flips to
// assert the declared surface that replaces them.
func TestPhantomCommandsAdvertised(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/").(Model)
	if !m.menu.active {
		t.Fatalf("menu should be active after typing '/'")
	}
	advertised := phantomNames()
	if !containsAll(menuNames(m), advertised...) {
		t.Errorf("completion menu should list the advertised commands; got %v", menuNames(m))
	}
	got, _ := m.runSlash("/help")
	joined := strings.Join(blockTexts(got.(Model).transcript), "\n")
	for _, name := range advertised {
		if !strings.Contains(joined, name) {
			t.Errorf("/help output should list %s; transcript:\n%s", name, joined)
		}
	}
}

// TestPhantomCommandsDispatchSilently pins the defect itself: submitting one
// of the ten commands echoes the line and clears the input but produces no
// status message, no run, and no command — a silent no-op where the REPL does
// real work. T7.7 replaces this with execution or an explicit rejection; flip
// the assertions then.
func TestPhantomCommandsDispatchSilently(t *testing.T) {
	for _, p := range phantomLines {
		got, cmd := NewModel(Options{}).runSlash(p.line)
		gm := got.(Model)
		if cmd != nil {
			t.Errorf("%s: dispatch returned a command; silent no-op should return nil (REPL: %s)", p.line, p.replDoes)
		}
		if gm.running {
			t.Errorf("%s: dispatch must not start a run (REPL: %s)", p.line, p.replDoes)
		}
		if gm.input.Value() != "" {
			t.Errorf("%s: input should be cleared after submit", p.line)
		}
		blocks := blockTexts(gm.transcript)
		if len(blocks) != 1 || blocks[0] != p.line {
			t.Errorf("%s: transcript should hold only the echoed line, got %q (REPL: %s)", p.line, blocks, p.replDoes)
		}
	}
}

// TestInterceptedStubsStayReachable pins the other side of the same stub
// table: /session and /rewind are intercepted by the TUI and degrade to an
// explicit no-session notice instead of the registry's empty Action, and
// /exit /quit quit (pinned by TestSlashExitQuits). When the intercept chain is
// folded into declarations (T7.7 slice 2), these notices are the behavior to
// preserve.
func TestInterceptedStubsStayReachable(t *testing.T) {
	for _, line := range []string{"/session", "/rewind"} {
		got, _ := NewModel(Options{}).runSlash(line)
		gm := got.(Model)
		blocks := blockTexts(gm.transcript)
		if len(blocks) != 2 {
			t.Fatalf("%s: want echo + notice, got %q", line, blocks)
		}
		want := "(" + strings.TrimPrefix(line, "/") + " unavailable: no active session)"
		if blocks[1] != want {
			t.Errorf("%s: notice = %q, want %q", line, blocks[1], want)
		}
	}
}
