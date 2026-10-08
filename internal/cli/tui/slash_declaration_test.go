package tui

// Slice-2 pins for declaration-driven dispatch (T7.7, spec
// wiki/port/slash-command-surface.md): runSlash carries no per-name intercept
// list — each face the TUI projects dispatches from the command's declared
// Projection in the shared registry. These tests pin the faces the registry
// pins cannot cover: the /memory hook render, the /rebuild async projection's
// degraded notice, and the /resume //rename projections that replaced their
// intercepts.

import (
	"strings"
	"testing"
)

// TestMemoryRendersThroughExecutorHook: /memory is a contract command (Parse +
// Executor.Memory hook). The TUI hook is unconditional, so the report renders
// even without a session (empty store) — never an unavailable notice, never a
// silent no-op.
func TestMemoryRendersThroughExecutorHook(t *testing.T) {
	got, cmd := NewModel(Options{}).runSlash("/memory")
	gm := got.(Model)
	if cmd != nil {
		t.Error("/memory dispatch returned a command; the render is synchronous")
	}
	if gm.running {
		t.Error("/memory must not start a run")
	}
	blocks := blockTexts(gm.transcript)
	if len(blocks) != 2 {
		t.Fatalf("/memory: want echo + report, got %q", blocks)
	}
	if !strings.Contains(blocks[1], "persistent memory:") {
		t.Errorf("/memory should render the report through the hook, got %q", blocks[1])
	}
}

// TestMemoryArgumentsRejected pins the Parse face: the report takes no
// arguments (the former intercept silently ignored them).
func TestMemoryArgumentsRejected(t *testing.T) {
	got, _ := NewModel(Options{}).runSlash("/memory focus")
	blocks := blockTexts(got.(Model).transcript)
	if len(blocks) != 2 || !strings.Contains(blocks[1], "memory: takes no arguments") {
		t.Errorf("/memory focus: want the explicit rejection, got %q", blocks)
	}
}

// TestRebuildProjectsFromDeclaration: /rebuild declares the TUI's async
// projection; session-less it degrades to the explicit notice through the
// projection. The registry path never sees the bare form, so a missing
// declaration could not hide behind an executor-hook fallback.
func TestRebuildProjectsFromDeclaration(t *testing.T) {
	got, _ := NewModel(Options{}).runSlash("/rebuild")
	blocks := blockTexts(got.(Model).transcript)
	if len(blocks) != 2 || blocks[1] != "(rebuild unavailable: no active session)" {
		t.Errorf("/rebuild: want echo + no-session notice, got %q", blocks)
	}
}

// TestRebuildArgumentsRejected pins the Parse face (the argument form resolves
// through the registry and is refused there).
func TestRebuildArgumentsRejected(t *testing.T) {
	got, _ := NewModel(Options{}).runSlash("/rebuild now")
	blocks := blockTexts(got.(Model).transcript)
	if len(blocks) != 2 || !strings.Contains(blocks[1], "rebuild: takes no arguments") {
		t.Errorf("/rebuild now: want the explicit rejection, got %q", blocks)
	}
}

// TestResumeSharesSessionsPickerProjection: /resume is a declaration entry
// carrying the same ProjSessionsPicker face — the alias needs no intercept and
// no second implementation.
func TestResumeSharesSessionsPickerProjection(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	got, _ := NewModel(Options{}).withSession(s, nil).runSlash("/resume")
	if !got.(Model).sessionsP.open {
		t.Fatal("/resume did not open the sessions picker")
	}
}

// TestRenameDispatchesFromDeclaration: /rename with an argument renames the
// session through the ProjRename projection (the intercept is gone).
func TestRenameDispatchesFromDeclaration(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	got, _ := NewModel(Options{}).withSession(s, nil).runSlash("/rename my title")
	gm := got.(Model)
	if gm.session.header.Title != "my title" || !gm.session.header.TitleIsManual {
		t.Errorf("/rename: header title = %q (manual=%v), want the argument applied",
			gm.session.header.Title, gm.session.header.TitleIsManual)
	}
	blocks := blockTexts(gm.transcript)
	// withSession reseeds the transcript (banner first), so the feedback is the
	// last block rather than a fixed index.
	last := blocks[len(blocks)-1]
	if !strings.Contains(last, "renamed session to") {
		t.Errorf("/rename: want the rename feedback as the last block, got %q", blocks)
	}
}
