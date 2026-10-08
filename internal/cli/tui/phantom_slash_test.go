package tui

// Regression pin for the REPL-face slash commands (T7.7, spec
// wiki/port/slash-command-surface.md): these commands execute in the REPL loop
// (internal/cli/repl) and declare no TUI face — after slice 2 they are
// identity + ProjREPLFace declarations, and the TUI rejects them explicitly
// (the transcript echoes the line and shows the unavailability notice) instead
// of the former silent no-op (the stub registrations are gone). /compact left
// this table in slice 1 and /memory /rebuild are contract commands now — both
// execute for real, pinned by TestCompactCommandExecutes below and the
// declaration-dispatch tests in slash_declaration_test.go.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// phantomLines is one representative invocation per REPL-face command, in the
// form a user would type. replDoes notes what the REPL does for the same line —
// the face the TUI does not project. Derived from the declaration table in
// prompts/registry.go against runSlash's ProjREPLFace projection.
var phantomLines = []struct{ line, replDoes string }{
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

// TestPhantomCommandsAdvertised verifies the REPL-face commands stay listed:
// the completion menu shows all of them and /help prints every name. They are
// declaration entries now (slice 2 replaced the stub registrations), so the
// advertised surface is unchanged; what changed is the dispatch — see
// TestPhantomCommandsRejectExplicitly.
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

// TestPhantomCommandsRejectExplicitly pins the slice-2 flip: submitting one of
// the REPL-face commands echoes the line and shows the declared unavailability
// notice — no silent no-op, no run (T7.7 §6: execution or explicit rejection).
func TestPhantomCommandsRejectExplicitly(t *testing.T) {
	for _, p := range phantomLines {
		got, cmd := NewModel(Options{}).runSlash(p.line)
		gm := got.(Model)
		if cmd != nil {
			t.Errorf("%s: dispatch returned a command; the rejection should return nil (REPL: %s)", p.line, p.replDoes)
		}
		if gm.running {
			t.Errorf("%s: dispatch must not start a run (REPL: %s)", p.line, p.replDoes)
		}
		if gm.input.Value() != "" {
			t.Errorf("%s: input should be cleared after submit", p.line)
		}
		blocks := blockTexts(gm.transcript)
		want := "(" + strings.Fields(p.line)[0][1:] + " unavailable: REPL-face command — run pigo --no-tui)"
		if len(blocks) != 2 || blocks[1] != want {
			t.Errorf("%s: transcript should hold the echo + %q, got %q (REPL: %s)", p.line, want, blocks, p.replDoes)
		}
	}
}

// TestCompactCommandExecutes pins /compact's slice-1 behavior (T7.7): the
// former silent no-op is a contract command now. Session-less it refuses
// explicitly; with a session the intercept runs the compaction off the tea
// loop through the shared cli.RunManualCompact core — the empty context
// completes without an LLM call, so the "nothing to compact" note rides
// compactDoneMsg.
func TestCompactCommandExecutes(t *testing.T) {
	m := NewModel(Options{})
	got, _ := m.runSlash("/compact")
	gm := got.(Model)
	blocks := blockTexts(gm.transcript)
	if len(blocks) != 2 || blocks[1] != "(compact unavailable: no active session)" {
		t.Errorf("/compact without a session: blocks = %q, want the echo + notice", blocks)
	}
	if gm.running {
		t.Error("/compact without a session must not start a run")
	}

	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "compact-model", ProviderName: "compact-provider"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m = NewModel(Options{}).withSession(s, nil)
	got, cmd := m.runSlash("/compact")
	gm = got.(Model)
	if !gm.running {
		t.Error("/compact with a session should mark the model running while the stream works")
	}
	if cmd == nil {
		t.Fatal("/compact with a session should return the compaction Cmd")
	}
	// runSlash batches the compaction with the spinner tick: unwrap and pick
	// the compactDoneMsg carrier.
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if cm, isCompact := c().(compactDoneMsg); isCompact {
				msg = cm
				break
			}
		}
	}
	done, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want compactDoneMsg", msg)
	}
	if !strings.Contains(done.summary, "nothing to compact") {
		t.Errorf("summary = %q, want the nothing-to-compact note", done.summary)
	}
}

// TestInterceptedStubsStayReachable pins the reachability side of the same
// fold: /session and /rewind degrade to an explicit no-session notice instead
// of a silent dispatch, and /exit /quit quit (pinned by TestSlashExitQuits).
// /session left the intercept list in T7.7 slice 1 — the intent executor's
// Session hook renders the same notice through the registry path; /rewind now
// dispatches through its declared ProjRewind face (slice 2), which this pin
// guards.
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
