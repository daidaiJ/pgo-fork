// Shell resolution (T8.4): the override chain for the bash tool's backend —
//
//	config.toml [shell] backend > PIGO_SHELL env > platform auto-detect
//
// Global layer only (T8.4 user ruling): the project layer carries no shell
// key, so a checked-out repo cannot pick the interpreter commands run under.
package run

import (
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/config"
)

// ResolveShellSettings merges the shell layers into one backend spec. The
// global [shell] table wins; PIGO_SHELL overrides when the table is unset;
// the zero spec (empty program) means platform auto-detect, resolved per
// call by the bash tool's cascade (bash first, the T8.4 ruling). An unknown
// backend name — in config or in PIGO_SHELL — is an error: a misconfigured
// interpreter must fail loudly at startup, not silently fall back.
func ResolveShellSettings(cfg config.ShellConfig, getenv func(string) string) (agenttool.ShellSpec, error) {
	if b := strings.TrimSpace(cfg.Backend); b != "" && !strings.EqualFold(b, "auto") {
		if strings.EqualFold(b, agenttool.ShellKindCustom) {
			if strings.TrimSpace(cfg.Command) == "" {
				return agenttool.ShellSpec{}, fmt.Errorf(`[shell] backend = "custom" needs a command (the program to run)`)
			}
			return agenttool.ShellSpec{Program: cfg.Command, Args: cfg.Args, Kind: agenttool.ShellKindCustom}, nil
		}
		spec, err := agenttool.ShellSpecFor(b)
		if err != nil {
			return agenttool.ShellSpec{}, fmt.Errorf("[shell] backend: %w", err)
		}
		return spec, nil
	}
	if env := strings.TrimSpace(getenv("PIGO_SHELL")); env != "" {
		spec, err := agenttool.ShellSpecFor(env)
		if err != nil {
			return agenttool.ShellSpec{}, fmt.Errorf("PIGO_SHELL=%q: %w", env, err)
		}
		return spec, nil
	}
	return agenttool.ShellSpec{}, nil
}

// BashToolFrom returns the run's live bash tool, or nil when tools are
// disabled. The /shell surface reads it for the listing and the hot switch.
func BashToolFrom(tools []agentcore.AgentTool) *agenttool.BashTool {
	for _, t := range tools {
		if bt, ok := t.(*agenttool.BashTool); ok {
			return bt
		}
	}
	return nil
}

// SetBashShell injects the resolved shell backend into the run's bash tool
// (drivers call it after SetupEnv, the SetAskPort pattern). It reports
// whether a bash tool was found; with an empty spec it is a no-op that still
// reports the probe result.
func SetBashShell(tools []agentcore.AgentTool, spec agenttool.ShellSpec) bool {
	bt := BashToolFrom(tools)
	if bt == nil {
		return false
	}
	if spec.Program != "" {
		bt.SetShellSpec(spec)
	}
	return true
}
