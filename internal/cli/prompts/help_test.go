// Tests for the shared catalog row renderer (T7.7 slice 3): /help, the REPL
// completion hint and the TUI menu all render from FormatCommandLine, so the
// three candidate surfaces list the same rows with the same source vocabulary
// ("[skill]"/"[plugin]"/"[template]"; builtins stay untagged) — spec
// wiki/port/slash-command-surface.md §6.
package prompts

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
)

func TestFormatCommandLine(t *testing.T) {
	cases := []struct {
		name string
		cmd  runtime.SlashCommand
		want string
	}{
		{"hint+desc skill", runtime.SlashCommand{Name: "review", ArgumentHint: "<PR-URL>", Description: "Review PRs", Source: runtime.SourceSkill}, "/review <PR-URL>  [skill] - Review PRs"},
		{"desc only builtin", runtime.SlashCommand{Name: "help", Description: "list commands", Source: runtime.SourceBuiltin}, "/help - list commands"},
		{"hint only plugin", runtime.SlashCommand{Name: "wr", ArgumentHint: "[instructions]", Source: runtime.SourcePlugin}, "/wr [instructions]  [plugin]"},
		{"neither template", runtime.SlashCommand{Name: "deploy", Source: runtime.SourceUser}, "/deploy  [template]"},
		{"bare", runtime.SlashCommand{Name: "exit"}, "/exit"},
	}
	for _, c := range cases {
		if got := FormatCommandLine(c.cmd); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestHelpActionListsSourcesWithBadges pins the /help face: a prompt template
// shows its argument hint, description and source badge, and a built-in row
// carries no badge (the untagged majority). The old "(source: <tier>)" tail is
// gone — the badge is the shared source vocabulary.
func TestHelpActionListsSourcesWithBadges(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, &cli.LiveConfig{Model: "test", ProviderName: "test"}, nil)
	reg.AddUser(runtime.SlashCommand{
		Name:         "review",
		ArgumentHint: "<PR-URL>",
		Description:  "Review PRs",
		Expand:       func(string) string { return "" },
	})

	out, err := reg.ResolveOutcome("/help")
	if err != nil {
		t.Fatalf("ResolveOutcome /help: %v", err)
	}
	if !out.Handled || out.Kind != runtime.SlashAction {
		t.Fatalf("/help should be a handled action, got handled=%v kind=%v", out.Handled, out.Kind)
	}
	for _, want := range []string{"/review <PR-URL>", "[template]", "Review PRs"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("/help output missing %q:\n%s", want, out.Message)
		}
	}
	if strings.Contains(out.Message, "(source:") {
		t.Errorf("/help should carry the source badge, not the old tier tail:\n%s", out.Message)
	}
	// The built-in row for /model must not be badged.
	if strings.Contains(out.Message, "/model [model-id]") && strings.Contains(out.Message, "/model [model-id]  [") {
		t.Errorf("built-in /model row should carry no source badge:\n%s", out.Message)
	}
}
