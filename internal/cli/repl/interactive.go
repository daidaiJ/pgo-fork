// This file wires the line-based REPL (US-003) and session persistence
// (US-024, #43) into the pigo command. When invoked without a prompt on a
// terminal, pigo starts the REPL loop (see repl.go); each run's messages are
// persisted to a local JSONL session so the conversation can be listed, resumed
// and replayed later.
package repl

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/dream"
	"github.com/smallnest/pigo/internal/lsp"
	"github.com/smallnest/pigo/internal/mcp"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/shellguard"
	"github.com/smallnest/pigo/internal/spans"
	"github.com/smallnest/pigo/internal/tooldecl"
	"github.com/smallnest/pigo/internal/toolrules"
	"github.com/smallnest/pigo/internal/trust"
)

// sessionStore returns the session store for the interactive REPL. It is a thin
// alias for headless.SessionStore so the REPL and headless runs share one store
// rooted at ~/.pigo/sessions (or PIGO_HOME).
func sessionStore() (*session.Store, error) {
	return headless.SessionStore()
}

// Options carries the resolved run configuration plus optional
// resume state into Run.
type Options struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	APIKey       string
	Protocol     string
	// ThinkingLevel is the resolved reasoning-effort level (US-023): it seeds the
	// live run config so every REPL turn requests it, until a control command
	// changes it.
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
	// Skills is the pre-loaded skill set (loaded once by setupAgentEnv, shared
	// with prompt injection). Each is registered as a /skill-name command. Empty
	// under --no-skills, so nothing is registered.
	Skills []*runtime.Skill

	// Plugins holds the loaded plugin manager so the REPL can deliver lifecycle
	// events to subscribed plugins (US-017, #133). It may be nil (no plugins).
	Plugins *plugin.Manager

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

	// Dream is the resolved [dream] configuration (US-008). Run uses it to decide
	// whether to launch the startup background consolidation; a zero value
	// (Enabled false) disables the auto-trigger entirely.
	Dream dream.Config

	// Shellguard is the resolved bash-command static-analysis mode (T2.1).
	// Off (the default) never installs the seam; ask consults the interactive
	// confirmation prompt; strict denies outright.
	Shellguard shellguard.Mode

	// MaxContext is the user's [compaction] max_context config (env.MaxContext).
	// It lowers the resolved compaction window when set (config wins over the
	// model-derived default); see cli.ResolveContextWindow.
	MaxContext config.MaxContext

	// Models is the config's [models."<id>"] profile face (T7.3 实测反馈):
	// the /model switcher lists these ids and a switch rebuilds the provider
	// from the profile. Nil/empty keeps the preset-catalog fallback.
	Models map[string]config.ModelProfile
	// ProviderConfigs is the config's [provider."<id>"] connection face
	// (T8.1) a profile's provider reference inherits from at switch time;
	// Proxy is the startup connection's egress proxy (reused by bare-model
	// switches). Nil/empty keep the defaults.
	ProviderConfigs map[string]config.ProviderSpec
	Proxy           string
	// ContextWindow / MaxOutputTokens are the startup profile's explicit
	// overrides (0 = derive from the provider catalog, as before).
	ContextWindow   int
	MaxOutputTokens int

	// Permissions is the user's [permissions] rules table (T5.2), loaded
	// into the permission engine alongside the persisted permissions file.
	Permissions config.PermissionsConfig

	// MCP is the live MCP manager (T6.8, run.Env.MCP). It backs the /mcp
	// surface commands and the status MCP section; nil when tools are
	// disabled or no server is configured.
	MCP *mcp.Manager
	// LSP is the workspace language server (T8.2, run.Env.LSP); nil when
	// LSP is disabled. It backs the /lsp surface command.
	LSP *lsp.Manager
}

// Run starts the line-based REPL over a persisted session. It keeps
// a single growing AgentContext across prompts (so turns share history) and
// saves the session's messages after each run completes (see runREPL/streamRun
// in repl.go).
func Run(opts Options) error {
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(opts.ProviderName, opts.APIKey)
	reg := run.ToolRegistry(opts.Tools)

	store, err := sessionStore()
	if err != nil {
		return err
	}

	// Resolve the launch directory once (pigo does not cd during a session). It is
	// the trust key, the directory side-effect tools are gated against, and — new
	// for #526 — the value stamped onto a fresh SessionHeader.Cwd so the session is
	// attributed to a project and a later /dream pass can distill it under the
	// right scope (mirrors headless.headlessCwd). An unresolvable cwd yields ""
	// (the session stays unattributed) rather than aborting the session.
	cwd, cwdErr := os.Getwd()

	// Establish the session: resume an existing one or create a fresh header.
	now := time.Now().UTC()
	var (
		agentCtx *agentcore.AgentContext
		header   session.SessionHeader
		history  []agentcore.AgentMessage
		curLeaf  string // active leaf id on resume; "" for a fresh session
	)
	if opts.ResumeID != "" {
		// Interactive resume always appends a fresh user message before running,
		// so a session that ended normally (trailing assistant reply) is resumable
		// here. Load the raw session and rebuild the context directly.
		// startup.session_load (T1.1): a large resumed session file is a prime
		// slow-startup suspect, so the load gets its own span.
		loadSpan := spans.Begin("startup.session_load")
		h, entries, err := store.LoadEntries(opts.ResumeID)
		loadSpan.End()
		if err != nil {
			return err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		if len(entries) > 0 {
			curLeaf = entries[len(entries)-1].ID
		}
		header = h
		agentCtx = &agentcore.AgentContext{SystemPrompt: h.SystemPrompt, Messages: msgs, Tools: opts.Tools}
		history = msgs
		if agentCtx.SystemPrompt == "" {
			agentCtx.SystemPrompt = opts.SysPrompt
		}
	} else {
		agentCtx = &agentcore.AgentContext{SystemPrompt: opts.SysPrompt, Tools: opts.Tools}
		header = session.SessionHeader{
			ID:           session.NewID(now),
			CreatedAt:    now,
			UpdatedAt:    now,
			Model:        opts.Model,
			Provider:     opts.ProviderName,
			SystemPrompt: opts.SysPrompt,
			Cwd:          cwd,
		}
	}

	// live holds the run configuration that a control command (e.g. /model) may
	// mutate mid-session. streamRun reads it on each prompt so a model switch
	// takes effect on the next turn; header is updated so the switch is persisted
	// with the session.
	live := &cli.LiveConfig{
		Model:           opts.Model,
		ProviderName:    opts.ProviderName,
		Provider:        opts.Provider,
		BaseURL:         opts.BaseURL,
		Protocol:        opts.Protocol,
		ThinkingLevel:   opts.ThinkingLevel,
		MaxContext:      opts.MaxContext,
		ModelProfiles:   opts.Models,
		ProviderConfigs: opts.ProviderConfigs,
		Proxy:           opts.Proxy,
		// The effective window follows the selected model's catalog window
		// (fallback DefaultContextWindow), lowered by an explicit
		// [compaction] max_context — re-derived on /model switches. The
		// output cap feeds the trigger line and the dynamic max_tokens stamp.
		// A startup config profile's explicit declarations win over both.
		ContextWindow:   cli.SeedContextWindow(opts.Provider, opts.Model, opts.MaxContext, opts.ContextWindow),
		MaxOutputTokens: cli.SeedMaxOutputTokens(opts.Provider, opts.Model, opts.MaxOutputTokens),
	}

	// Project trust (US-018, #134): load the persisted trust store for the
	// launch directory. A load failure (e.g. a corrupted trust.json) is
	// non-fatal: trust is disabled (mgr stays nil) and the REPL still runs -
	// the store is surfaced rather than silently overwritten. cwd is captured
	// once above since pigo does not cd during a session; if it cannot be resolved
	// trust is disabled too, since an empty cwd would silently never match.
	mgr, mgrErr := trust.NewManager(trust.DefaultPath())
	if mgrErr != nil {
		fmt.Fprintf(os.Stderr, "pigo: trust store unavailable, trust disabled: %v\n", mgrErr)
		mgr = nil
	}
	if cwdErr != nil && mgr != nil {
		fmt.Fprintf(os.Stderr, "pigo: cannot resolve working directory, trust disabled: %v\n", cwdErr)
		mgr = nil
	}
	// in is the shared input reader for the main loop and the tool-call
	// confirmation prompt (see repl.go). Wrapping os.Stdin once here means both
	// read from the same buffer.
	reader := bufio.NewReaderSize(os.Stdin, replScanBufInit)

	// ask_user port (T4.2): questionnaires render on stdout and read from the
	// shared reader under the same mutex as the trust/shellguard confirmations,
	// so all three prompts serialize on one stdin. The mutex is created here and
	// shared with replDeps.confirmMu.
	askMu := &sync.Mutex{}
	run.SetAskPort(opts.Tools, &stdinAskPort{out: os.Stdout, in: reader, mu: askMu})

	// Permission engine (T5.2): rules + side-effect contract + self-edit
	// guard, with the stdin ask channel (y/n/a/s) and the trust manager as
	// the trusted-directory fast path. A config or store error is fatal: a
	// boundary the user believes is in force must not silently vanish.
	var trustedFn toolrules.TrustedFunc
	if mgr != nil {
		trustedFn = mgr.IsTrusted
	}
	permEngine, engineErr := run.BuildPermissionEngine(cwd, opts.Tools, opts.Permissions,
		trust.StdinAskPort(os.Stdout, reader, askMu, mgr, cwd), trustedFn)
	if engineErr != nil {
		return fmt.Errorf("permission engine: %w", engineErr)
	}

	// Wire slash-commands: built-ins (compile-time) plus any user templates under
	// ~/.pigo/commands (mirrors the commands/*.md convention) plus skills under
	// ~/.agents/skills. A load error is non-fatal — the REPL still runs with the
	// built-ins. Instance built-ins that need live state (/model, /help) are
	// registered against `live`.
	slash, err := prompts.BuildSlashRegistry(live, creds, opts.Skills, opts.Plugins, prompts.PromptTemplateSources{
		Settings:       opts.ConfigPrompts,
		CLI:            opts.CliPrompts,
		Disable:        opts.NoPromptTemplates,
		ProjectDir:     filepath.Join(cwd, ".pigo", "prompts"),
		ProjectTrusted: mgr != nil && mgr.IsTrusted(cwd),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: slash-commands: %v\n", err)
	}
	trust.RegisterCommand(slash, mgr, cwd)

	// Config-surface commands (T6.9): /skills and /mcp project the config face
	// interactively — every toggle writes config.toml (persist.go) and mirrors
	// into live state; the commands are registered ONCE here so the REPL and
	// the TUI (which calls the same prompts wiring) expose identical surfaces.
	// skillsView aliases opts.Skills so /skills reload can swap the view
	// without reaching into run state the registry cannot see.
	skillsView := &opts.Skills
	surface := &prompts.SurfaceDeps{
		MCP: opts.MCP,
		LSP: opts.LSP,
		LSPStore: func(enabled bool) error {
			return run.SetProjectLSPEnabled(cwd, enabled)
		},
		LSPConfigured: func() bool {
			return run.LSPProjectConfigured(cwd)
		},
		Skills:     func() []*runtime.Skill { return *skillsView },
		SetSkills:  func(s []*runtime.Skill) { *skillsView = s },
		SkillsDir:  run.SkillsDir(),
		ConfigPath: config.FileConfigPath(),
	}
	prompts.RegisterSurfaceCommands(slash, surface)

	// --approve grants the launch directory session trust up front (mirrors pi's
	// --approve/-a), so the first-launch prompt is skipped and side-effect tools
	// run without per-call confirmation. Otherwise, on the first launch in an
	// undecided directory, ask the user how much to trust it before any tool
	// runs. This happens before replay so the trust question is the first thing
	// the user sees, not their prior history.
	trust.EstablishTrust(os.Stdout, reader, mgr, cwd, opts.Approve)

	// Replay the resumed conversation so the user sees history before re-prompting.
	if len(history) > 0 {
		replayTranscript(os.Stdout, history)
	}

	// Startup background consolidation (US-008, FR-4/FR-17): if dream is enabled
	// and due, spawn `pigo --dream` in a goroutine now so it runs while the user
	// works — it never blocks the first prompt, and prints a one-line notice on
	// completion. The dream state/lock live under dream.ResolveMemoryRoot (the
	// same root the subprocess consolidates), independent of whether the memory
	// tool is wired into this session. Not-due / disabled is a cheap no-op.
	maybeStartBackgroundDream(os.Stdout, dream.ResolveMemoryRoot(), cwd, opts.Dream)

	// startup.ui_init (T1.1) covers the last assembly stretch (hooks, editor,
	// signal wiring) and is closed by runREPL's first prompt print.
	uiInit := spans.Begin("startup.ui_init")

	deps := replDeps{
		uiInit:     uiInit,
		store:      store,
		header:     header,
		agentCtx:   agentCtx,
		live:       live,
		reg:        reg,
		reminders:  run.WithRunawayGuard(run.TodoReminders(opts.Tools)),
		schedule:   agenttool.ScheduleFromTools(opts.Tools),
		slash:      slash,
		creds:      creds,
		trust:      mgr,
		cwd:        cwd,
		in:         reader,
		confirmMu:  askMu,
		curLeaf:    curLeaf,
		persisted:  len(history),
		memoryRoot: run.MemoryRootFromTools(opts.Tools),
		memstore:   run.MemoryStoreFromTools(opts.Tools),
		snap:       run.SnapshotRecorderFromTools(opts.Tools),
		jobs:       run.BashJobStoreFromTools(opts.Tools),
		notifier:   plugin.NewEventNotifier(opts.Plugins, os.Stderr),
		goal:       agenttool.NewGoalState(),
		telemetry:  cli.NewTelemetryHolder(),
		shellguard: opts.Shellguard,
		toolPlan:   opts.ToolPlan,
		permEngine: permEngine,
		mcpMgr:     opts.MCP,
		skillsView: skillsView,
		surface:    surface,
	}
	// Promoted /status (T6.9 G-4 → T7.7): the report moved to the slash
	// Executor's Status hook — runREPL builds it from deps, so the REPL and
	// the TUI render through the same shared status renderer.
	return runREPL(os.Stdin, os.Stdout, deps)
}
