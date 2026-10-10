package prompts

import (
	"errors"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestParseShell pins the /shell grammar (T8.4): bare and "status" resolve to
// the listing, one argument switches to that backend (case-insensitive), and
// anything else is refused with the usage line (pure parse — no live state
// touched).
func TestParseShell(t *testing.T) {
	cases := []struct {
		args string
		want runtime.Intent
	}{
		{"", runtime.Intent{Kind: runtime.IntentShellShow}},
		{"status", runtime.Intent{Kind: runtime.IntentShellShow}},
		{"pwsh", runtime.Intent{Kind: runtime.IntentShellSwitch, ShellBackend: "pwsh"}},
		{"  WSL ", runtime.Intent{Kind: runtime.IntentShellSwitch, ShellBackend: "wsl"}},
	}
	for _, tc := range cases {
		got, err := parseShell(tc.args)
		if err != nil {
			t.Errorf("parseShell(%q): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseShell(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
	for _, bad := range []string{"nushell", "custom", "pwsh extra"} {
		if _, err := parseShell(bad); err == nil {
			t.Errorf("parseShell(%q) = nil error, want a usage refusal", bad)
		}
	}
}

// TestExecutorShellIntents pins the executor wiring: a nil surface degrades
// to the context note; the listing marks the live backend and names the
// switch line; the switch without a store refuses; a live switch persists
// through the store and applies to the tool.
func TestExecutorShellIntents(t *testing.T) {
	x := &Executor{}
	if msg := x.Execute(runtime.Intent{Kind: runtime.IntentShellShow}).Message; msg != "shell: config surface unavailable in this context" {
		t.Fatalf("nil surface show = %q", msg)
	}
	x = &Executor{Surface: &SurfaceDeps{}}
	if msg := x.Execute(runtime.Intent{Kind: runtime.IntentShellShow}).Message; !strings.Contains(msg, "bash tool unavailable") {
		t.Fatalf("no-tool listing = %q", msg)
	}
	if msg := x.Execute(runtime.Intent{Kind: runtime.IntentShellSwitch, ShellBackend: "pwsh"}).Message; !strings.Contains(msg, "bash tool unavailable") {
		t.Fatalf("no-tool switch = %q", msg)
	}
	// A live tool with no store refuses the persistence, not the listing.
	x = &Executor{Surface: &SurfaceDeps{Bash: &agenttool.BashTool{}}}
	if msg := x.Execute(runtime.Intent{Kind: runtime.IntentShellSwitch, ShellBackend: "pwsh"}).Message; !strings.Contains(msg, "config writes unavailable") {
		t.Fatalf("switch without store = %q", msg)
	}
}

// TestShellSurfaceSwitch pins the live-switch semantics: the tool's backend
// flips before the write lands, the message names the persistence target and
// the guard skip for non-bash backends, and an unknown backend refuses.
func TestShellSurfaceSwitch(t *testing.T) {
	bash := &agenttool.BashTool{}
	var stored string
	deps := &SurfaceDeps{
		Bash: bash,
		ShellStore: func(backend string) error {
			stored = backend
			return nil
		},
	}
	msg := deps.ShellSwitch("pwsh")
	if stored != "pwsh" {
		t.Fatalf("store got %q, want pwsh", stored)
	}
	if bash.ShellKind() != agenttool.ShellKindPwsh {
		t.Fatalf("live kind = %q, want pwsh", bash.ShellKind())
	}
	if !containsAll(msg, "switched to pwsh", "config.toml", "shellguard static analysis is skipped") {
		t.Fatalf("switch message = %q", msg)
	}
	// A bash switch carries no guard note.
	msg = deps.ShellSwitch("bash")
	if strings.Contains(msg, "shellguard static analysis is skipped") {
		t.Fatalf("bash switch message mentions the guard skip: %q", msg)
	}
	// A store failure keeps the live swap but says so.
	deps.ShellStore = func(string) error { return errors.New("disk full") }
	msg = deps.ShellSwitch("cmd")
	if !containsAll(msg, "for this session", "config write failed") || bash.ShellKind() != agenttool.ShellKindCmd {
		t.Fatalf("store-failure message = %q, kind = %q", msg, bash.ShellKind())
	}
}

// TestShellSurfaceList pins the listing: the current backend marked, the
// selectable family in order, and the custom backend labeled.
func TestShellSurfaceList(t *testing.T) {
	bash := &agenttool.BashTool{}
	deps := &SurfaceDeps{Bash: bash}
	msg := deps.shellList()
	if !containsAll(msg, "shell: bash (current)", "powershell — powershell -Command", "wsl — wsl.exe --exec bash -c", "switch: /shell <backend>") {
		t.Fatalf("listing = %q", msg)
	}
	bash.SetShellSpec(agenttool.ShellSpec{Program: "nushell", Kind: agenttool.ShellKindCustom})
	if !containsAll(deps.shellList(), "custom (current", "config.toml [shell]") {
		t.Fatalf("custom listing = %q", deps.shellList())
	}
}
