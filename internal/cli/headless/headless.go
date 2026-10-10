// This file is the headless run driver: the print / stream-json run path
// (US-020) extracted from the CLI dispatch seam (#363). dispatch resolves the
// output mode and the run environment, then hands off to Run, which wires the
// session, prompt, thinking level, and provider credentials into a
// runtime.HeadlessConfig and executes one run. Plugin slash commands and output
// mode parsing live here because they are specific to the headless path.
package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/shellguard"
	"github.com/smallnest/pigo/internal/trust"
)

// RunParams carries the resolved inputs for one headless run. Mode and Env are
// resolved by the caller (dispatch) — Mode via ParseOutputMode, Env via
// run.SetupEnv — so their distinct exit codes stay at the call site; Run owns
// the rest of the run lifecycle.
type RunParams struct {
	Mode          runtime.HeadlessMode
	Env           run.Env
	Prompt        string
	Model         string
	APIKey        string
	ThinkingLevel string
	ResumeID      string
	// Shellguard is the resolved bash-command static-analysis mode (T2.1).
	// Off (the default) installs no seam. Headless has no interactive
	// channel, so flagged commands follow NonInteractiveDenial.
	Shellguard shellguard.Mode
	// NonInteractiveDenial is "terminate" (default) or "continue"
	// (--non-interactive-denial continue): a shellguard denial either aborts
	// the run or becomes a failed tool result the agent can route around.
	NonInteractiveDenial string
	// Permissions is the user's [permissions] rules table (T5.2), loaded
	// into the permission engine alongside the persisted permissions file.
	Permissions config.PermissionsConfig
}

// Run executes one headless run over p.Prompt, writing agent output to out and
// diagnostics to errOut, and returns a process exit code (0 = success). The run
// is backed by a session so its id appears in the first stream-json event and it
// can be resumed with --resume/--continue; a resumed session seeds its prior
// messages ahead of the new prompt.
func Run(ctx context.Context, p RunParams, out, errOut io.Writer) int {
	env := p.Env
	// Built-in slash commands need a loop (-p has none): a picker or a
	// loop-owned command cannot be projected, and the former behavior sent the
	// literal text to the model as a prompt (T7.7 §4.6). Refuse explicitly
	// before the plugin path, so a built-in still wins a same-name collision
	// exactly as it does in the registry.
	if msg := refuseBuiltinSlash(p.Prompt); msg != "" {
		fmt.Fprintf(errOut, "pigo: %s\n", msg)
		return 2
	}
	// Best-effort plugin slash-command support in headless mode: if the prompt is
	// a "/cmd ..." naming a plugin command, invoke it, print its notifications to
	// errOut, and use the returned prompt for this run (appending the raw args if
	// the command produced no prompt). Headless has no turn injection, so
	// appending the returned prompt is the accepted behavior. A non-plugin prompt
	// or unknown command is left untouched.
	headlessPrompt := resolveHeadlessPluginCommand(p.Prompt, env.Plugins, errOut)
	promptContent, err := ui.BuildUserContent(headlessPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}

	// Back the headless run with a session so its id appears in the first
	// stream-json event and the run can be resumed with --resume/--continue,
	// matching the interactive REPL and pi/Claude Code. A resumed session seeds
	// its prior messages ahead of the new prompt.
	priorMsgs, hs, err := openHeadlessSession(p.ResumeID, p.Model, env.ProviderName, env.SysPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	// Bind the sub-agent transcript store to this session (T7.1) so a task
	// dispatched from a headless run settles into this session's sidecar and can
	// be resumed by a later run. A nil store keeps the resume face off.
	env.Subagents.BindSession(hs.header.ID)
	// Bind the usage ledger to the same session (O1/T7.3c) so its per-turn
	// records (sub-agent turns included) accrue to this session's file.
	env.Usage.BindSession(hs.header.ID)
	messages := append(priorMsgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: promptContent})
	agentCtx := &agentcore.AgentContext{
		SystemPrompt: hs.header.SystemPrompt,
		Messages:     messages,
		Tools:        env.Tools,
	}

	// Resolve the effective reasoning-effort level through the layered config
	// chain (default < global < project < env < --thinking-level flag).
	thinking, err := run.ResolveThinkingLevel(p.ThinkingLevel)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 2
	}

	// Resolve the API key by provider name from the environment (never logged).
	// An explicit --api-key overrides env/config for the resolved provider.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(env.ProviderName, p.APIKey)
	runCfg := run.NewConfig(p.Model, env.ProviderName, thinking, env.Provider, creds, run.ToolRegistry(env.Tools), run.TodoReminders(env.Tools), env.Schedule, env.ToolPlan)
	// Compaction parity with the interactive drivers (T3.3 closeout finding):
	// run.NewConfig leaves ContextWindow and Compaction zero, and the REPL/TUI
	// seed both on their live config themselves — headless print did neither,
	// so both microcompaction (ContextWindow<=0 short-circuits) and full
	// compaction (Enabled=false) were silently off in print mode. Resolve the
	// window the same way the interactive drivers do: the model's catalog
	// window (fallback cli.DefaultContextWindow) lowered by an explicit
	// [compaction] max_context.
	runCfg.ContextWindow = cli.ResolveContextWindow(env.Provider, p.Model, env.MaxContext)
	runCfg.MaxOutputTokens = cli.ResolveMaxOutputTokens(env.Provider, p.Model)
	runCfg.Compaction = compaction.DefaultCompactionSettings
	runCfg.SessionID = hs.header.ID
	// Account each turn's usage into the session ledger (O1/T7.3c). The sink is
	// the session-bound recorder, so a later run (or the interactive front-ends)
	// reads the same cumulative numbers.
	runCfg.RecordUsage = env.Usage.Record
	// Route auto-compaction checkpoints to the shared memory root so a rebuild can
	// recover the pre-watermark prefix (no-op when memory is disabled → empty root).
	runCfg.MemoryRoot = run.MemoryRootFromTools(env.Tools)

	// Wire hooks uniformly with every other driver (#425): resolve the trust-gated
	// hook set, install the tool-execution + Stop seams, dispatch SessionStart, and
	// chain the SessionEnd/PreCompact observer onto the plugin event notifier. A
	// malformed hook layer is a config error (exit 2), matching thinking-level.
	source := "startup"
	if p.ResumeID != "" {
		source = "resume"
	}
	set, herr := run.ResolveHookSet(env.Cwd, run.Trusted(env.Cwd))
	if herr != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", herr)
		return 2
	}
	hookDeps := run.HookDeps{SessionID: hs.header.ID, ProjectDir: env.Cwd, WarnLog: errOut}
	// Deliver agent lifecycle events to any subscribed plugin (US-017, #133).
	// NewEventNotifier returns nil when no plugin subscribes, so the base handler
	// stays nil in the common no-plugin case.
	var baseOnEvent func(agentcore.AgentEvent)
	if n := plugin.NewEventNotifier(env.Plugins, errOut); n != nil {
		baseOnEvent = n.Handle
	}
	d, onEvent := run.InstallDriverHooks(ctx, &runCfg, set, hookDeps, source, baseOnEvent)

	// Shellguard denial seam (T2.1): headless has no interactive channel, so
	// Hazardous/Incomplete bash commands follow --non-interactive-denial —
	// terminate (default) cancels the run context so the loop stops after the
	// denial lands, continue keeps the run going so the agent can route
	// around the refusal. Chained after the hook seam: a hook block wins, an
	// allow falls through to the analysis. A nil seam (mode off) is a no-op.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	sgSeam, sgTerminated := agenttool.ShellguardDenialSeam(p.Shellguard, agenttool.ShellKindFromTools(env.Tools), p.NonInteractiveDenial == "continue", runCancel)
	if sgSeam != nil {
		runCfg.Batch.ToolExecutorConfig.BeforeToolCall = agenttool.ChainBeforeToolCall(runCfg.Batch.ToolExecutorConfig.BeforeToolCall, sgSeam)
	}
	// Permission engine (T5.2): rules + effect contract + self-edit guard,
	// chained after the shellguard denial seam. There is no interactive
	// channel, so the ask step fails closed — an untrusted directory's
	// effect calls become failed tool results the agent can route around
	// (the continue posture); read-only tools, allow rules, and trusted
	// directories pass unchanged. A config or store error is a usage-level
	// failure (exit 2): a boundary the user believes is in force must not
	// silently vanish.
	var trustMgr *trust.Manager
	if m, merr := trust.NewManager(trust.DefaultPath()); merr == nil {
		trustMgr = m
	}
	permEngine, engineErr := run.BuildPermissionEngine(env.Cwd, env.Tools, p.Permissions, nil,
		func(cwd string) bool { return trustMgr != nil && trustMgr.IsTrusted(cwd) }, nil)
	if engineErr != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", engineErr)
		return 2
	}
	runCfg.Batch.ToolExecutorConfig.BeforeToolCall = agenttool.ChainBeforeToolCall(runCfg.Batch.ToolExecutorConfig.BeforeToolCall, permEngine.BeforeToolCall)
	// UserPromptSubmit runs before the prompt is handed to the loop: a block aborts
	// the headless run non-zero; additionalContext is injected into this run only.
	if d != nil {
		if block, reason := run.DispatchUserPromptSubmit(ctx, d, &runCfg, hookDeps, headlessPrompt); block {
			fmt.Fprintf(errOut, "pigo: prompt blocked by hook: %s\n", reason)
			return 1
		}
	}

	cfg := runtime.HeadlessConfig{
		Mode: p.Mode,
		Out:  out,
		Run:  runCfg,
	}
	cfg.OnEvent = onEvent
	runErr := runtime.RunHeadless(runCtx, agentCtx, cfg)
	// Persist the run's messages regardless of run outcome so a partial run is
	// still resumable; a persistence failure is reported but does not mask a run
	// error.
	if perr := hs.persist(agentCtx); perr != nil {
		fmt.Fprintf(errOut, "pigo: warning: could not persist session %s: %v\n", hs.header.ID, perr)
	}
	if runErr != nil {
		if sgTerminated != nil && sgTerminated() {
			fmt.Fprintln(errOut, "pigo: run terminated by a shellguard denial (non-interactive; --non-interactive-denial continue lets the agent route around denials)")
			return 1
		}
		fmt.Fprintf(errOut, "pigo: %v\n", runErr)
		return 1
	}
	return 0
}

// builtinSlashCatalog is the identity catalog the print-mode slash guard
// resolves against: the built-in declarations only (the live, config-surface
// and loop-face entries), with no live wiring. User prompt templates, skills
// and plugin commands are not built-ins and stay on the prompt path.
var builtinSlashCatalog = prompts.BuiltinCatalog()

// refuseBuiltinSlash returns the explicit refusal for a print-mode prompt that
// names a built-in slash command, or "" when the prompt should run as a normal
// turn (plain text, an unknown "/name", a prompt template, a skill command, or
// a plugin command). -p has no loop: an interactive face has nothing to project
// and a loop-owned command has no state to act on, so executing neither and
// sending the literal text to the model ("/status" as a prompt) is the defect
// this closes (T7.7 §4.6 — execution or an explicit message, never a silently
// misread prompt).
func refuseBuiltinSlash(prompt string) string {
	name, _, ok := runtime.SplitInvocation(prompt)
	if !ok {
		return ""
	}
	cmd, found := builtinSlashCatalog.Lookup(name)
	if !found {
		return ""
	}
	if cmd.Projection != runtime.ProjNone {
		return fmt.Sprintf("/%s needs an interactive terminal (run pigo for the TUI or pigo --no-tui for the REPL); -p runs one prompt and does not dispatch built-in slash commands", name)
	}
	return fmt.Sprintf("/%s is a built-in slash command; -p runs one prompt and does not dispatch built-ins (run pigo --no-tui for the REPL)", name)
}

// resolveHeadlessPluginCommand gives the headless / print path best-effort
// support for plugin slash commands. When prompt is a "/cmd ..." naming a
// plugin command (from mgr.Commands()), it invokes the command, prints each
// returned notification to notifyOut, and returns the command's returned Prompt
// as the run's prompt. If the command returns no prompt, the raw argument text
// is used instead (so a bare "/cmd" with only notifications still runs
// something sensible rather than an empty prompt). Any other input — a
// non-command, an unknown command, or a call error — leaves prompt unchanged so
// the normal headless run proceeds. mgr may be nil (no plugins).
//
// Headless has no turn-injection loop, so "inject the returned prompt" degrades
// to "use the returned prompt for this run", which the acceptance criteria
// permit.
func resolveHeadlessPluginCommand(prompt string, mgr *plugin.Manager, notifyOut io.Writer) string {
	if mgr == nil || !strings.HasPrefix(strings.TrimLeft(prompt, " \t"), "/") {
		return prompt
	}
	trimmed := strings.TrimLeft(prompt, " \t")[1:]
	name := trimmed
	args := ""
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		name = trimmed[:i]
		args = strings.TrimSpace(trimmed[i+1:])
	}
	for _, pc := range mgr.Commands() {
		if pc.Spec.Name != name {
			continue
		}
		// Encode the raw arg text as a JSON string (never null), matching the
		// host's CommandCallParams.Args contract.
		raw, _ := json.Marshal(args)
		res, err := pc.Plugin.CallCommand(context.Background(), name, json.RawMessage(raw))
		if err != nil {
			fmt.Fprintf(notifyOut, "pigo: plugin command %q failed: %v\n", name, err)
			return prompt
		}
		for _, n := range res.Notifications {
			if n.Type != "" {
				fmt.Fprintf(notifyOut, "[%s] %s\n", n.Type, n.Message)
			} else {
				fmt.Fprintln(notifyOut, n.Message)
			}
		}
		if res.Prompt != "" {
			return res.Prompt
		}
		return args
	}
	return prompt
}

// ParseOutputMode maps the --output-format flag onto a HeadlessMode, erroring on
// an unknown value.
func ParseOutputMode(outputFmt string) (runtime.HeadlessMode, error) {
	switch outputFmt {
	case "text", "":
		return runtime.PrintMode, nil
	case "stream-json":
		return runtime.StreamJSONMode, nil
	default:
		return 0, fmt.Errorf("unknown --output-format %q (want text|stream-json)", outputFmt)
	}
}
