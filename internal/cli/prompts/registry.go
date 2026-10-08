// Package prompts holds the slash-command registry assembly shared by the REPL
// (internal/cli/repl) and the forthcoming TUI (internal/cli/tui). It was sunk
// out of the repl package (#383) so both front-ends wire the same built-in,
// live-state, plugin-declared, prompt-template and skill commands from one
// owner, avoiding drift between the two command surfaces.
//
// The logic here is a verbatim move of repl's former private
// buildSlashRegistry/loadPromptPaths/promptTemplateSources (plus their
// register helpers), exported unchanged so REPL behavior is identical.
package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// PromptTemplateSources carries the prompt-template discovery sources that
// BuildSlashRegistry loads beyond the global ~/.pigo/{commands,prompts} dirs.
// Settings is the config.toml `prompts` array (TierSettings); CLI is the
// --prompt-template flag list (TierCLI, wired in #339). Each entry is a file or
// directory (loaded non-recursively). Missing paths are warned and skipped.
type PromptTemplateSources struct {
	Settings []string
	CLI      []string
	// Disable (--no-prompt-templates) turns off all prompt-template discovery
	// (global, project, settings, CLI); built-ins and skills are unaffected.
	Disable bool
	// ProjectDir is the project-local prompts dir (.pigo/prompts in the working
	// dir), loaded at the project tier only when ProjectTrusted is true.
	ProjectDir string
	// ProjectTrusted reports whether the working directory is trusted; project
	// templates load only then (mirrors pi: project prompts after the project is
	// trusted).
	ProjectTrusted bool
}

// LoadPromptPaths loads prompt templates from each path (file or dir), skipping
// and warning on paths that don't exist or fail to read. It is tier-agnostic;
// the caller registers each result at the desired tier (AddSettings/AddCLI).
func LoadPromptPaths(paths []string) []runtime.SlashCommand {
	var out []runtime.SlashCommand
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: prompts path %q not found, skipping\n", p)
			continue
		}
		var cmds []runtime.SlashCommand
		if info.IsDir() {
			cmds, err = runtime.LoadUserCommandsDir(p)
		} else {
			c, e := runtime.LoadPromptFile(p)
			if e != nil {
				err = e
			} else {
				cmds = []runtime.SlashCommand{c}
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: prompts path %q: %v\n", p, err)
			continue
		}
		out = append(out, cmds...)
	}
	return out
}

// BuildSlashRegistry assembles the slash-command registry: compile-time
// built-ins seeded by runtime.NewSlashRegistry, the live-state action commands
// (/model, /help) bound to live, user declarative templates loaded from
// ~/.pigo/commands (or $PIGO_HOME/commands), plugin-declared commands from the
// loaded Manager, plus the pre-loaded skills — each surfaced as a "/skill-name"
// command (mirrors Claude Code's /skill invocation). A missing directory is not an
// error. Names that collide with a built-in are shadowed (the built-in wins) and
// reported on stderr. The skills slice is loaded once by setupAgentEnv (empty
// under --no-skills), so no /skill-name commands are registered when it is
// empty. mgr may be nil (no plugins loaded). creds may be nil, which disables
// "/models fetch" online discovery.
func BuildSlashRegistry(live *cli.LiveConfig, creds *provider.CredentialStore, skills []*runtime.Skill, mgr *plugin.Manager, srcs PromptTemplateSources) (*runtime.SlashRegistry, error) {
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)
	RegisterPluginCommands(reg, mgr)
	// --no-prompt-templates disables all prompt-template discovery (global,
	// settings, CLI); built-in slash commands and skills are unaffected.
	if !srcs.Disable {
		dir := os.Getenv("PIGO_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return reg, nil // built-ins only
			}
			dir = filepath.Join(home, ".pigo")
		}
		// Load user prompt templates from both the legacy ~/.pigo/commands and
		// the pi-aligned ~/.pigo/prompts (both non-recursive, global tier).
		// Loading commands first means a same-named template in prompts/
		// overrides the legacy one (last-write-wins within the global tier). A
		// missing directory is not an error (LoadUserCommandsDir returns nil,
		// nil for IsNotExist).
		for _, sub := range []string{"commands", "prompts"} {
			cmds, err := runtime.LoadUserCommandsDir(filepath.Join(dir, sub))
			if err != nil {
				return reg, err
			}
			for _, c := range cmds {
				reg.AddUser(c)
			}
		}
		// Settings-tier templates from the config.toml `prompts` array, then
		// CLI-tier templates from --prompt-template. Each entry is a file or
		// dir; missing paths are warned and skipped.
		for _, c := range LoadPromptPaths(srcs.Settings) {
			reg.AddSettings(c)
		}
		for _, c := range LoadPromptPaths(srcs.CLI) {
			reg.AddCLI(c)
		}
		// Project-tier templates from .pigo/prompts in the working directory,
		// loaded only when the project is trusted (mirrors pi). A missing dir is
		// not an error. Overrides global/settings/CLI (project tier is higher).
		if srcs.ProjectTrusted && srcs.ProjectDir != "" {
			cmds, err := runtime.LoadUserCommandsDir(srcs.ProjectDir)
			if err != nil {
				return reg, err
			}
			for _, c := range cmds {
				reg.AddProject(c)
			}
		}
	}
	// Register skills as /skill-name commands from the pre-loaded set (shared with
	// prompt injection in setupAgentEnv, so the directory is read once). All
	// skills — including disable-model-invocation ones — get a slash command; the
	// prompt-injection side filters the disabled ones. Under --no-skills the set
	// is empty, so nothing is registered.
	for _, s := range skills {
		reg.AddSkill(s.SlashCommand())
	}
	if sh := reg.Shadowed(); len(sh) > 0 {
		parts := make([]string, len(sh))
		for i, e := range sh {
			parts[i] = e.String()
		}
		fmt.Fprintf(os.Stderr, "pigo: commands shadowed by higher-priority source (rename to use): %v\n", parts)
	}
	return reg, nil
}

// RegisterPluginCommands installs each plugin-declared slash command
// (Manager.Commands()) into the registry as a hybrid (Run) command. Invoking it
// RPCs the owning plugin (Plugin.CallCommand), returns the plugin's
// notifications as the outcome Message, and returns the plugin's Prompt to run
// as the next turn. Plugin commands are registered with AddPlugin so a same-named
// built-in still wins (existing precedence preserved) and a collision is
// reported as shadowed. mgr may be nil (no plugins), in which case this is a
// no-op.
//
// The args passed to CallCommand are the invocation's raw argument text encoded
// as a JSON string (json.RawMessage of a quoted string), never null: the host
// (node #263) expects a JSON string for a no-arg command, so a bare "/cmd"
// sends `""` rather than nil. Each command captures its own plugin and spec name
// (loop variables copied per-iteration).
func RegisterPluginCommands(reg *runtime.SlashRegistry, mgr *plugin.Manager) {
	if mgr == nil {
		return
	}
	for _, pc := range mgr.Commands() {
		pc := pc // capture per iteration
		reg.AddPlugin(runtime.SlashCommand{
			Name:        pc.Spec.Name,
			Description: pc.Spec.Description,
			Run: func(args string) (message, prompt string) {
				// Encode the raw arg text as a JSON string ("" for no args), matching
				// the host's CommandCallParams.Args contract (a JSON string, never
				// null). json.Marshal of a Go string always succeeds.
				raw, _ := json.Marshal(args)
				res, err := pc.Plugin.CallCommand(context.Background(), pc.Spec.Name, json.RawMessage(raw))
				if err != nil {
					return fmt.Sprintf("plugin command %q failed: %v", pc.Spec.Name, err), ""
				}
				return formatNotifications(res.Notifications), res.Prompt
			},
		})
	}
}

// formatNotifications renders a plugin command's notifications into a single
// block to surface to the user, one per line, prefixed by their type (when set)
// so severity is visible. Returns "" when there are none.

// switchToProfile rebuilds the live provider from one [models."<id>"] config
// profile: the profile's base_url/protocol/provider win over the session's
// (unset fields fall through), its api_key/credential becomes the credential
// override for the resolved provider, and the compaction window/output cap/
// effort follow the profile's declarations. An explicit effort argument wins
// over the profile's thinking_level. Returns the transcript feedback.
func switchToProfile(live *cli.LiveConfig, creds *provider.CredentialStore, key string, prof config.ModelProfile, effort string) string {
	wire := prof.WireModel(key)
	baseURL := prof.BaseURL
	if baseURL == "" {
		baseURL = live.BaseURL
	}
	protocol := prof.Protocol
	if protocol == "" {
		protocol = live.Protocol
	}
	prov, name, err := provider.ResolveProvider(wire, baseURL, protocol, prof.Provider, os.Getenv)
	if err != nil {
		return fmt.Sprintf("model: cannot switch to profile %q: %v", key, err)
	}
	live.Model = wire
	live.ProviderName = name
	live.Provider = prov
	live.BaseURL = baseURL
	if apiKey := profileAPIKey(prof); apiKey != "" && creds != nil {
		creds.SetOverride(name, apiKey)
	}
	// The window follows the profile's explicit declaration when it has one;
	// otherwise the catalog/default resolution applies as on every switch.
	live.ContextWindow = cli.SeedContextWindow(prov, wire, live.MaxContext, prof.ContextWindow)
	live.MaxOutputTokens = cli.SeedMaxOutputTokens(prov, wire, prof.MaxOutputTokens)
	if effort != "" {
		live.ThinkingLevel = agentcore.ThinkingLevel(effort)
		return fmt.Sprintf("model switched to %s (provider: %s, config profile %s); effort: %s (next turn)", wire, name, key, effort)
	}
	if v, ok := validThinkingLevel(prof.ThinkingLevel); ok {
		live.ThinkingLevel = v
		return fmt.Sprintf("model switched to %s (provider: %s, config profile %s); effort: %s (profile default, next turn)", wire, name, key, v)
	}
	return fmt.Sprintf("model switched to %s (provider: %s, config profile %s)", wire, name, key)
}

// profileAPIKey resolves the profile's credential: the literal api_key wins,
// otherwise the named reference is read from $PIGO_HOME/.credentials.yaml
// (issue #568). An unresolvable reference yields "" so the switch keeps the
// session's existing credential rather than failing the switch.
func profileAPIKey(prof config.ModelProfile) string {
	if prof.APIKey != "" {
		return prof.APIKey
	}
	if prof.Credential == "" {
		return ""
	}
	key, err := provider.ResolveCredentialReference(provider.CredentialFilePath(), prof.Credential)
	if err != nil {
		return ""
	}
	return key
}

// profileListing renders the config-profile section of /models: one line per
// [models."<id>"] entry (id — label — description), the active profile's wire
// model tagged (current). Empty when the config declares no profiles — the
// preset catalog is the fallback face then.
func profileListing(live *cli.LiveConfig) string {
	if live == nil || len(live.ModelProfiles) == 0 {
		return ""
	}
	ids := config.FileConfig{Models: live.ModelProfiles}.ProfileIDs()
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("config profiles (switch with /model <id>):")
	for _, id := range ids {
		p := live.ModelProfiles[id]
		wire := p.WireModel(id)
		fmt.Fprintf(&b, "\n  %s", id)
		if label := p.Label(id); label != id {
			b.WriteString("  — " + label)
		}
		if p.Description != "" {
			b.WriteString("  — " + p.Description)
		}
		if live.Model == wire {
			b.WriteString(" (current)")
		}
	}
	return b.String()
}

// fetchModelCatalog implements "/models fetch" (issue #566): query the live
// provider's endpoint for its real model catalog, cache the ids on live for
// /model switching, and summarize the result. Errors degrade gracefully — the
// static preset listing remains the source of truth for switching.
func fetchModelCatalog(live *cli.LiveConfig, creds *provider.CredentialStore) string {
	if creds == nil {
		return "models: fetch unavailable (no credential store); /models shows the static presets"
	}
	spec, ok := provider.LookupProviderSpec(live.ProviderName)
	if !ok {
		return fmt.Sprintf("models: fetch unavailable: unknown provider %q; /models shows the static presets", live.ProviderName)
	}
	baseURL := live.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = spec.DefaultBaseURL
	}
	protocol := live.Protocol
	if strings.TrimSpace(protocol) == "" {
		protocol = spec.Protocol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := provider.FetchRemoteModels(ctx, baseURL, protocol, creds.GetAPIKey(ctx, live.ProviderName))
	if err != nil {
		return fmt.Sprintf("models: fetch failed: %v\nrun /models for the static presets", err)
	}
	live.FetchedModels = ids
	live.FetchedAt = time.Now()
	preview := ids
	if len(preview) > 8 {
		preview = ids[:8]
	}
	return fmt.Sprintf("models: fetched %d models from %s (%s):\n  %s\nswitch with /model <id>; run /models for the static presets",
		len(ids), strings.TrimRight(baseURL, "/"), live.ProviderName, strings.Join(preview, "\n  "))
}

func formatNotifications(notes []plugin.CommandNotification) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n")
		}
		if n.Type != "" {
			b.WriteString("[")
			b.WriteString(n.Type)
			b.WriteString("] ")
		}
		b.WriteString(n.Message)
	}
	return b.String()
}

// RegisterLiveCommands installs the contract commands (T7.7): /model
// /models /think /effect /status /session /compact /memory /rebuild declare
// identity plus a pure Parse (parse.go); their Execute faces live in the
// front-end Executor (executor.go). /help stays an Action closure — it renders
// the registry itself. The remaining built-ins are loop-face declarations:
// identity plus a Projection (T7.7 slice 2), no Action closure. live and creds
// remain the wiring seam the front-ends pass; the Executor carries them to the
// Execute face.
func RegisterLiveCommands(reg *runtime.SlashRegistry, live *cli.LiveConfig, creds *provider.CredentialStore) {
	// T7.7 contract command: Parse is pure (parse.go); the Execute face — the
	// former closure body — lives in Executor.modelSwitch. Projection marks
	// the bare-submit dropdown the TUI opens.
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "model",
		Description:  "view or switch the active model: /model [model-id] [effort] (see /models for presets)",
		ArgumentHint: "[model-id] [effort]",
		Projection:   runtime.ProjModelMenu,
		Parse:        parseModel,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "models",
		Description: "list preset providers and models you can switch to; /models fetch queries the live endpoint for its real catalog",
		Parse:       parseModels,
	})
	// T7.7 contract commands: /think and its /effect alias share one Parse
	// (parse.go); the Execute face — the former thinkAction body — lives in
	// Executor.execute. Projection marks the bare-submit dropdown the TUI
	// opens for both names.
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "think",
		ArgumentHint: "[off|minimal|low|medium|high|xhigh|max]",
		Description:  "view or switch the reasoning-effort level; takes effect on the next turn",
		Projection:   runtime.ProjThinkMenu,
		Parse:        parseThink,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "effect",
		ArgumentHint: "[off|minimal|low|medium|high|xhigh|max]",
		Description:  "alias of /think: view or switch the reasoning-effort level",
		Projection:   runtime.ProjThinkMenu,
		Parse:        parseThink,
	})
	// T7.7 contract commands: /status /session /compact were an intercept or
	// stub-table face (promoted render, stub entry, stub entry); they are
	// declared commands now — Parse here, Execute in the front-end's Executor
	// (Status/Session/Compact hooks).
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "status",
		Description: "show runtime config, context, MCP, skills, permissions, credentials, telemetry",
		Parse:       parseStatus,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "session",
		Description: "show session stats: messages, tokens, model, compactions",
		Parse:       parseSession,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "compact",
		Description: "summarize and compact the conversation context now",
		Projection:  runtime.ProjCompact,
		Parse:       parseCompact,
	})
	// T7.7 slice 2 contract commands: /memory and /rebuild were REPL+TUI
	// intercepts — Parse here, Execute in the front-end's Executor (Memory/
	// Rebuild hooks). /rebuild also declares the TUI's off-the-loop projection
	// (ProjRebuild); /memory needs none — both front-ends render the report
	// through the hook.
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "memory",
		Description: "show the persistent-memory + infinite-context report",
		Parse:       parseMemory,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "rebuild",
		Description: "reconstruct the conversation context from the persisted checkpoint",
		Projection:  runtime.ProjRebuild,
		Parse:       parseRebuild,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "help",
		Description: "list available slash commands",
		Action: func(string) string {
			color := ui.Enabled()
			var b strings.Builder
			b.WriteString(ui.Colorize(color, ui.Bold, "available commands:"))
			for _, c := range reg.List() {
				b.WriteString("\n  ")
				b.WriteString(ui.Colorize(color, ui.Cyan, "/"+c.Name))
				rest := ""
				if c.ArgumentHint != "" {
					rest += " " + c.ArgumentHint
				}
				if c.Description != "" {
					rest += " - " + c.Description
				}
				rest += " (source: " + c.Tier.String() + ")"
				b.WriteString(ui.Colorize(color, ui.Dim, rest))
			}
			return b.String()
		},
	})
	// Loop-face declarations (T7.7 slice 2): the former stub table, now
	// identity + Projection with no Action closure. /help and the completion
	// menu render from these declarations; a front-end that cannot project a
	// face gets the explicit unavailability notice from ResolveOutcome —
	// never the old silent stub no-op.
	//
	//   - exit/quit terminate the front-end loop (both interactive faces).
	//   - sessions/resume/rename/context carry TUI faces; the REPL resolves
	//     them to the TUI-face notice until its own projections land
	//     (slice 3). remote-control/rewind carry TUI faces too, and the REPL
	//     loop still intercepts them ahead of resolution with its own
	//     implementations.
	//   - The REPL-face commands (fork … dream) execute in the REPL loop
	//     only; the TUI rejects them explicitly (pinned by the TUI's phantom
	//     rejection test).
	for _, c := range []runtime.SlashCommand{
		{Name: "exit", Description: "exit pigo", Projection: runtime.ProjQuit},
		{Name: "quit", Description: "exit pigo", Projection: runtime.ProjQuit},
		{Name: "sessions", Description: "open the session picker: browse, resume or delete sessions", Projection: runtime.ProjSessionsPicker},
		{Name: "resume", Description: "alias of /sessions: open the session picker", Projection: runtime.ProjSessionsPicker},
		{Name: "rename", Description: "rename the session's display title (terminal title follows)", ArgumentHint: "<title|--auto>", Projection: runtime.ProjRename},
		{Name: "context", Description: "toggle the context-usage overlay panel", Projection: runtime.ProjContextPanel},
		{Name: "remote-control", Description: "mirror this session to a phone/browser on your LAN: /remote-control [stop|status]", Projection: runtime.ProjRemoteControl},
		{Name: "rewind", Description: "roll files and the conversation back to before an earlier turn: /rewind [n]", Projection: runtime.ProjRewind},
		{Name: "fork", Description: "branch from a historical message into a new session: /fork [n]", Projection: runtime.ProjREPLFace},
		{Name: "clone", Description: "duplicate the current session into an independent branch", Projection: runtime.ProjREPLFace},
		{Name: "tree", Description: "show the session branch tree; switch active branch: /tree [n]", Projection: runtime.ProjREPLFace},
		{Name: "export", Description: "export the session to a file: /export [path.jsonl|path.html]", Projection: runtime.ProjREPLFace},
		{Name: "import", Description: "import a JSONL export as a new session: /import <path.jsonl>", Projection: runtime.ProjREPLFace},
		{Name: "copy", Description: "copy the most recent assistant reply to the clipboard", Projection: runtime.ProjREPLFace},
		{Name: "goal", Description: "run autonomously toward a goal: /goal [--tokens N] <objective> | pause | resume | clear", Projection: runtime.ProjREPLFace},
		{Name: "btw", Description: "ask a quick side question without touching the main conversation: /btw <question> (kept in a hidden peek session; bare /btw reopens the last one)", Projection: runtime.ProjREPLFace},
		{Name: "dream", Description: "consolidate memory now (dedupe, merge, prune, distill); /dream --dry-run previews without writing", Projection: runtime.ProjREPLFace},
	} {
		reg.AddBuiltin(c)
	}
}

// validThinkingLevel reports whether s is one of the known reasoning-effort
// levels and returns the typed value. It mirrors the enum in agentcore so a
// /think argument can be validated without importing the config layer.
func validThinkingLevel(s string) (agentcore.ThinkingLevel, bool) {
	switch agentcore.ThinkingLevel(s) {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return agentcore.ThinkingLevel(s), true
	default:
		return "", false
	}
}

// presetListing renders the preset provider/model catalog for /models. With an
// argument it filters to a single provider (e.g. "/models nvidia"). Providers
// are grouped and shown with the env var their API key is read from (referenced
// by name only, never a value). The output guides the user to `/model <id>`.
func presetListing(filter string) string {
	var b strings.Builder
	b.WriteString("preset providers & models (switch with /model <id>):")
	shown := 0
	for _, pv := range provider.PresetProviders {
		if filter != "" && !strings.EqualFold(filter, pv.Name) {
			continue
		}
		models := provider.PresetsByProvider(pv.Name)
		if len(models) == 0 {
			continue
		}
		shown++
		b.WriteString("\n\n")
		b.WriteString(pv.Name)
		if pv.EnvVar != "" {
			b.WriteString(" (API key: $")
			b.WriteString(pv.EnvVar)
			b.WriteString(")")
		} else {
			b.WriteString(" (local, no API key)")
		}
		for _, m := range models {
			b.WriteString("\n  ")
			b.WriteString(m.ID)
			if m.DisplayName != "" {
				b.WriteString("  — ")
				b.WriteString(m.DisplayName)
			}
		}
	}
	if shown == 0 {
		if filter != "" {
			return fmt.Sprintf("no preset provider named %q (try openrouter, nvidia, or ollama)", filter)
		}
		return "no presets configured"
	}
	return b.String()
}
