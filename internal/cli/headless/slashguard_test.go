// Tests for the print-mode built-in slash guard (T7.7 slice 3, spec
// wiki/port/slash-command-surface.md §4.6): -p has no loop, so a declared
// built-in command is refused with an explicit message instead of being sent
// to the model as literal prompt text.
package headless

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

// TestRefuseBuiltinSlash classifies the guard's inputs: built-ins with an
// interactive (projected) face point at the interactive front-ends; an
// executable-text built-in says it is a built-in; everything else stays on the
// prompt path.
func TestRefuseBuiltinSlash(t *testing.T) {
	cases := []struct {
		prompt  string
		wantHas string
	}{
		{"/sessions", "needs an interactive terminal"},
		{"/model gpt-5", "needs an interactive terminal"},
		{"/fork 2", "needs an interactive terminal"},
		{"/help", "is a built-in slash command"},
		{"/status", "is a built-in slash command"},
		{"hello world", ""},
		{"/nope", ""},
		{"/review this PR", ""},
		{"path/to/file", ""},
	}
	for _, c := range cases {
		got := refuseBuiltinSlash(c.prompt)
		if c.wantHas == "" {
			if got != "" {
				t.Errorf("refuseBuiltinSlash(%q) = %q, want no refusal", c.prompt, got)
			}
			continue
		}
		if !strings.Contains(got, c.wantHas) {
			t.Errorf("refuseBuiltinSlash(%q) = %q, want it to contain %q", c.prompt, got, c.wantHas)
		}
	}
}

// TestRunRefusesBuiltinSlash pins the run-level contract: exit code 2, the
// explicit message on stderr, and no output on stdout (the run never starts).
func TestRunRefusesBuiltinSlash(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(context.Background(), RunParams{
		Mode:   runtime.PrintMode,
		Prompt: "/sessions",
	}, &out, &errOut)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "/sessions needs an interactive terminal") {
		t.Errorf("stderr = %q, want the explicit refusal", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want no run output", out.String())
	}
}
