// Command pigo is the CLI entry point for the pigo agent. It parses flags,
// overlays config.toml, and dispatches to one of the run modes — interactive
// REPL, headless print, session listing, or the internal sub-agent RPC server:
//
//	pigo                                          # interactive REPL (on a TTY)
//	pigo -p "read README and summarize"           # print mode: final text
//	pigo -p "..." --output-format stream-json      # line-delimited JSON events
//	pigo install <pkg> | list | uninstall | update # package management
//
// The provider is resolved from --model against the built-in OpenAI-compatible
// gateways (OpenRouter by default, Ollama for local models), with the API key
// taken from the environment. The process exit code reflects success (0) or
// failure (1), so the command composes cleanly in pipelines. All run-assembly,
// REPL, headless, and config logic lives under internal/cli/*; this file keeps
// only flag parsing (cliOptions), config overlay (applyFileConfig), and the
// dispatch seam that wires those subpackages together.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/cli/pkgcmd"
	"github.com/smallnest/pigo/internal/cli/repl"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/sessioncmd"
	"github.com/smallnest/pigo/internal/cli/tui"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/dream"
	"github.com/smallnest/pigo/internal/lsp"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/selfupdate"
	"github.com/smallnest/pigo/internal/shellguard"
	"github.com/smallnest/pigo/internal/spans"
	"github.com/smallnest/pigo/internal/webhook"
)

// Build metadata, injected at release time via -ldflags by goreleaser
// (see .goreleaser.yaml). They keep their default values for `go build`/
// `go run` from source, so `pigo --version` still works without a release build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// cliOptions is the parsed command line, produced by main() and consumed by
// dispatch. Separating parse from dispatch makes the dispatch logic testable
// without touching the global flag set.
type cliOptions struct {
	prompt   string
	model    string
	baseURL  string
	apiKey   string
	protocol string
	// provider, when non-empty, selects a built-in provider by name from the
	// registry (mirrors pi's provider selection): provider.ResolveProvider then builds the
	// matching wire driver using the provider's default base URL, protocol, and
	// API-key env var, ignoring the model-id heuristics.
	provider  string
	outputFmt string
	// shellguardMode is the bash-command static safety analysis mode
	// (T2.1): "off" | "ask" | "strict", resolved flag > [shellguard] mode >
	// "off" (shellguard is an opt-in advanced feature). Validated by
	// shellguard.ParseMode in run().
	shellguardMode string
	// nonInteractiveDenial controls headless behavior when shellguard denies
	// a bash command: "terminate" (default) aborts the run; "continue"
	// turns the denial into a failed tool result the agent can route around.
	nonInteractiveDenial string
	noTools              bool
	listSessions         bool
	// GitHub review webhook mode (--github-review, issue #567): run an isolated
	// webhook listener that turns PR ready-for-review events into read-only
	// review sessions.
	githubReview           bool
	githubWebhookSecretEnv string
	githubWebhookAddr      string
	githubWebhookRepo      string
	resumeID               string
	continueLast           bool
	// approve grants the launch directory session-level trust up front (mirrors pi's
	// --approve/-a): the first-launch trust prompt is skipped and side-effect
	// tools (bash/write/edit) run without per-call confirmation for this run.
	approve bool
	// noSkills disables skill discovery (mirrors pi's --no-skills): skills under
	// ~/.agents/skills are not loaded as /skill-name commands.
	noSkills bool
	// systemPrompt, when non-empty, replaces the default coding-assistant base
	// instruction (mirrors pi's --system-prompt). The environment block and
	// AGENTS.md injection still apply on top of it.
	systemPrompt string
	// appendSystemPrompt holds --append-system-prompt values (mirrors pi, repeatable):
	// each is a path to a file whose contents are appended, or literal text when
	// it is not an existing file. Appended after the base prompt and AGENTS.md.
	appendSystemPrompt []string
	// configPrompts holds prompt-template paths from the config.toml `prompts`
	// array (settings tier); each is a file or directory loaded non-recursively.
	// Populated by applyFileConfig; empty when the config omits `prompts`.
	configPrompts []string
	// promptTemplates holds --prompt-template paths (CLI tier, repeatable); each
	// is a file or directory loaded non-recursively.
	promptTemplates []string
	// noPromptTemplates disables all prompt-template discovery (global, project,
	// settings, CLI); built-in slash commands are unaffected. Independent of
	// --no-skills.
	noPromptTemplates bool
	// subagentRPC selects the process-isolated sub-agent server mode (US-019,
	// #135): pigo reads JSON-RPC sub-agent run requests from stdin and writes
	// results to stdout. Internal, used by SubAgentTool's process mode.
	subagentRPC bool
	// dream, when set, runs the process-isolated memory-consolidation pass and
	// exits: pigo enumerates + consolidates the global/project memory scope, emits
	// a single-line Report JSON on stdout, and exits 0/1. Internal, spawned by the
	// dream scheduler (and usable headlessly by scripts). See internal/dream and
	// SPEC §4.1/§4.2.
	dream bool
	// dreamDryRun pairs with --dream: analyze and report without writing files or
	// updating dream state (the lock is still taken). SPEC §5.5 dry-run row.
	dreamDryRun bool
	// thinkingLevel, when non-empty, is the --thinking-level flag: the reasoning
	// effort for requests (off|minimal|low|medium|high|xhigh|max). It is the highest-
	// precedence layer in resolveThinkingLevel, overriding PIGO_THINKING_LEVEL, the
	// config files, and the built-in default (medium).
	thinkingLevel string
	// showVersion prints build metadata (version/commit/date, injected at release
	// time by goreleaser) and exits, without running the agent.
	showVersion bool
	// traceStartup is the --trace-startup flag: print the startup/exit span
	// timeline (T1.1, internal/spans) to stderr. It is consumed by a pre-scan in
	// main (spans begin before flag.Parse completes) and declared here only so
	// --help documents it.
	traceStartup bool
	// credentialRef is the config.toml `credential` value (issue #568): a named
	// reference into ~/.pigo/.credentials.yaml (0600). It resolves to opts.apiKey
	// when no --api-key / config api_key is present; the literal secret never
	// touches config files.
	credentialRef string
	// noTUI forces the line-based REPL instead of the full-screen TUI (US-001).
	// When set — or when stdout is not a TTY — the no-prompt path falls back to
	// repl.Run rather than launching tui.Run.
	noTUI bool
	// cwd, when non-empty, is the working directory pigo switches to before doing
	// anything else (matches the Claude Agent SDK's cwd option / git -C). Every
	// cwd-derived resolution — built-in tool file roots, project trust, hooks
	// project dir, .pigo/ project config, git info, the status-bar path — reads
	// os.Getwd(), so a single os.Chdir here makes all of them operate in the
	// given directory. This is what makes pigo usable as an SDK backend that can
	// be pointed at an arbitrary project root.
	cwd string
	// memory holds the resolved [memory]/[checkpoint]/[compaction] config tables
	// (defaults applied, string forms parsed). These have no CLI flags — the
	// config file is their only source — so applyFileConfig always populates this
	// (defaults when the tables are absent) for downstream memory/checkpoint/
	// compaction wiring to consume. See config.MemorySettings.
	memory config.MemorySettings
	// dreamCfg is the resolved [dream] configuration (enabled / interval /
	// recent-sessions), populated by applyFileConfig from the [dream] table with
	// defaults applied. The interactive REPL consumes it to decide the startup
	// background auto-consolidation (US-008). Like memory it has no CLI flags.
	dreamCfg dream.Config
	// permsCfg is the [permissions] rules table (T5.2), passed through from
	// applyFileConfig. Like memory/dream it has no CLI flags; every driver
	// loads it into the permission engine.
	permsCfg config.PermissionsConfig
	// allowedTools and disallowedTools are the --allowed-tools/--disallowed-tools
	// values: the tool-level admission boundary for the run, filling the gap
	// between "all tools" and --no-tools. Each is repeatable and each value may be
	// comma-separated. Names match case-insensitively, and deny wins over allow
	// when a name appears on both sides (fail-closed). The boundary is enforced at
	// the tool-registration layer in run.SetupEnv, strictly before the
	// BeforeToolCall confirmation gate, so --approve waives confirmation prompts
	// but can never widen the boundary.
	allowedTools    []string
	disallowedTools []string
	// toolsCfg is the [tools] table (T4.1 deferred tool declaration). It has no
	// CLI flags, so applyFileConfig overlays it unconditionally and SetupEnv
	// resolves the plan (capability gate + deferrable set) from it.
	toolsCfg config.ToolsConfig
	// mcpCfg is the [mcp] table (T6.8 MCP servers). Like toolsCfg it has no
	// CLI flags; applyFileConfig overlays it unconditionally and SetupEnv
	// connects the servers (per-server fault tolerance, never fatal).
	mcpCfg config.MCPConfig
	// lspSet is the resolved LSP configuration (T8.2: global [lsp] table
	// < trusted project switch < PIGO_LSP; resolved once in the main flow).
	lspSet lsp.Settings
	// shellSet is the resolved shell backend (T8.4: global [shell] table
	// > PIGO_SHELL > platform detect; global layer only, resolved once in
	// the main flow). The zero spec = platform auto-detect.
	shellSet agenttool.ShellSpec
	// modelProfiles is the [models."<id>"] profile table (T7.3 实测反馈),
	// passed through to the front-ends so the /model switcher lists the
	// config's model ids (grok 对齐). Empty when the config declares none.
	modelProfiles map[string]config.ModelProfile
	// providerConfigs is the [provider."<id>"] face (T8.1) carried to the
	// front-ends so a /model switch re-resolves the profile's provider
	// inheritance. Nil when the config declares no sections.
	providerConfigs map[string]config.ProviderSpec
	// proxy is the config resolution's egress proxy URL (T8.1): the model
	// profile's proxy, else its referenced provider's. Empty keeps the
	// default transport. Threaded to SetupEnv and the front-ends.
	proxy string
	// profileWindow / profileMaxTokens are the startup profile's explicit
	// context_window / max_output_tokens overrides; 0 = derive from the
	// provider catalog as before.
	profileWindow    int
	profileMaxTokens int
}

func main() {
	// Session subcommands (pigo session export|list, issue #570) dispatch early
	// like the package-management ones: they are standalone actions that never
	// touch the interactive/headless flag surface.
	if len(os.Args) > 1 && os.Args[1] == "session" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: pigo session export <session-id> [flags] | pigo session list")
			os.Exit(2)
		}
		os.Exit(sessioncmd.Run(os.Args[2], os.Args[3:], os.Stdout, os.Stderr))
	}

	// Package-management subcommands (pigo install|list|uninstall|update ...) are
	// positional and distinct from the flag-driven agent modes, so peel them off
	// before pflag parsing — the agent flags don't apply to them.
	if len(os.Args) > 1 && pkgcmd.Subcommands[os.Args[1]] {
		// `pigo update` routes by whether a positional package name follows it:
		// none — or flags-only, e.g. `pigo update --check` — is binary self-update
		// (#466: download the latest release and replace this binary); a package
		// name stays package-update (handled by pkgcmd). This is the US-003 dispatch
		// split, with updateIsSelfUpdate as the pure classifier so routing is
		// unit-testable (TestUpdateIsSelfUpdate).
		if os.Args[1] == "update" && updateIsSelfUpdate(os.Args[2:]) {
			os.Exit(selfupdate.Run(context.Background(), version, os.Stdout, os.Stderr))
		}
		os.Exit(pkgcmd.Run(os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
	}

	// Startup/exit span profiling (T1.1): --trace-startup is pre-scanned here
	// rather than left to flag.Parse because spans begin before parsing completes
	// — startup.total covers flag registration and Parse itself. SetTrace enables
	// recording even without PIGO_SPAN_PROFILE_OUT; HandleExitSignals no-ops
	// when recording stays off, leaving the default kill behavior untouched.
	for _, a := range os.Args {
		if a == "--trace-startup" {
			spans.SetTrace(os.Stderr)
			break
		}
	}
	spans.HandleExitSignals()
	total := spans.Begin("startup.total")
	// Panic path: this defer runs during unwind, so a crashing startup still
	// lands its profile. The normal path flushes explicitly below (os.Exit
	// would skip defers); the signal path flushes inside HandleExitSignals.
	defer spans.Flush()

	parseSpan := spans.Begin("startup.flag_parse")
	var opts cliOptions
	flag.StringVarP(&opts.prompt, "print", "p", "", "prompt to run in headless print mode")
	flag.StringVarP(&opts.model, "model", "m", "", "model id to run against; resolved from config.toml ([models] profiles / model key) or this flag — unknown ids fail instead of falling back to a gateway (T8.1)")
	flag.StringVarP(&opts.baseURL, "base-url", "u", "", "override provider base URL (e.g. local Ollama)")
	flag.StringVarP(&opts.apiKey, "api-key", "k", "", "API key for the resolved provider (overrides env/config; else <PROVIDER>_API_KEY)")
	flag.StringVarP(&opts.protocol, "protocol", "P", "", "force wire protocol for a custom endpoint: openai | anthropic (default: inferred from model id)")
	flag.StringVar(&opts.provider, "provider", "", "select a built-in provider by name (e.g. deepseek, minimax); uses its default base URL, protocol, and API-key env var (see --help provider list)")
	flag.BoolVar(&opts.githubReview, "github-review", false, "run the isolated GitHub ready-for-review webhook (issue #567): PR draft→ready creates a read-only review session and runs it")
	flag.StringVar(&opts.githubWebhookSecretEnv, "github-webhook-secret-env", "PIGO_GITHUB_WEBHOOK_SECRET", "name of the env var holding the high-entropy GitHub webhook secret (credential reference; never the secret itself)")
	flag.StringVar(&opts.githubWebhookAddr, "github-webhook-addr", "127.0.0.1:3081", "listen address for the GitHub review webhook (put a TLS reverse proxy/tunnel in front; the endpoint is plain HTTP)")
	flag.StringVar(&opts.githubWebhookRepo, "github-webhook-repo", "", "restrict review to this repository (owner/name); empty accepts any repo")
	flag.StringVarP(&opts.outputFmt, "output-format", "o", "text", "output format: text | stream-json")
	flag.StringVar(&opts.shellguardMode, "shellguard", "", "bash command static safety analysis: off | ask | strict (default off; opt-in; config: [shellguard] mode)")
	flag.StringVar(&opts.nonInteractiveDenial, "non-interactive-denial", "terminate", "headless behavior when shellguard denies a command: terminate | continue (continue converts the denial into a failed tool result and keeps the run going)")
	flag.BoolVarP(&opts.noTools, "no-tools", "n", false, "disable the built-in file/shell tools")
	flag.StringArrayVar(&opts.allowedTools, "allowed-tools", nil, "restrict the model to these tools (repeatable, comma-separated, case-insensitive); empty means no restriction and --disallowed-tools wins on conflict")
	flag.StringArrayVar(&opts.disallowedTools, "disallowed-tools", nil, "remove these tools from the model's set (repeatable, comma-separated, case-insensitive); takes precedence over --allowed-tools")
	flag.BoolVarP(&opts.listSessions, "list-sessions", "l", false, "list stored interactive sessions and exit")
	flag.StringVarP(&opts.resumeID, "resume", "r", "", "resume the interactive session with this id")
	flag.BoolVarP(&opts.continueLast, "continue", "c", false, "resume the most recent interactive session")
	flag.BoolVarP(&opts.approve, "approve", "a", false, "trust the working directory for this run: skip the first-launch trust prompt and run side-effect tools without per-call confirmation")
	flag.BoolVar(&opts.noSkills, "no-skills", false, "disable skill discovery (do not load skills under ~/.agents/skills as /skill-name commands)")
	flag.BoolVar(&opts.noPromptTemplates, "no-prompt-templates", false, "disable prompt-template discovery (do not load ~/.pigo/{commands,prompts}, .pigo/prompts, config prompts, or --prompt-template); built-in slash commands are unaffected")
	flag.StringVar(&opts.systemPrompt, "system-prompt", "", "system prompt to use instead of the default coding-assistant prompt (mirrors pi --system-prompt)")
	flag.StringArrayVar(&opts.appendSystemPrompt, "append-system-prompt", nil, "append text or file contents to the system prompt; repeatable (mirrors pi --append-system-prompt)")
	flag.StringArrayVar(&opts.promptTemplates, "prompt-template", nil, "load a prompt template from a file or directory (non-recursive); repeatable (mirrors pi --prompt-template)")
	flag.StringVar(&opts.thinkingLevel, "thinking-level", "", "reasoning effort: off|minimal|low|medium|high|xhigh|max (overrides PIGO_THINKING_LEVEL and config; default medium)")
	flag.BoolVar(&opts.subagentRPC, "subagent-rpc", false, "internal: run as a process-isolated sub-agent JSON-RPC server over stdio (US-019)")
	flag.BoolVar(&opts.dream, "dream", false, "internal: run a memory-consolidation pass over the global/project memory scope, emit a Report JSON on stdout, and exit (SPEC §4.1)")
	flag.BoolVar(&opts.dreamDryRun, "dream-dry-run", false, "internal: with --dream, analyze and report without writing files or updating dream state (SPEC §5.5)")
	flag.BoolVar(&opts.noTUI, "no-tui", false, "use the line-based REPL instead of the full-screen TUI")
	flag.StringVarP(&opts.cwd, "cwd", "C", "", "run as if pigo was started in this directory (matches the Claude Agent SDK's cwd; like git -C): tool file access, trust, hooks, and project config all resolve against it")
	flag.BoolVarP(&opts.showVersion, "version", "v", false, "print version information and exit")
	flag.BoolVar(&opts.traceStartup, "trace-startup", false, "print the startup/exit span timeline to stderr (consumed via pre-scan; implies span recording)")
	// Extend the default pflag usage with a "Supported providers" block so
	// `--help` documents the values accepted by --provider (name → env var →
	// default base URL → protocol). The list is derived from the provider
	// registry, so it never drifts from the code.
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		cli.PrintProviderHelp(out)
	}
	flag.Parse()
	parseSpan.End()

	// --cwd switches the process working directory before anything cwd-derived is
	// resolved (tool roots, trust, hooks, project config, git info). Doing it here
	// — after parse, before config overlay and dispatch — means every downstream
	// os.Getwd() sees the requested directory, so pigo behaves as if it had been
	// launched there. A bad path is a usage error (exit 2) rather than a silent
	// fall-through to the original directory.
	if opts.cwd != "" {
		if err := os.Chdir(opts.cwd); err != nil {
			fmt.Fprintf(os.Stderr, "pigo: --cwd: %v\n", err)
			os.Exit(2)
		}
	}

	// Overlay the user config (T8.1: canonical path = $PIGO_HOME/config.toml
	// else ~/.pigo/config.toml; a pre-unification XDG file is read as a
	// fallback and migrated — LoadUserConfig copies it — with a warning).
	// File values replace built-in defaults, but any flag the user set on the
	// command line still wins (CLI > file > default). A malformed file warns
	// but does not abort — defaults apply.
	cfgLoad := spans.Begin("startup.config_load")
	cfg, cfgPath, cfgErr := config.LoadUserConfig()
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", cfgErr)
	} else {
		if cfgPath != "" && cfgPath != config.FileConfigPath() {
			fmt.Fprintf(os.Stderr, "pigo: config loaded from legacy path %s (copied to %s); the old file can be moved or deleted\n", cfgPath, config.FileConfigPath())
		}
		// Provider-section warnings fire once at startup, grok's
		// lenient-parse alignment: a bad section is skipped, never fatal.
		for _, w := range cfg.ValidateProviders() {
			fmt.Fprintf(os.Stderr, "pigo: config: %s\n", w)
		}
		applyFileConfig(&opts, cfg, flag.CommandLine.Changed)
		// [models."<id>"] profiles (T7.3 实测反馈): when the resolved model
		// names a profile it becomes the startup model (its base_url/keys/
		// window travel with it); the full profile set always flows to the
		// front-ends so /model lists the config's model ids (grok 对齐).
		applyModelProfile(&opts, cfg, flag.CommandLine.Changed)
	}
	// LSP settings (T8.2): layered once here so every driver — headless,
	// TUI, REPL, review — shares one resolution. A malformed project
	// config.json is a hard error, same as the hooks/thinking layers.
	lspSet, err := run.ResolveLSPSettings(cfg.LSP)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(2)
	}
	opts.lspSet = lspSet
	// Shell backend (T8.4): global-only layering, resolved once here for the
	// same reason. An unknown backend name is a usage error (exit 2).
	shellSet, err := run.ResolveShellSettings(cfg.Shell, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(2)
	}
	opts.shellSet = shellSet
	cfgLoad.End()

	// Validate the shellguard mode tiers now (flag > file > default off): a
	// bad value is a usage error (exit 2), matching --output-format, rather
	// than a silently disabled safety feature.
	if opts.shellguardMode == "" {
		opts.shellguardMode = "off"
	}
	if _, err := shellguard.ParseMode(opts.shellguardMode); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(2)
	}
	if opts.nonInteractiveDenial != "terminate" && opts.nonInteractiveDenial != "continue" {
		fmt.Fprintf(os.Stderr, "pigo: unknown --non-interactive-denial %q (want terminate|continue)\n", opts.nonInteractiveDenial)
		os.Exit(2)
	}
	// A bare provider name ("zai", "deepseek") means that provider's default
	// model (issue #564): canonicalize once here so every downstream consumer —
	// SetupEnv, the REPL/TUI live seeds, sub-agent children — carries a real
	// model id instead of routing the literal name to OpenRouter.
	opts.model = provider.CanonicalizeModel(opts.model)

	// Resolve a credential reference into an API key (issue #568): the config
	// carries only a NAME; the literal secret lives in
	// $PIGO_HOME/.credentials.yaml (0600). A missing reference warns rather
	// than aborts — the provider may authenticate from the ambient environment.
	if opts.credentialRef != "" && opts.apiKey == "" {
		path := provider.CredentialFilePath()
		if provider.CredentialFilePermissionsWarn(path) {
			fmt.Fprintf(os.Stderr, "pigo: warning: %s is readable by group/other; chmod 600 recommended\n", path)
		}
		key, err := provider.ResolveCredentialReference(path, opts.credentialRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: credential %q: %v\n", opts.credentialRef, err)
		} else {
			opts.apiKey = key
		}
	}

	// --version is a standalone action: print build metadata and exit.
	if opts.showVersion {
		fmt.Printf("pigo %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	// A prompt may also be supplied as positional args.
	if opts.prompt == "" {
		opts.prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}

	// Normal exit path: close the startup envelope and flush the profile
	// before os.Exit (which would skip the deferred Flush above).
	code := dispatch(context.Background(), opts, os.Stdout, os.Stderr)
	total.End()
	spans.Flush()
	os.Exit(code)
}

// applyFileConfig overlays config.toml values onto opts, but only for flags the
// user did not set on the command line (changed reports whether a flag name was
// explicitly passed). This yields the precedence: CLI flag > config file >
// default. Zero-valued config fields never override.
func applyFileConfig(opts *cliOptions, cfg config.FileConfig, changed func(string) bool) {
	if cfg.Model != "" && !changed("model") {
		opts.model = cfg.Model
	}
	if cfg.BaseURL != "" && !changed("base-url") {
		opts.baseURL = cfg.BaseURL
	}
	if cfg.APIKey != "" && !changed("api-key") {
		opts.apiKey = cfg.APIKey
	}
	if cfg.Credential != "" && !changed("api-key") {
		// A direct api_key in the file wins over a reference when both are set;
		// a reference only fills the gap (issue #568).
		if cfg.APIKey == "" {
			opts.credentialRef = cfg.Credential
		}
	}
	if cfg.Protocol != "" && !changed("protocol") {
		opts.protocol = cfg.Protocol
	}
	if cfg.Provider != "" && !changed("provider") {
		opts.provider = cfg.Provider
		// T8.1: a top-level provider naming a usable [provider] section is a
		// config connection — its fields fill the slots the top-level config
		// and the flags left unset, and the proxy rides the section. The
		// section otherwise stays a built-in family hint (old semantics); the
		// reference itself is not passed to ResolveProvider as a family name
		// when it is a config connection (the connection is fully expressed
		// by base_url/protocol/credentials).
		if spec, ok := cfg.ProviderFor(cfg.Provider); ok {
			opts.provider = ""
			if spec.BaseURL != "" && opts.baseURL == "" && !changed("base-url") {
				opts.baseURL = spec.BaseURL
			}
			if spec.Protocol != "" && opts.protocol == "" && !changed("protocol") {
				opts.protocol = spec.Protocol
			}
			if spec.APIKey != "" && opts.apiKey == "" && !changed("api-key") {
				opts.apiKey = spec.APIKey
			}
			if opts.apiKey == "" && spec.Credential != "" && !changed("api-key") {
				opts.credentialRef = spec.Credential
			}
			if opts.apiKey == "" && spec.EnvKey != "" {
				if v := os.Getenv(spec.EnvKey); v != "" {
					opts.apiKey = v
				}
			}
			if spec.Proxy != "" {
				opts.proxy = spec.Proxy
			}
		}
	}
	if cfg.ThinkingLevel != "" && !changed("thinking-level") {
		opts.thinkingLevel = cfg.ThinkingLevel
	}
	if cfg.OutputFormat != "" && !changed("output-format") {
		opts.outputFmt = cfg.OutputFormat
	}
	if cfg.Shellguard.Mode != "" && !changed("shellguard") {
		opts.shellguardMode = cfg.Shellguard.Mode
	}
	if cfg.NoTools && !changed("no-tools") {
		opts.noTools = true
	}
	if cfg.NoSkills && !changed("no-skills") {
		opts.noSkills = true
	}
	if cfg.Approve && !changed("approve") {
		opts.approve = true
	}
	if cfg.SystemPrompt != "" && !changed("system-prompt") {
		opts.systemPrompt = cfg.SystemPrompt
	}
	// The tool boundary follows the standard precedence (CLI > file > default)
	// rather than the additive treatment prompts get below. Merging would be the
	// wrong semantics for a security boundary: a user passing --allowed-tools to
	// widen what the file's allowed_tools narrowed must actually get the wider
	// set, not the intersection. Each flag overrides its own key independently:
	// --allowed-tools does not clear a file-level disallowed_tools, and because
	// deny wins on conflict a file deny survives a CLI allow — re-admitting a
	// file-denied tool requires overriding --disallowed-tools on the CLI.
	if len(cfg.AllowedTools) > 0 && !changed("allowed-tools") {
		opts.allowedTools = cfg.AllowedTools
	}
	if len(cfg.DisallowedTools) > 0 && !changed("disallowed-tools") {
		opts.disallowedTools = cfg.DisallowedTools
	}
	// prompts (settings tier) are additive with --prompt-template (CLI tier,
	// wired in #339), so they are always passed through when present.
	if len(cfg.Prompts) > 0 {
		opts.configPrompts = cfg.Prompts
	}
	// The [memory]/[checkpoint]/[compaction] tables have no CLI flags, so they
	// are resolved (with defaults) and overlaid unconditionally — an absent set
	// of tables yields the default-safe MemorySettings.
	opts.memory = cfg.ResolveMemorySettings()
	// The [tools] table (T4.1 deferred tool declaration) also has no CLI flags;
	// overlay it unconditionally. Plan resolution (capability gate + deferrable
	// set) lives in run.SetupEnv.
	opts.toolsCfg = cfg.Tools
	// The [mcp] table (T6.8 MCP servers) also has no CLI flags; overlay it
	// unconditionally. Connection (per-server fault tolerance) lives in
	// run.SetupEnv.
	opts.mcpCfg = cfg.MCP
	// The [dream] table also has no CLI flags; normalize it (defaults applied when
	// the table is absent) so the interactive startup trigger has a resolved
	// Config. NewConfig treats a nil enabled as true, so dream is on by default.
	opts.dreamCfg = dream.NewConfig(cfg.Dream.Enabled, cfg.Dream.IntervalDays, cfg.Dream.RecentSessions)
	opts.permsCfg = cfg.Permissions
}

// applyModelProfile resolves the [models."<id>"] profile face (T7.3 实测反馈,
// grok 的 [model."<id>"] 对齐; T8.1 inheritance). It always carries the profile
// set onto opts so the /model switcher lists the config's model ids; when the
// resolved model string names a profile, that profile's CONNECTION face — the
// profile's own fields over the referenced [provider] section's per-field
// inheritance — fills the unset, flag-unshadowed slots (base_url, protocol,
// credentials, proxy, thinking_level), and its explicit context_window /
// max_output_tokens ride along as overrides. Runs right after applyFileConfig
// and BEFORE CanonicalizeModel, so a profile id (user-chosen) is matched
// verbatim and only the wire id it resolves to gets canonicalized.
func applyModelProfile(opts *cliOptions, cfg config.FileConfig, changed func(string) bool) {
	opts.modelProfiles = cfg.Models
	opts.providerConfigs = cfg.Providers
	key, profile, ok := cfg.ProfileFor(opts.model)
	if !ok {
		return
	}
	// T8.1: the connection face is the profile resolved against the
	// [provider] sections (per-field inheritance); the profile's explicit
	// fields win, unset fields ride the referenced provider.
	conn, connOK := cfg.ResolveModelConnection(key)
	if !connOK {
		conn = config.ModelConnection{}
	}
	opts.model = profile.WireModel(key)
	if conn.BaseURL != "" && !changed("base-url") {
		opts.baseURL = conn.BaseURL
	}
	if conn.Protocol != "" && !changed("protocol") {
		opts.protocol = conn.Protocol
	}
	if conn.APIKey != "" && !changed("api-key") {
		// A direct api_key wins over a reference and over the env var (the
		// same precedence the top-level config uses, issue #568).
		opts.apiKey = conn.APIKey
	}
	if opts.apiKey == "" && conn.Credential != "" && !changed("api-key") {
		opts.credentialRef = conn.Credential
	}
	if opts.apiKey == "" && conn.EnvKey != "" {
		if v := os.Getenv(conn.EnvKey); v != "" {
			opts.apiKey = v
		}
	}
	if conn.ThinkingLevel != "" && !changed("thinking-level") {
		opts.thinkingLevel = conn.ThinkingLevel
	}
	// The provider reference drives resolution: a config connection is fully
	// expressed by its base_url/protocol/credentials (the reference itself is
	// not a built-in family name), while a built-in family hint keeps the old
	// heuristic path.
	if conn.ProviderRef != "" && !changed("provider") {
		if conn.UsedConfigProvider {
			opts.provider = ""
		} else {
			opts.provider = conn.ProviderRef
		}
	}
	opts.proxy = conn.Proxy
	opts.profileWindow = conn.ContextWindow
	opts.profileMaxTokens = conn.MaxOutputTokens
}

// dispatch runs the resolved command and returns a process exit code, writing
// diagnostics to errOut. It is the run-assembly seam: every path (list, REPL,
// headless, subagent-rpc) is reached from here, so the CLI's behavior can be
// exercised without re-parsing flags. A returned code of 0 is success.
func dispatch(ctx context.Context, opts cliOptions, out, errOut io.Writer) int {
	// opts.shellguardMode was validated in run() (flag > file > off).
	sgMode := shellguard.Mode(opts.shellguardMode)
	modeDispatch := spans.Begin("startup.mode_dispatch")

	// --subagent-rpc is a fully separate mode: speak the sub-agent JSON-RPC
	// protocol over stdio and exit. It is the subprocess end of process-isolated
	// sub-agents and shares nothing with the interactive/headless paths.
	if opts.subagentRPC {
		spans.SetLabel("subagent-rpc")
		modeDispatch.End()
		return headless.RunSubAgentRPC(ctx, os.Stdin, out, errOut)
	}

	// --dream is the subprocess consolidation mode (SPEC §4.1/§4.2): run one
	// memory-consolidation pass to completion, emit a single-line Report JSON on
	// stdout (progress/logs go to stderr), and exit 0 on success / 1 on failure.
	// It runs before any interactive/headless session assembly and honors -C/--cwd
	// for the project scope (applied above via os.Chdir). It shares nothing with
	// the REPL/headless paths.
	if opts.dream {
		spans.SetLabel("dream")
		modeDispatch.End()
		return runDream(ctx, opts, out, errOut)
	}

	// --github-review is a standalone long-running mode: the isolated webhook
	// listener (issue #567). It shares nothing with interactive/headless paths.
	if opts.githubReview {
		spans.SetLabel("github-review")
		modeDispatch.End()
		return runGitHubReview(ctx, opts, errOut)
	}

	// --list-sessions is a standalone action: print and exit.
	if opts.listSessions {
		spans.SetLabel("list-sessions")
		modeDispatch.End()
		if err := headless.PrintSessions(out); err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		return 0
	}

	// --continue resolves to the most recently updated session id.
	resumeID := opts.resumeID
	if opts.continueLast && resumeID == "" {
		id, err := headless.MostRecentSessionID()
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		if id == "" {
			fmt.Fprintln(errOut, "pigo: no sessions to continue")
			return 1
		}
		resumeID = id
	}

	// No prompt + an interactive terminal → start the interactive UI. By default
	// this is the full-screen TUI (US-001); --no-tui (or a non-terminal stdout)
	// forces the line-based REPL (US-003). A --resume id also enters the
	// interactive UI to continue an existing session. No prompt with a
	// non-terminal stdout (pipe/CI) and no resume is an error, since there is
	// nothing to run and nothing to interact with.
	if opts.prompt == "" {
		isTTY := ui.StdoutIsTerminal()
		if resumeID == "" && !isTTY {
			fmt.Fprintln(errOut, "pigo: no prompt (use -p \"...\" or positional args)")
			return 2
		}
		modeDispatch.End()
		env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, opts.proxy, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt, opts.memory.Memory.Enabled, opts.memory.MaxContext, opts.toolsCfg, opts.mcpCfg, opts.lspSet, run.NewToolPolicy(opts.allowedTools, opts.disallowedTools))
	// Shell backend (T8.4): inject the resolved spec into the live bash tool.
	run.SetBashShell(env.Tools, opts.shellSet)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return setupExitCode(err)
		}
		if env.Plugins != nil {
			defer closeWithSpan(env.Plugins.Close)
		}
		if env.MCP != nil {
			defer closeWithSpan(env.MCP.Close)
		}
		if env.LSP != nil {
			defer closeWithSpan(env.LSP.Close)
		}
		if env.Memory != nil {
			defer closeWithSpan(env.Memory.Close)
		}
		thinking, err := run.ResolveThinkingLevel(opts.thinkingLevel)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 2
		}
		// Interactive modes (TUI + REPL) default to --approve: the operator is
		// present, and the historical fail-closed default made even read-only
		// shell calls unusable in the TUI (wiki/port/tui-blank-header-fixes §5).
		// Pass -a=false to restore per-call fail-closed approval; headless runs
		// keep the opt-in default so unattended side effects stay gated.
		approvedSet := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "approve" {
				approvedSet = true
			}
		})
		if !approvedSet {
			opts.approve = true
		}
		if shouldUseTUI(opts, isTTY) {
			spans.SetLabel("tui")
			// Refresh the cached latest-release check off the hot path so the banner
			// can show an upgrade hint on this or the next launch without blocking
			// startup (US-004). No-ops for dev builds or a fresh cache.
			selfupdate.StartBackgroundCheck(version)
			exitTotal := spans.Begin("exit.total")
			err := tui.Run(tui.Options{
				Model:             opts.model,
				ProviderName:      env.ProviderName,
				Provider:          env.Provider,
				BaseURL:           opts.baseURL,
				APIKey:            opts.apiKey,
				Protocol:          opts.protocol,
				Version:           version,
				ThinkingLevel:     thinking,
				Tools:             env.Tools,
				SysPrompt:         env.SysPrompt,
				ResumeID:          resumeID,
				Approve:           opts.approve,
				Shellguard:        sgMode,
				Skills:            env.Skills,
				ToolPlan:          env.ToolPlan,
				Plugins:           env.Plugins,
				MCP:               env.MCP,
				LSP:               env.LSP,
				Bash:              env.Bash,
				Subagents:         env.Subagents,
				Usage:             env.Usage,
				ConfigPrompts:     opts.configPrompts,
				CliPrompts:        opts.promptTemplates,
				NoPromptTemplates: opts.noPromptTemplates,
				MaxContext:        env.MaxContext,
				Models:            opts.modelProfiles,
				ProviderConfigs:   opts.providerConfigs,
				Proxy:             opts.proxy,
				ContextWindow:     opts.profileWindow,
				MaxOutputTokens:   opts.profileMaxTokens,
				Permissions:       opts.permsCfg,
			})
			exitTotal.End()
			if err != nil {
				fmt.Fprintf(errOut, "pigo: %v\n", err)
				return 1
			}
			return 0
		}
		spans.SetLabel("repl")
		exitTotal := spans.Begin("exit.total")
		err = repl.Run(repl.Options{
			Model:             opts.model,
			ProviderName:      env.ProviderName,
			Provider:          env.Provider,
			BaseURL:           opts.baseURL,
			APIKey:            opts.apiKey,
			Protocol:          opts.protocol,
			ThinkingLevel:     thinking,
			Tools:             env.Tools,
			SysPrompt:         env.SysPrompt,
			ResumeID:          resumeID,
			Approve:           opts.approve,
			Shellguard:        sgMode,
			Skills:            env.Skills,
			Plugins:           env.Plugins,
			MCP:               env.MCP,
			LSP:               env.LSP,
			Bash:              env.Bash,
			Subagents:         env.Subagents,
			Usage:             env.Usage,
			ToolPlan:          env.ToolPlan,
			MaxContext:        env.MaxContext,
			Models:            opts.modelProfiles,
			ProviderConfigs:   opts.providerConfigs,
			Proxy:             opts.proxy,
			ContextWindow:     opts.profileWindow,
			MaxOutputTokens:   opts.profileMaxTokens,
			ConfigPrompts:     opts.configPrompts,
			CliPrompts:        opts.promptTemplates,
			NoPromptTemplates: opts.noPromptTemplates,
			Dream:             opts.dreamCfg,
			Permissions:       opts.permsCfg,
		})
		exitTotal.End()
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		return 0
	}

	modeDispatch.End()
	spans.SetLabel("headless")
	mode, err := headless.ParseOutputMode(opts.outputFmt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 2
	}

	env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, opts.proxy, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt, opts.memory.Memory.Enabled, opts.memory.MaxContext, opts.toolsCfg, opts.mcpCfg, opts.lspSet, run.NewToolPolicy(opts.allowedTools, opts.disallowedTools))
	// Shell backend (T8.4): inject the resolved spec into the live bash tool.
	run.SetBashShell(env.Tools, opts.shellSet)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return setupExitCode(err)
	}
	if env.Plugins != nil {
		defer closeWithSpan(env.Plugins.Close)
	}
	if env.LSP != nil {
		defer closeWithSpan(env.LSP.Close)
	}
	if env.Memory != nil {
		defer closeWithSpan(env.Memory.Close)
	}
	exitTotal := spans.Begin("exit.total")
	code := headless.Run(ctx, headless.RunParams{
		Mode:                 mode,
		Env:                  env,
		Prompt:               opts.prompt,
		Model:                opts.model,
		APIKey:               opts.apiKey,
		ThinkingLevel:        opts.thinkingLevel,
		ResumeID:             resumeID,
		Shellguard:           sgMode,
		NonInteractiveDenial: opts.nonInteractiveDenial,
		Permissions:          opts.permsCfg,
	}, out, errOut)
	exitTotal.End()
	return code
}

// closeWithSpan wraps a deferred Env teardown (plugin manager, memory store —
// the MCP/plugin/background-work harvest) in an exit.shutdown span so the exit
// profile covers it. Used via defer in dispatch so it runs in the caller's frame.
func closeWithSpan(close func() error) {
	s := spans.Begin("exit.shutdown")
	close()
	s.End()
}

// setupExitCode maps a run.SetupEnv failure to a process exit code. A bad tool
// policy is a usage error (2), matching --cwd and --output-format; everything
// else — provider resolution, prompt assembly — is a runtime failure (1).
func setupExitCode(err error) int {
	var policyErr *run.ToolPolicyError
	if errors.As(err, &policyErr) {
		return 2
	}
	return 1
}

// runDream executes the subprocess memory-consolidation pass (SPEC §4.1/§4.2).
// It runs dream.Runner to completion, marshals the resulting Report as a single
// line of JSON on stdout (the parent/scheduler parses this), and returns the
// process exit code: 0 on success (including a "skipped" run when another dream
// holds the lock) or 1 on failure. Progress and diagnostics go to errOut. The
// project scope comes from the working directory, which -C/--cwd already applied
// via os.Chdir before dispatch, so an empty ProjectDir here resolves to cwd.
// runGitHubReview serves the isolated GitHub ready-for-review webhook
// (issue #567). The secret is resolved indirectly — the flag names the env var
// that carries the high-entropy shared secret — so it never sits in config or
// the process list. The provider/environment resolve through the normal
// SetupEnv chain, but each review run executes only the read-only tool subset.
func runGitHubReview(ctx context.Context, opts cliOptions, errOut io.Writer) int {
	secret := strings.TrimSpace(os.Getenv(strings.TrimSpace(opts.githubWebhookSecretEnv)))
	if secret == "" {
		fmt.Fprintf(errOut, "pigo: --github-review requires a webhook secret in $%s\n", opts.githubWebhookSecretEnv)
		return 2
	}
	env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, opts.proxy, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt, opts.memory.Memory.Enabled, opts.memory.MaxContext, opts.toolsCfg, opts.mcpCfg, opts.lspSet, run.NewToolPolicy(opts.allowedTools, opts.disallowedTools))
	// Shell backend (T8.4): inject the resolved spec into the live bash tool.
	run.SetBashShell(env.Tools, opts.shellSet)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return setupExitCode(err)
	}
	if env.Plugins != nil {
		defer closeWithSpan(env.Plugins.Close)
	}
	if env.LSP != nil {
		defer closeWithSpan(env.LSP.Close)
	}
	if env.Memory != nil {
		defer closeWithSpan(env.Memory.Close)
	}
	store, err := headless.SessionStore()
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	srv := &webhook.Server{
		Secret:       secret,
		Repo:         opts.githubWebhookRepo,
		Store:        store,
		Workspace:    env.Cwd,
		Model:        opts.model,
		ProviderName: env.ProviderName,
		SysPrompt:    env.SysPrompt,
		Provider:     env.Provider,
		Runner:       nil, // set below via DefaultRunner once srv is built
	}
	srv.Runner = srv.DefaultRunner()
	fmt.Fprintf(errOut, "pigo: github review webhook listening on %s (repo filter: %q; TLS reverse proxy required in front)\n", opts.githubWebhookAddr, opts.githubWebhookRepo)
	httpSrv := &http.Server{Addr: opts.githubWebhookAddr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	exitTotal := spans.Begin("exit.total")
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			exitTotal.End()
			fmt.Fprintf(errOut, "pigo: webhook listener: %v\n", err)
			return 1
		}
	case <-ctx.Done():
		_ = httpSrv.Shutdown(context.Background())
	}
	exitTotal.End()
	return 0
}

func runDream(ctx context.Context, opts cliOptions, out, errOut io.Writer) int {
	projectDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	// The dream pass reuses the main-session model (SPEC Q3): resolve the same
	// model/provider/api-key tuple cmd/pigo already overlaid from flags+config,
	// and inject a real LLM-backed Consolidator so `pigo --dream` performs the
	// semantic merge/prune step (not just the deterministic dedup/path-clean).
	thinking, err := run.ResolveThinkingLevel(opts.thinkingLevel)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	cons, err := dream.NewLLMConsolidator(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, thinking)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	r := &dream.Runner{Consolidator: cons}
	report, err := r.Run(ctx, dream.RunOptions{
		DryRun:     opts.dreamDryRun,
		ProjectDir: projectDir,
	})
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	// Single-line JSON on stdout is the stdout contract (SPEC §4.2). Encoder
	// writes a trailing newline, keeping the report one line.
	if err := json.NewEncoder(out).Encode(report); err != nil {
		fmt.Fprintf(errOut, "pigo: dream: encode report: %v\n", err)
		return 1
	}
	return 0
}

// shouldUseTUI is the pure entry-gating predicate for the no-prompt path
// (US-001, SPEC 4.2/5.2): the full-screen TUI is used only when stdout is a TTY
// and --no-tui was not set. --no-tui or a non-terminal stdout always forces the
// line-based REPL. Keeping the decision in a side-effect-free function lets the
// gating be unit-tested without a real terminal or spawning Bubble Tea (see
// TestDispatchTUIGating); dispatch handles the non-TTY/no-resume usage error
// before calling this, so it only decides TUI-vs-REPL for the interactive case.
func shouldUseTUI(opts cliOptions, isTTY bool) bool {
	return isTTY && !opts.noTUI
}

// updateIsSelfUpdate classifies the arguments that follow `pigo update` (US-003)
// to route between binary self-update and pkgmgr package-update. It returns true
// — self-update — when no positional package name is present: any argument that
// does not begin with '-' is treated as a package name and routes to
// package-update, while flags-only invocations (e.g. `pigo update --check`) stay
// on the self-update path. Keeping the decision side-effect-free lets the routing
// be unit-tested without spawning either update path (see TestUpdateIsSelfUpdate).
func updateIsSelfUpdate(rest []string) bool {
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return true
}
