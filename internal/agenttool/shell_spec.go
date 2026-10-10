// Shell backend specs (T8.4): the named-backend table the config layer, the
// /shell switch surface and the shellguard guardability rule all share. The
// default selection stays bash-first (T8.4 user ruling: the model ecosystem
// teaches bash syntax; only config/PIGO_SHELL moves off it).
package agenttool

import (
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// Shell backend kinds. bash and wsl run bash syntax, so shellguard's static
// analysis stays valid; powershell/pwsh/cmd/custom do not.
const (
	ShellKindBash       = "bash"
	ShellKindPowerShell = "powershell"
	ShellKindPwsh       = "pwsh"
	ShellKindCmd        = "cmd"
	ShellKindWSL        = "wsl"
	ShellKindCustom     = "custom"
)

// ShellSpec is one resolved shell backend: the program to exec, the argument
// prefix that makes it read the command line from the next argument, and the
// backend kind the guard and the listing surfaces reason about. The zero
// value means "auto-detect at call time" (resolveShell's platform cascade).
type ShellSpec struct {
	Program string
	Args    []string
	Kind    string
}

// shellBackendSpecs is the named-backend table in panel order.
var shellBackendSpecs = []ShellSpec{
	{Program: "bash", Args: []string{"-c"}, Kind: ShellKindBash},
	{Program: "powershell", Args: []string{"-Command"}, Kind: ShellKindPowerShell},
	{Program: "pwsh", Args: []string{"-Command"}, Kind: ShellKindPwsh},
	{Program: "cmd", Args: []string{"/C"}, Kind: ShellKindCmd},
	{Program: "wsl.exe", Args: []string{"--exec", "bash", "-c"}, Kind: ShellKindWSL},
}

// ShellBackendNames lists the selectable backends in panel order.
func ShellBackendNames() []string {
	names := make([]string, 0, len(shellBackendSpecs))
	for _, s := range shellBackendSpecs {
		names = append(names, s.Kind)
	}
	return names
}

// ShellSpecFor resolves a named backend. "custom" is config-only (it needs
// the [shell] command/args pair), so the switch surfaces refuse it here.
func ShellSpecFor(backend string) (ShellSpec, error) {
	b := strings.ToLower(strings.TrimSpace(backend))
	for _, s := range shellBackendSpecs {
		if b == s.Kind {
			return s, nil
		}
	}
	if b == ShellKindCustom {
		return ShellSpec{}, fmt.Errorf("the custom backend comes from config [shell] command/args only")
	}
	return ShellSpec{}, fmt.Errorf("unknown shell backend %q (want bash|powershell|pwsh|cmd|wsl)", backend)
}

// GuardableShellKind reports whether shellguard's bash-syntax static analysis
// applies to the backend kind (T8.4 user ruling, lenient): powershell/pwsh/
// cmd/custom skip the guard — the analyzer would misjudge their syntax —
// while bash and wsl (bash inside the distro) keep it. "" is the
// auto-detect label, whose first choice is bash.
func GuardableShellKind(kind string) bool {
	switch kind {
	case "", ShellKindBash, ShellKindWSL:
		return true
	default:
		return false
	}
}

// ShellKindFromTools returns a live kind closure over the run's bash tool,
// for the shellguard seams (nil when no bash tool is in the set — tools
// disabled — which the seams read as "analyze as bash"). The "shell" alias
// unwraps to its backing tool, so an alias-only set still resolves.
func ShellKindFromTools(tools []agentcore.AgentTool) func() string {
	for _, t := range tools {
		switch bt := t.(type) {
		case *BashTool:
			return bt.ShellKind
		case *ShellAliasTool:
			return bt.Bash.ShellKind
		}
	}
	return nil
}
