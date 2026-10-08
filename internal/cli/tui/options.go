package tui

import (
	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/mcp"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/shellguard"
	"github.com/smallnest/pigo/internal/tooldecl"
)

// Options carries the resolved run configuration into Run. It deliberately
// mirrors repl.Options (see internal/cli/repl/interactive.go) field-for-field so
// cmd/pigo's dispatch can map the same assembled environment to either path, and
// so downstream nodes can port the REPL's session/live/slash/trust wiring into
// the TUI without reshaping the entry seam. This skeleton node does not yet
// consume most fields — they are here to lock the contract.
type Options struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	APIKey       string
	Protocol     string
	// Version is the running build version (main.version), shown in the startup
	// banner. Empty or "dev"/"unknown" renders as-is with no update hint.
	Version string
	// ThinkingLevel is the resolved reasoning-effort level (US-023): it seeds the
	// live run config so every turn requests it, until a control command changes
	// it.
	ThinkingLevel agentcore.ThinkingLevel
	Tools         []agentcore.AgentTool
	SysPrompt     string

	// ResumeID, when non-empty, resumes an existing session: its messages seed
	// the context and replayed transcript. Otherwise a fresh session is created.
	ResumeID string

	// Approve, when true, grants the launch directory session trust before the
	// run so the first-launch trust prompt is skipped and side-effect tools run
	// without per-call confirmation (mirrors pi's --approve/-a).
	Approve bool
	// Shellguard is the resolved bash-command static-analysis mode (T2.1).
	// Off (the default) never installs the seam. The TUI has no per-call
	// confirmation prompt, so ask and strict both deny flagged commands
	// outright (fail closed; registered in the spec's deviation log).
	Shellguard shellguard.Mode
	// Skills is the pre-loaded skill set (loaded once by run.SetupEnv, shared with
	// prompt injection). Each is registered as a /skill-name command. Empty under
	// --no-skills, so nothing is registered.
	Skills []*runtime.Skill

	// Plugins holds the loaded plugin manager so the TUI can deliver lifecycle
	// events to subscribed plugins (US-017, #133). It may be nil (no plugins).
	Plugins *plugin.Manager

	// MCP is the live MCP manager (T6.8/T6.9, run.Env.MCP). It backs the /mcp
	// surface commands and the status MCP section; nil when tools are disabled
	// or no server is configured.
	MCP *mcp.Manager

	// ToolPlan is the run's deferred tool declaration plan (T4.1), or nil for
	// direct declaration. Passed through from run.SetupEnv's Env.ToolPlan.
	ToolPlan *tooldecl.Plan

	// ConfigPrompts holds prompt-template paths from the config.toml `prompts`
	// array (settings tier); each is a file or dir loaded non-recursively.
	ConfigPrompts []string
	// CliPrompts holds --prompt-template paths (CLI tier, repeatable).
	CliPrompts []string
	// NoPromptTemplates disables all prompt-template discovery (global, project,
	// settings, CLI); built-in slash commands are unaffected. Independent of
	// --no-skills.
	NoPromptTemplates bool

	// MaxContext is the user's [compaction] max_context config (env.MaxContext).
	// It lowers the resolved compaction window when set (config wins over the
	// model-derived default); see cli.ResolveContextWindow.
	MaxContext config.MaxContext

	// Models is the config's [models."<id>"] profile face (T7.3 实测反馈):
	// the /model switcher lists these ids and a switch rebuilds the provider
	// from the profile. Nil/empty keeps the preset-catalog fallback.
	Models map[string]config.ModelProfile
	// ContextWindow / MaxOutputTokens are the startup profile's explicit
	// overrides (0 = derive from the provider catalog, as before).
	ContextWindow   int
	MaxOutputTokens int

	// Permissions is the user's [permissions] rules table (T5.2), loaded
	// into the permission engine alongside the persisted permissions file.
	Permissions config.PermissionsConfig
}
