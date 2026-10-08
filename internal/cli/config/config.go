// Package config implements pigo's optional user config file at
// ~/.config/pigo/config.toml (honoring $XDG_CONFIG_HOME when set) plus the
// provider-agnostic base-url env-var name derivation. Values in the file
// replace pigo's built-in defaults, but an explicit command-line flag always
// wins over the file:
//
//	command-line flag > config.toml > built-in default
//
// A missing file is not an error (defaults apply); a malformed file is surfaced
// to the caller so it can warn rather than silently ignore user intent.
//
// The package is intentionally free of any cliOptions/run-assembly concern: it
// only loads and decodes the file and derives env-var names. Overlaying a
// FileConfig onto the parsed CLI options lives in cmd/pigo, alongside the
// options struct it mutates.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileConfig is the on-disk shape of config.toml. Every field is optional; an
// absent (zero-value) field leaves the corresponding default/flag untouched.
// Keys are snake_case to read naturally in TOML.
type FileConfig struct {
	Model   string `toml:"model"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key"`
	// Credential is a named credential REFERENCE (issue #568): the literal key
	// lives in $PIGO_HOME/.credentials.yaml (mode 0600) under this name, and
	// config.toml carries only the name. It resolves to the API-key tier, below
	// an explicit --api-key flag. Empty means no reference.
	Credential    string `toml:"credential"`
	Protocol      string `toml:"protocol"`
	Provider      string `toml:"provider"`
	ThinkingLevel string `toml:"thinking_level"`
	OutputFormat  string `toml:"output_format"`
	NoTools       bool   `toml:"no_tools"`
	NoSkills      bool   `toml:"no_skills"`
	Approve       bool   `toml:"approve"`
	SystemPrompt  string `toml:"system_prompt"`
	// AllowedTools and DisallowedTools are the tool-level admission boundary:
	// the config-file tier of --allowed-tools / --disallowed-tools. Names match
	// case-insensitively and DisallowedTools wins when a name appears in both.
	// A CLI flag replaces the file value wholesale rather than merging with it,
	// so passing --allowed-tools can widen a boundary the file narrowed.
	AllowedTools    []string `toml:"allowed_tools"`
	DisallowedTools []string `toml:"disallowed_tools"`
	// Prompts is the config.toml `prompts` array: paths (files or dirs) to load
	// prompt templates from at the settings tier (mirrors pi's settings prompts).
	Prompts []string `toml:"prompts"`
	// Memory, Checkpoint, and Compaction are nested TOML tables for the
	// persistent-memory / infinite-context feature. They are pure config
	// plumbing here; defaults/parsing live in memory.go (Resolve* helpers) and
	// the overlay into runtime options lives in cmd/pigo. See
	// tasks/spec-persistent-memory-infinite-context.md §3/§4/§5.2.
	Memory     MemoryConfig     `toml:"memory"`
	Checkpoint CheckpointConfig `toml:"checkpoint"`
	Compaction CompactionConfig `toml:"compaction"`
	// Dream is the [dream] TOML table for the /dream memory-consolidation
	// feature. Pure config plumbing here; defaults/normalization live in
	// internal/dream (Config). See tasks/spec-dream-memory-consolidation.md
	// §3.3.
	Dream DreamConfig `toml:"dream"`
	// Shellguard is the [shellguard] TOML table for the bash-command static
	// safety analysis (T2.1). Pure config plumbing; mode resolution
	// (flag > file > default "off") lives in cmd/pigo's applyFileConfig.
	// shellguard is an opt-in advanced feature: the default is off.
	Shellguard ShellguardConfig `toml:"shellguard"`
	// Tools is the [tools] TOML table for the deferred tool declaration (T4.1).
	// Pure config plumbing; plan assembly + the capability gate live in
	// internal/cli/run (SetupEnv).
	Tools ToolsConfig `toml:"tools"`
	// Permissions is the [permissions] TOML table (T5.2): user-authored
	// call-level rules (allow/deny per tool + pattern). Pure config plumbing;
	// engine assembly lives in internal/cli/run (BuildPermissionEngine).
	Permissions PermissionsConfig `toml:"permissions"`
	// MCP is the [[mcp.servers]] TOML table (T6.8): MCP servers pigo launches
	// and speaks the Model Context Protocol with over stdio. Pure config
	// plumbing; connection and tool adaptation live in internal/mcp, and the
	// assembly into the tool face lives in internal/cli/run (SetupEnv).
	MCP MCPConfig `toml:"mcp"`
	// Skills is the [skills] TOML table (T6.9): the skill-face switch. Pure
	// config plumbing; the filtering of the loaded skill set lives in
	// internal/cli/run (LoadSkills), and /skills writes it through
	// SetSkillsDisabled.
	Skills SkillsConfig `toml:"skills"`
	// Models is the [models."<id>"] profile table (T7.3 实测反馈): the model
	// switcher's face of record — grok 的 [model."<id>"] 对齐。Pure config
	// plumbing; startup resolution lives in cmd/pigo (applyModelProfile) and
	// the session switch in internal/cli/prompts (/model action).
	Models map[string]ModelProfile `toml:"models"`
}

// SkillsConfig is the [skills] TOML table (T6.9). Disabled names skills that
// stay on disk but leave every face — prompt ads, skill-as-tool
// materialization, and the /name slash command (the hidden-tier semantics
// applied to skills; spec slash-config-surface.md §2.2). Absent key or empty
// list means every discovered skill is active, which is the default a user
// expects.
type SkillsConfig struct {
	Disabled []string `toml:"disabled"`
}

// SkillDisabled reports whether the named skill is switched off. Matching is
// case-insensitive and whitespace-trimmed, matching how [tools] name lists
// behave.
func (c SkillsConfig) SkillDisabled(name string) bool {
	for _, n := range c.Disabled {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return true
		}
	}
	return false
}

// ToolDeclarationMode values for ToolsConfig.DeclarationMode.
const (
	// ToolDeclarationDirect is the default: the full face is declared.
	ToolDeclarationDirect = "direct"
	// ToolDeclarationDeferred defers the external tool face; the model claims
	// tools via search_tools (T4.1).
	ToolDeclarationDeferred = "deferred"
)

// ToolsConfig is the [tools] TOML table for deferred tool declaration (T4.1).
//
//	declaration_mode "deferred" puts every external tool (plugin tools today,
//	the MCP face later) into the deferred tier: not declared to the model, but
//	discoverable and claimable via search_tools. The default "direct" keeps
//	today's full-declaration behavior; per-tool opt-in stays possible via the
//	deferred list even in direct mode.
//
// Lists match tool names case-insensitively; unknown names are ignored
// (admission stays ValidateToolPolicy's job — a deferred entry can never
// widen a tool-policy boundary).
type ToolsConfig struct {
	// DeclarationMode is "direct" (default) or "deferred".
	DeclarationMode string `toml:"declaration_mode"`
	// Deferred names tools that start in the deferred tier (in addition to
	// the whole external face under declaration_mode="deferred").
	Deferred []string `toml:"deferred"`
	// Direct exempts named tools from deferral (explicit per-tool override;
	// wins over Deferred).
	Direct []string `toml:"direct"`
	// Hidden hides named tools entirely: not declared, not discoverable
	// (connectivity preserved, e.g. a plugin whose tools should stay silent).
	Hidden []string `toml:"hidden"`
	// DeferredCapable force-enables the model capability bit for the deferred
	// protocol (BYOK/custom endpoints not in the capability catalog). Without
	// it (or a catalog bit) a deferred-mode run falls back to direct
	// declaration with an info log (spec §2.3: 按能力裁剪请求).
	DeferredCapable bool `toml:"deferred_capable"`
	// SubagentDefer defers the task sub-agent's tool face the same way: the
	// child's registry keeps its policy-filtered direct face and records the
	// rest of the parent plan as deferred (能力只减不增, spec §3.4).
	SubagentDefer bool `toml:"subagent_defer"`
}

// MCPServerConfig is one [[mcp.servers]] entry (T6.8). It is the user-facing
// shape of an MCP server; internal/mcp.ServerConfig is the runtime shape it is
// converted to (they are kept separate so the config package stays free of
// transport concerns).
//
// Tool naming: a server's tools register as mcp__<name>__<tool>, so Name may
// not contain '_' (validated in internal/mcp.ServerConfig.Validate).
//
// DisabledTools is the per-tool switch: a named tool stays out of the declared
// face while its connection is kept, so re-enabling never re-runs tools/list
// (grok's stash semantics, spec mcp-integration-shape.md §3.1).
type MCPServerConfig struct {
	Name string `toml:"name"`
	// Type selects the transport: "stdio" (default) launches Command and speaks
	// line-delimited JSON-RPC over its stdio; "http" speaks Streamable HTTP
	// against URL (one POST per message, Mcp-Session-Id session header).
	Type string `toml:"type"`
	// URL is the Streamable HTTP endpoint (type = "http").
	URL string `toml:"url"`
	// Command is the executable to launch (stdio transport).
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	Env     []string `toml:"env"`
	Dir     string   `toml:"dir"`
	// Enabled is a pointer so an absent key (nil) defaults to on, which is
	// what a user expects from a server they just described.
	Enabled        *bool    `toml:"enabled"`
	TimeoutSeconds int      `toml:"timeout_seconds"`
	MaxParallel    int      `toml:"max_parallel"`
	DisabledTools  []string `toml:"disabled_tools"`
}

// MCPConfig is the [[mcp.servers]] TOML table (T6.8). Empty means no MCP
// servers, which is the default: pigo ships without any.
type MCPConfig struct {
	Servers []MCPServerConfig `toml:"servers"`
}

// ShellguardConfig is the [shellguard] TOML table. Mode is one of "off",
// "ask", or "strict" (validated by shellguard.ParseMode); empty means unset
// so the flag tier wins.
type ShellguardConfig struct {
	Mode string `toml:"mode"`
}

// PermRule is one [[permissions.rules]] entry: a call-level boundary for a
// single tool. Pattern semantics are per-tool (toolrules.Rule): bash =
// word-boundary command prefix, write/edit = path prefix, other tools =
// the whole tool. Action "deny" is terminal — no trust grant overrides it.
type PermRule struct {
	Tool    string `toml:"tool"`
	Pattern string `toml:"pattern"`
	Action  string `toml:"action"`
}

// PermissionsConfig is the [permissions] TOML table (T5.2): user-authored
// rules loaded at startup into the permission engine, alongside rules
// settled interactively (the "save this rule" answer writes the same
// permissions file).
type PermissionsConfig struct {
	Rules []PermRule `toml:"rules"`
}

// DreamConfig is the [dream] TOML table for /dream memory consolidation.
// Enabled is a pointer so an absent key (nil) is distinguishable from an
// explicit false: nil is treated as true, only enabled = false disables
// auto-trigger. IntervalDays and RecentSessions use zero as "apply default"
// (7 and 20 respectively); normalization lives in dream.Config.
type DreamConfig struct {
	Enabled        *bool `toml:"enabled"`
	IntervalDays   int   `toml:"interval_days"`
	RecentSessions int   `toml:"recent_sessions"`
}

// FileConfigPath returns the path to the user config file:
// $XDG_CONFIG_HOME/pigo/config.toml, or ~/.config/pigo/config.toml by default.
// It returns "" when neither can be resolved, so the caller treats the file as
// absent.
func FileConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "pigo", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pigo", "config.toml")
}

// LoadFileConfig reads and decodes config.toml. A missing file (or an empty
// path) returns a zero config with no error; a malformed file is an error.
func LoadFileConfig(path string) (FileConfig, error) {
	if path == "" {
		return FileConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{}, nil
		}
		return FileConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg FileConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return FileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// GenericBaseURLEnvVar derives the generic base-url override env var name for a
// provider: the provider name uppercased with hyphens rewritten to underscores,
// suffixed with _BASE_URL. For example "zai-coding-cn" → "ZAI_CODING_CN_BASE_URL"
// and "deepseek" → "DEEPSEEK_BASE_URL". An empty provider name yields "".
//
// It lives here (not with ResolveBaseURL) because it is a pure name derivation
// with no dependency on the provider registry — the provider-agnostic part of
// base-url resolution. ResolveBaseURL itself lives in internal/provider.
func GenericBaseURLEnvVar(providerName string) string {
	n := strings.TrimSpace(providerName)
	if n == "" {
		return ""
	}
	n = strings.ReplaceAll(n, "-", "_")
	return strings.ToUpper(n) + "_BASE_URL"
}
