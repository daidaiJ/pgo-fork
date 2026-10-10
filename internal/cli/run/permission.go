// permission.go assembles the toolrules permission engine (T5.2) at the
// drivers' wiring point: effect table from the assembled tool set, rules
// from the [permissions] config table plus the persisted permissions file,
// the self-edit surface over pigo's own configuration paths, and the
// driver-supplied ask channel + trust manager.
package run

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/toolrules"
)

// BuildPermissionEngine wires the permission engine for a run. ask and
// trusted are the driver-injected seams (nil ask = fail closed at the ask
// step, headless/TUI-without-channel semantics; nil trusted = never
// directory-trusted). mode reads the session's approval posture (T7.6);
// nil = ask semantics unchanged.
//
// Config rules with invalid fields are a hard error — a typo in a boundary
// the user believes is in force is the worst outcome (ToolPolicyError
// reasoning). A corrupted permissions FILE is surfaced by LoadStore the same
// way; a missing file is not an error.
func BuildPermissionEngine(cwd string, tools []agentcore.AgentTool, perms config.PermissionsConfig, ask toolrules.AskPort, trusted toolrules.TrustedFunc, mode func() toolrules.ApprovalMode) (*toolrules.Engine, error) {
	store, err := toolrules.LoadStore(toolrules.DefaultPath())
	if err != nil {
		return nil, err
	}
	sessionRules, err := parseConfigRules(perms)
	if err != nil {
		return nil, err
	}
	// Self-edit surface: pigo's own boundary files. A write reaching any of
	// them always escalates to the ask channel regardless of rules or trust.
	surface := toolrules.NewSelfEditSurface(
		trustFilePath(),
		toolrules.DefaultPath(),
		config.FileConfigPath(),
	)
	engine, err := toolrules.NewEngine(toolrules.EngineConfig{
		Cwd:          cwd,
		Effects:      toolrules.EffectTable(tools),
		Store:        store,
		SessionRules: sessionRules,
		Surface:      surface,
		Trusted:      trusted,
		Ask:          ask,
		Mode:         mode,
	})
	if err != nil {
		return nil, err
	}
	return engine, nil
}

// parseConfigRules normalizes the [permissions] rules table into session
// rules (they ride in-memory; the persisted tier is the file's).
func parseConfigRules(perms config.PermissionsConfig) ([]toolrules.Rule, error) {
	if len(perms.Rules) == 0 {
		return nil, nil
	}
	out := make([]toolrules.Rule, 0, len(perms.Rules))
	for i, r := range perms.Rules {
		norm, err := toolrules.NormalizeRule(toolrules.Rule{
			Tool:    r.Tool,
			Pattern: r.Pattern,
			Action:  toolrules.Action(r.Action),
			Scope:   toolrules.ScopePersisted,
		})
		if err != nil {
			return nil, fmt.Errorf("permissions.rules[%d]: %w", i, err)
		}
		out = append(out, norm)
	}
	return out, nil
}

// trustFilePath mirrors trust.DefaultPath for the self-edit surface without
// importing the trust package's manager (only its path convention).
func trustFilePath() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return filepath.Join(dir, "trust.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo", "trust.json")
}
