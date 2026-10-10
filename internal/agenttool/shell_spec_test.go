// Shell backend spec + alias tests (T8.4): the named-backend table, the
// guardability rule, the hot-swap seam and the "shell" alias's delegation.
package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestShellSpecFor(t *testing.T) {
	for _, name := range []string{"bash", "powershell", "pwsh", "cmd", "wsl"} {
		spec, err := ShellSpecFor(name)
		if err != nil {
			t.Fatalf("ShellSpecFor(%q): %v", name, err)
		}
		if spec.Kind != name || spec.Program == "" || len(spec.Args) == 0 {
			t.Fatalf("ShellSpecFor(%q) = %+v, want kind/program/args populated", name, spec)
		}
	}
	// Case-insensitive + trimmed.
	if spec, err := ShellSpecFor("  PWSH "); err != nil || spec.Kind != ShellKindPwsh {
		t.Fatalf("ShellSpecFor case-insensitive = %+v, %v", spec, err)
	}
	// custom is config-only; unknown names are a usage error.
	if _, err := ShellSpecFor("custom"); err == nil {
		t.Fatal("ShellSpecFor(custom) = nil error, want the config-only refusal")
	}
	if _, err := ShellSpecFor("nushell"); err == nil {
		t.Fatal("ShellSpecFor(unknown) = nil error, want a usage refusal")
	}
}

func TestShellBackendNamesOrder(t *testing.T) {
	names := ShellBackendNames()
	if len(names) == 0 || names[0] != "bash" {
		t.Fatalf("ShellBackendNames()[0] = %q, want bash first (the T8.4 cascade ruling)", names[0])
	}
}

func TestGuardableShellKind(t *testing.T) {
	for _, k := range []string{"", ShellKindBash, ShellKindWSL} {
		if !GuardableShellKind(k) {
			t.Errorf("GuardableShellKind(%q) = false, want true (bash syntax)", k)
		}
	}
	for _, k := range []string{ShellKindPowerShell, ShellKindPwsh, ShellKindCmd, ShellKindCustom} {
		if GuardableShellKind(k) {
			t.Errorf("GuardableShellKind(%q) = true, want false (T8.4 skip ruling)", k)
		}
	}
}

func TestBashToolShellKindAndHotSwap(t *testing.T) {
	tool := &BashTool{}
	if got := tool.ShellKind(); got != ShellKindBash {
		t.Fatalf("ShellKind(auto-detect) = %q, want bash (cascade first choice)", got)
	}
	tool.SetShellSpec(ShellSpec{Program: "powershell", Args: []string{"-Command"}, Kind: ShellKindPowerShell})
	if got := tool.ShellKind(); got != ShellKindPowerShell {
		t.Fatalf("ShellKind after SetShellSpec = %q, want powershell", got)
	}
	if tool.Shell != "powershell" || len(tool.ShellArgs) != 1 || tool.ShellArgs[0] != "-Command" {
		t.Fatalf("SetShellSpec did not swap the live fields: %q %v", tool.Shell, tool.ShellArgs)
	}
	// Reset to auto-detect.
	tool.SetShellSpec(ShellSpec{})
	if got := tool.ShellKind(); got != ShellKindBash {
		t.Fatalf("ShellKind after reset = %q, want bash", got)
	}
}

func TestBashToolInferShellKind(t *testing.T) {
	cases := map[string]string{
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: ShellKindPowerShell,
		"/usr/bin/pwsh":     ShellKindPwsh,
		"cmd":               ShellKindCmd,
		"wsl.exe":           ShellKindWSL,
		"/usr/bin/bash":     ShellKindBash,
		"/opt/bin/myinterp": ShellKindBash, // unknown = POSIX "-c" family
	}
	for program, want := range cases {
		if got := inferShellKind(program); got != want {
			t.Errorf("inferShellKind(%q) = %q, want %q", program, got, want)
		}
	}
}

func TestShellAliasDelegates(t *testing.T) {
	bash := &BashTool{Shell: "bash"}
	alias := &ShellAliasTool{Bash: bash}
	if alias.Name() != "shell" {
		t.Fatalf("alias name = %q, want shell", alias.Name())
	}
	if alias.Schema() == nil || !strings.Contains(string(alias.Schema()), `"command"`) {
		t.Fatal("alias schema does not match the bash schema")
	}
	if alias.Effect() != bash.Effect() {
		t.Fatal("alias effect differs from bash")
	}
	res, err := alias.Execute(context.Background(), "call_1", json.RawMessage(`{"command":"echo aliased"}`), nil)
	if err != nil {
		t.Fatalf("alias execute: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("alias result carries no content")
	}
	if tc, ok := res.Content[0].(agentcore.TextContent); !ok || !strings.Contains(tc.Text, "aliased") {
		t.Fatalf("alias result = %+v, want the command output", res)
	}
	// The hot switch reaches calls through the alias too.
	bash.SetShellSpec(ShellSpec{Program: "pwsh", Args: []string{"-Command"}, Kind: ShellKindPwsh})
	if alias.Bash.ShellKind() != ShellKindPwsh {
		t.Fatal("the alias does not observe the live backend swap")
	}
}

func TestShellKindFromTools(t *testing.T) {
	bash := &BashTool{}
	tools := []agentcore.AgentTool{bash, &ShellAliasTool{Bash: bash}}
	kind := ShellKindFromTools(tools)
	if kind == nil {
		t.Fatal("ShellKindFromTools = nil with a bash tool present")
	}
	if kind() != ShellKindBash {
		t.Fatalf("kind() = %q, want bash", kind())
	}
	if ShellKindFromTools([]agentcore.AgentTool{&ShellAliasTool{Bash: bash}}) == nil {
		t.Fatal("the alias alone must still locate the bash tool")
	}
	if ShellKindFromTools(nil) != nil {
		t.Fatal("ShellKindFromTools(nil) != nil")
	}
}
