// Tests for the REPL's declaration-driven slash dispatch and its degraded
// projections (T7.7 slice 3, spec wiki/port/slash-command-surface.md §4.5/§6):
// the REPL dispatches from the command's declared Projection (no per-name
// intercept chain), runs the loop-owned faces for real, and projects a
// TUI-face picker/overlay as a candidate list plus a usage hint — never a fake
// picker, never a silent no-op.
package repl

import (
	"bytes"
	"strings"
	"testing"
)

// TestREPLSessionsProjectsCandidatesAndUsage pins the /sessions //resume
// projection: the saved sessions are listed (candidate list), followed by the
// usage line and the explicit availability notice, and no run starts.
func TestREPLSessionsProjectsCandidatesAndUsage(t *testing.T) {
	for _, line := range []string{"/sessions", "/resume"} {
		p := &replProvider{reply: "hi"}
		deps, _ := newTestDeps(t, p)
		seedTurn(t, &deps, "first question")

		var out bytes.Buffer
		if err := runREPL(strings.NewReader(line+"\n/exit\n"), &out, deps); err != nil {
			t.Fatalf("%s: runREPL: %v", line, err)
		}
		got := out.String()
		for _, want := range []string{
			"saved sessions (resume with: pigo --resume <id>)",
			deps.header.ID,
			"(current)",
			"usage: " + line,
			"(" + strings.TrimPrefix(line, "/") + " unavailable: TUI-face command)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: projection missing %q:\n%s", line, want, got)
			}
		}
		if p.calls != 0 {
			t.Errorf("%s must not launch a run, got %d calls", line, p.calls)
		}
	}
}

// TestREPLRenameProjectsUsageNotExecution pins the /rename projection: the REPL
// cannot run the TUI rename (terminal title + picker state), so it prints the
// usage hint and the notice, and the session title on disk stays untouched.
func TestREPLRenameProjectsUsageNotExecution(t *testing.T) {
	deps, store := newTestDeps(t, &replProvider{reply: "hi"})
	seedTurn(t, &deps, "hello")

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/rename my title\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	got := out.String()
	for _, want := range []string{"usage: /rename <title|--auto>", "(rename unavailable: TUI-face command)"} {
		if !strings.Contains(got, want) {
			t.Errorf("/rename projection missing %q:\n%s", want, got)
		}
	}
	h, _, err := store.Load(deps.header.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if h.Title != "" {
		t.Errorf("/rename must not execute in the REPL, title = %q", h.Title)
	}
}

// TestREPLContextProjectsUsage pins the third TUI-face projection.
func TestREPLContextProjectsUsage(t *testing.T) {
	deps, _ := newTestDeps(t, &replProvider{reply: "hi"})
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/context\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	got := out.String()
	for _, want := range []string{"usage: /context", "(context unavailable: TUI-face command)"} {
		if !strings.Contains(got, want) {
			t.Errorf("/context projection missing %q:\n%s", want, got)
		}
	}
}

// TestREPLSlashDispatchNameBoundary pins that dispatch resolves the exact
// command name: "/exporter" is not "/export" (the old space-prefix guards are
// gone; the name split is the single boundary).
func TestREPLSlashDispatchNameBoundary(t *testing.T) {
	deps, _ := newTestDeps(t, &replProvider{reply: "hi"})
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/exporter\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `unknown command "/exporter"`) {
		t.Errorf("/exporter should be an unknown command, got:\n%s", got)
	}
}
