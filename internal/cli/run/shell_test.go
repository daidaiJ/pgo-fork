// Shell resolution tests (T8.4): the global-only override chain — config
// [shell] backend > PIGO_SHELL env > platform auto-detect (zero spec) — and
// the SetBashShell injection seam.
package run

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/config"
)

func TestResolveShellSettingsDefaultDetect(t *testing.T) {
	spec, err := ResolveShellSettings(config.ShellConfig{}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("ResolveShellSettings(zero): %v", err)
	}
	if spec.Program != "" || spec.Kind != "" {
		t.Fatalf("zero config = %+v, want the auto-detect zero spec", spec)
	}
}

func TestResolveShellSettingsConfigWins(t *testing.T) {
	spec, err := ResolveShellSettings(config.ShellConfig{Backend: "pwsh"}, func(string) string { return "cmd" })
	if err != nil {
		t.Fatalf("ResolveShellSettings(pwsh): %v", err)
	}
	if spec.Kind != agenttool.ShellKindPwsh || spec.Program != "pwsh" {
		t.Fatalf("config backend = %+v, want pwsh", spec)
	}
	// "auto" in config means the config layer is unset: the env fills the
	// gap (chain semantics), and with no env the zero spec = detect.
	spec, err = ResolveShellSettings(config.ShellConfig{Backend: "auto"}, func(string) string { return "cmd" })
	if err != nil || spec.Kind != agenttool.ShellKindCmd {
		t.Fatalf("auto backend with env = %+v, %v; want cmd (env fills the gap)", spec, err)
	}
	spec, err = ResolveShellSettings(config.ShellConfig{Backend: "auto"}, func(string) string { return "" })
	if err != nil || spec.Program != "" {
		t.Fatalf("auto backend without env = %+v, %v; want the zero spec", spec, err)
	}
}

func TestResolveShellSettingsEnvFillsTheGap(t *testing.T) {
	spec, err := ResolveShellSettings(config.ShellConfig{}, func(name string) string {
		if name == "PIGO_SHELL" {
			return "wsl"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("ResolveShellSettings(env): %v", err)
	}
	if spec.Kind != agenttool.ShellKindWSL {
		t.Fatalf("env backend = %+v, want wsl", spec)
	}
}

func TestResolveShellSettingsUnknownIsAnError(t *testing.T) {
	if _, err := ResolveShellSettings(config.ShellConfig{Backend: "nushell"}, func(string) string { return "" }); err == nil {
		t.Fatal("unknown config backend = nil error, want a startup failure")
	}
	if _, err := ResolveShellSettings(config.ShellConfig{}, func(string) string { return "fish" }); err == nil {
		t.Fatal("unknown PIGO_SHELL = nil error, want a startup failure")
	}
	if _, err := ResolveShellSettings(config.ShellConfig{Backend: "custom"}, func(string) string { return "" }); err == nil {
		t.Fatal("custom without command = nil error, want a startup failure")
	}
}

func TestResolveShellSettingsCustom(t *testing.T) {
	spec, err := ResolveShellSettings(config.ShellConfig{Backend: "custom", Command: "nushell", Args: []string{"-c"}}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("ResolveShellSettings(custom): %v", err)
	}
	if spec.Program != "nushell" || len(spec.Args) != 1 || spec.Kind != agenttool.ShellKindCustom {
		t.Fatalf("custom spec = %+v, want nushell [-c] custom", spec)
	}
}

func TestSetBashShellInjection(t *testing.T) {
	tools := []agentcore.AgentTool{&agenttool.ReadTool{}}
	if SetBashShell(tools, agenttool.ShellSpec{Program: "pwsh", Kind: agenttool.ShellKindPwsh}) {
		t.Fatal("SetBashShell reported a hit without a bash tool")
	}
	bash := &agenttool.BashTool{}
	tools = append(tools, bash)
	if !SetBashShell(tools, agenttool.ShellSpec{Program: "pwsh", Args: []string{"-Command"}, Kind: agenttool.ShellKindPwsh}) {
		t.Fatal("SetBashShell missed the bash tool")
	}
	if bash.ShellKind() != agenttool.ShellKindPwsh {
		t.Fatalf("bash kind after injection = %q, want pwsh", bash.ShellKind())
	}
	// An empty spec leaves the tool untouched (auto-detect stays).
	if !SetBashShell(tools, agenttool.ShellSpec{}) || bash.ShellKind() != agenttool.ShellKindPwsh {
		t.Fatal("an empty spec must not reset the injected backend")
	}
	if BashToolFrom(tools) != bash {
		t.Fatal("BashToolFrom missed the bash tool")
	}
}
