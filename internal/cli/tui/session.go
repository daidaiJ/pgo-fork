// This file binds the full-screen TUI to the real agent run seam and the local
// session store (US-009, FR-16/17). It is the TUI counterpart to the REPL's
// replDeps + streamRun + cli.PersistTurn plumbing (internal/cli/repl): it
// assembles an AgentContext + RunConfig from the model's Options, feeds them to
// the event bridge (bridge.go's startRun → runtime.StartRun/DrainStream), and
// persists the growing conversation to ~/.pigo/sessions after each turn.
//
// It deliberately imports the SHARED lower-level packages the REPL also uses
// (session, runtime, provider, cli, cli/run, cli/headless, cli/ui) rather than
// the repl package itself, so the two entry paths share one store and one
// run-config shape without an import cycle (repl and tui are siblings; prompts
// imports tui, so tui must not reach back into repl/prompts).
package tui

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/memory"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/shellguard"
	"github.com/smallnest/pigo/internal/spans"
	"github.com/smallnest/pigo/internal/tooldecl"
	"github.com/smallnest/pigo/internal/trust"
)

// runSession holds the assembled per-session state for a TUI run: the persisted
// store + header, the growing conversation context, the live (mutable) run
// config, and the tool/credential collaborators. It mirrors the subset of
// repl.replDeps the TUI needs, and owns the same session-tree cursor bookkeeping
// (curLeaf / persisted) so each turn is persisted as a branch rather than a
// flattening rewrite.
type runSession struct {
	store     *session.Store
	header    session.SessionHeader
	agentCtx  *agentcore.AgentContext
	live      *cli.LiveConfig
	reg       *agenttool.ToolRegistry
	reminders *runtime.ReminderRegistry
	schedule  *agenttool.Schedule
	creds     *provider.CredentialStore
	// toolPlan is the run's deferred tool declaration plan (T4.1); nil keeps
	// declaration direct. buildConfig mounts it on every turn's RunConfig.
	toolPlan *tooldecl.Plan

	// cwd is the directory pigo was launched in, captured once at session
	// assembly. It is the trust key and the /status environment display.
	cwd string
	// trust persists project-trust decisions (US-018, #134). It is nil when
	// trust is disabled (store could not be loaded / no cwd); when nil /status
	// reports "disabled" and the trust-gated hook layer is skipped.
	trust *trust.Manager
	// shellguard is the resolved bash-command static-analysis mode (T2.1).
	// Off (default) leaves the seam uninstalled; with no per-call channel in
	// the TUI, ask/strict deny flagged bash commands outright (fail closed).
	shellguard shellguard.Mode
	// slash is the shared slash-command registry the TUI consults exactly as the
	// REPL does. It is assembled per-session against the live config (withSession
	// rebinds the model's registry to this one) so /model switches reach it.
	slash *runtime.SlashRegistry
	// telemetry holds the retained per-run telemetry events (US-001, #291) and
	// the cumulative accumulator that sums metrics across all runs in the
	// session. The run loop folds each run's TelemetryEvent into it; /status
	// reads it back through the Host contract.
	telemetry *cli.TelemetryHolder

	// memoryRoot is the persistent-memory Store root (empty when memory is
	// disabled). It routes auto-compaction checkpoints and /rebuild recovery to
	// <memoryRoot>/sessions/<id>/, the canonical checkpoint location.
	memoryRoot string
	// memstore is the live persistent-memory Store (nil when memory is disabled).
	// It lets /memory inspect entry counts without re-opening the database.
	memstore *memory.Store

	// dispatcher is the session's hook dispatcher, nil when no hooks are
	// configured (FR-18). hookDeps carries the session id / project dir stamped
	// onto every HookInput and hook process environment.
	dispatcher *hooks.Dispatcher
	hookDeps   run.HookDeps
	// onEvent is the observer chain delivered to every run: the plugin notifier
	// (US-017) with the SessionEnd/PreCompact hook notifier chained after it.
	onEvent func(agentcore.AgentEvent)

	// curLeaf is the id of the on-disk entry the next turn descends from; persisted
	// is the number of agentCtx.Messages already written. persist() appends only
	// Messages[persisted:] as a branch from curLeaf (see cli.PersistTurn).
	curLeaf   string
	persisted int

	// cancelRun cancels the in-flight run's context; startRun sets it and the
	// two-stage interrupt (Model.interruptFn → interrupt) calls it. It is nil
	// before the first run and after a run is cancelled.
	cancelRun context.CancelFunc

	// lastBtw is the /btw side thread's context from this process and
	// lastBtwBase the background-message index it diverged from. Both are
	// carried on the Host contract for parity with the REPL; the TUI does not
	// run /btw today, so they stay nil/0.
	lastBtw     *agentcore.AgentContext
	lastBtwBase int

	// remote owns the running remote-control server+bridge (remotecontrol.go),
	// nil when /remote-control is off. buildConfig reads it to install the remote
	// confirm seam so risky tool calls route to the paired browser while connected.
	remote *remoteSession
}

// newRunSession assembles the run session from the resolved Options, opening the
// shared ~/.pigo/sessions store. When Options carries a ResumeID it loads that
// session's entries and rebuilds the context (the returned history seeds the
// replayed transcript); otherwise it starts a fresh session with a new header.
// It is the production entry; newRunSessionWithStore holds the store-agnostic
// core so tests can drive it against a temp-dir store.
func newRunSession(opts Options) (*runSession, []agentcore.Message, error) {
	store, err := headless.SessionStore()
	if err != nil {
		return nil, nil, err
	}
	return newRunSessionWithStore(store, opts)
}

// newRunSessionWithStore is the store-agnostic core of newRunSession: given an
// already-opened store it resolves resume-vs-fresh, builds the live config and
// collaborators, and returns the session plus the resumed history (nil for a
// fresh session).
func newRunSessionWithStore(store *session.Store, opts Options) (*runSession, []agentcore.Message, error) {
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(opts.ProviderName, opts.APIKey)

	// cwd is the launch directory, captured once: it stamps fresh sessions, is
	// the trust key, and feeds /status's environment section and the hook layer.
	cwd, _ := os.Getwd()

	now := time.Now().UTC()
	var (
		agentCtx *agentcore.AgentContext
		header   session.SessionHeader
		history  []agentcore.Message
		curLeaf  string
	)
	if opts.ResumeID != "" {
		// startup.session_load (T1.1): LoadEntries on a large session file is a
		// prime slow-startup suspect, so it gets its own span.
		loadSpan := spans.Begin("startup.session_load")
		h, entries, err := store.LoadEntries(opts.ResumeID)
		loadSpan.End()
		if err != nil {
			return nil, nil, err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		if len(entries) > 0 {
			curLeaf = entries[len(entries)-1].ID
		}
		header = h
		sysPrompt := h.SystemPrompt
		if sysPrompt == "" {
			sysPrompt = opts.SysPrompt
		}
		agentCtx = &agentcore.AgentContext{SystemPrompt: sysPrompt, Messages: msgs, Tools: opts.Tools}
		history = msgs
	} else {
		agentCtx = &agentcore.AgentContext{SystemPrompt: opts.SysPrompt, Tools: opts.Tools}
		// Stamp the launch directory onto a fresh session (#526/#524) so the
		// session is attributed to a project and a later /dream pass can distill it
		// under the right scope, mirroring headless/REPL. An unresolvable cwd
		// yields "" (session stays unattributed) rather than aborting.
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

	live := &cli.LiveConfig{
		Model:         opts.Model,
		ProviderName:  opts.ProviderName,
		Provider:      opts.Provider,
		BaseURL:       opts.BaseURL,
		Protocol:      opts.Protocol,
		ThinkingLevel: opts.ThinkingLevel,
		MaxContext:    opts.MaxContext,
		ContextWindow:   cli.ResolveContextWindow(opts.Provider, opts.Model, opts.MaxContext),
		MaxOutputTokens: cli.ResolveMaxOutputTokens(opts.Provider, opts.Model),
	}

	// Project trust (US-018, #134): load the persisted trust store for the
	// launch directory, mirroring the REPL. A load failure (or an unresolvable
	// cwd) is non-fatal: trust is disabled (mgr stays nil) and the TUI still
	// runs — the store is surfaced rather than silently overwritten.
	mgr, mgrErr := trust.NewManager(trust.DefaultPath())
	if mgrErr != nil {
		fmt.Fprintf(os.Stderr, "pigo: trust store unavailable, trust disabled: %v\n", mgrErr)
		mgr = nil
	}
	if cwd == "" && mgr != nil {
		fmt.Fprintf(os.Stderr, "pigo: cannot resolve working directory, trust disabled\n")
		mgr = nil
	}

	s := &runSession{
		store:      store,
		header:     header,
		agentCtx:   agentCtx,
		live:       live,
		reg:        run.ToolRegistry(opts.Tools),
		reminders:  run.WithRunawayGuard(run.TodoReminders(opts.Tools)),
		schedule:   agenttool.ScheduleFromTools(opts.Tools),
		toolPlan:   opts.ToolPlan,
		creds:      creds,
		cwd:        cwd,
		trust:      mgr,
		shellguard: opts.Shellguard,
		slash:      newSlashRegistry(opts, live),
		telemetry:  cli.NewTelemetryHolder(),
		curLeaf:    curLeaf,
		persisted:  len(history),
		memoryRoot: run.MemoryRootFromTools(opts.Tools),
		memstore:   run.MemoryStoreFromTools(opts.Tools),
	}
	// /trust is a per-session command (its closure captures mgr + cwd), so it is
	// registered here rather than in newSlashRegistry. A nil mgr is a no-op.
	trust.RegisterCommand(s.slash, mgr, cwd)

	// Wire hooks uniformly with every other driver (#425): resolve the trust-gated
	// hook set, build the dispatcher, dispatch SessionStart once, and compose the
	// SessionEnd/PreCompact observer with the plugin notifier. Trust is granted by
	// --approve (Options.Approve) or the shared trust store; project-layer hooks
	// only apply when trusted (FR-14). A malformed hook layer disables hooks with a
	// warning rather than failing the TUI launch.
	s.hookDeps = run.HookDeps{SessionID: header.ID, ProjectDir: cwd, WarnLog: os.Stderr}
	trusted := opts.Approve || (mgr != nil && mgr.IsTrusted(cwd))
	var baseOnEvent func(agentcore.AgentEvent)
	if n := plugin.NewEventNotifier(opts.Plugins, os.Stderr); n != nil {
		baseOnEvent = n.Handle
	}
	if set, err := run.ResolveHookSet(cwd, trusted); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: hooks disabled: %v\n", err)
		s.onEvent = baseOnEvent
	} else if d := run.BuildDispatcher(set, s.hookDeps); d != nil {
		s.dispatcher = d
		if s.reminders == nil {
			s.reminders = runtime.NewReminderRegistry()
		}
		ssCfg := runtime.RunConfig{Reminders: s.reminders}
		run.DispatchSessionStart(context.Background(), d, &ssCfg, s.hookDeps, sessionStartSource(opts))
		s.reminders = ssCfg.Reminders
		n := hooks.NewHookNotifier(d, s.hookDeps.SessionID, s.hookDeps.ProjectDir)
		s.onEvent = chainTUIEvent(baseOnEvent, n.Handle)
	} else {
		s.onEvent = baseOnEvent
	}
	return s, history, nil
}

// sessionStartSource maps the resolved run options to the SessionStart source
// tag: "resume" when continuing an existing session, "startup" otherwise.
func sessionStartSource(opts Options) string {
	if opts.ResumeID != "" {
		return "resume"
	}
	return "startup"
}

// chainTUIEvent composes the plugin notifier with the hook notifier into one
// observer; a nil operand is identity.
func chainTUIEvent(prev, next func(agentcore.AgentEvent)) func(agentcore.AgentEvent) {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ev agentcore.AgentEvent) {
		prev(ev)
		next(ev)
	}
}

// buildConfig assembles the RunConfig for one turn from the live config and
// collaborators. It replicates repl.streamRun's assembly (same LoopConfig fields,
// tool registry and reminders) minus the interactive trust confirmation hook: the
// TUI has no stdin prompt to confirm side-effect tool calls on, so tools run
// under the trust granted up front by --approve (Options.Approve) rather than a
// per-call BeforeToolCall prompt. The stream fn is derived from the live provider
// and the API key resolved through the credential store, exactly as the REPL does.
func (s *runSession) buildConfig() runtime.RunConfig {
	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         s.live.Model,
			Provider:      s.live.ProviderName,
			ThinkingLevel: s.live.ThinkingLevel,
			Stream:        provider.StreamFnFromProvider(s.live.Provider),
			GetAPIKey:       s.creds.GetAPIKey,
			ContextWindow:   s.live.ContextWindow,
			MaxOutputTokens: s.live.MaxOutputTokens,
			Compaction:      compaction.DefaultCompactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{
				Registry: s.reg,
			},
		},
		Reminders:  s.reminders,
		SessionID:  s.header.ID,
		MemoryRoot: s.memoryRoot,
		// Deferred tool declaration (T4.1): nil plan = direct, zero overhead.
		ToolDeclaration: s.toolPlan,
	}
	// T3.3: compaction needs the persisted-message cursor to place its marker at
	// the branch tip (after the last persisted entry), keeping the tree
	// append-only under persist()'s plain tail append.
	cfg.PersistedCount = func() int { return s.persisted }
	// Per-turn wiring of the tool-execution + Stop seams; nil dispatcher is a
	// no-op so the hot path pays nothing when no hooks are configured (FR-18).
	if s.dispatcher != nil {
		run.InstallSeams(&cfg, s.dispatcher, s.hookDeps)
	}
	// Due session-local reminders ride the follow-up seam (issue #565); a nil
	// scheduler (no schedule_* tools) leaves the seam unwired.
	if s.schedule != nil {
		cfg.GetFollowUpMessages = s.schedule.FollowUpMessages
	}
	// When remote control is active, route side-effect tool-call confirmations to
	// the paired browser (no-op when no client is connected or the cwd is trusted,
	// so the non-remote path is unchanged). The trust manager is read from the
	// shared store; a nil manager disables the seam.
	if s.remote != nil {
		if mgr, err := trust.NewManager(trust.DefaultPath()); err == nil {
			cfg.Batch.ToolExecutorConfig.BeforeToolCall = remoteConfirmSeam(s.remote, mgr, s.hookDeps.ProjectDir)
		}
	}
	// Shellguard (T2.1): the TUI has no per-call confirmation channel, so
	// ask and strict modes both deny flagged bash commands outright (fail
	// closed). Off installs nothing. The seam runs ahead of the remote
	// confirm seam so a Safe command falls through unchanged.
	if sg := agenttool.ShellguardSeam(s.shellguard, nil); sg != nil {
		cfg.Batch.ToolExecutorConfig.BeforeToolCall = agenttool.ChainBeforeToolCall(sg, cfg.Batch.ToolExecutorConfig.BeforeToolCall)
	}
	return cfg
}

// rebuildDoneMsg reports the outcome of a manual /rebuild to the model: summary
// is the status line to show in the transcript, err is set when the rebuild
// failed (the context is then left unchanged).
type rebuildDoneMsg struct {
	summary string
	err     error
}

// rebuildCmd runs a context rebuild off the tea loop (the no-checkpoint fallback
// makes a summarization LLM call, so it must not block the UI goroutine) and
// yields a rebuildDoneMsg the model folds into the transcript. It mirrors the
// REPL's runManualRebuild.
func (s *runSession) rebuildCmd() tea.Cmd {
	return func() tea.Msg {
		summary, err := s.rebuild()
		return rebuildDoneMsg{summary: summary, err: err}
	}
}

// rebuild reconstructs the shared context from the session's persisted checkpoint
// (inserting a compaction marker at the watermark — the request view collapses
// the pre-watermark prefix), falling back to summarizing compaction when no
// checkpoint exists. It replaces agentCtx.Messages in place on success; the
// inserted marker rides the next persist()'s plain tail append (T3.3 marker
// model — no flatten, no linear re-save).
func (s *runSession) rebuild() (string, error) {
	msgs := s.agentCtx.Messages
	before := compaction.EstimateContextTokens(compaction.ProjectView(msgs)).Tokens
	// Checkpoints live under <memoryRoot>/sessions/<id>/; recover from the same
	// root the loop writes to. Empty when memory is disabled — RebuildFromCheckpoint
	// then falls back to summarizing compaction.
	memoryRoot := s.memoryRoot
	cfg := s.buildConfig()
	res, err := runtime.RebuildFromCheckpoint(context.Background(), msgs, s.header.ID, memoryRoot, &cfg, nil)
	if err != nil {
		return "", err
	}
	if res.NoOp {
		return fmt.Sprintf("nothing to rebuild (%d tokens, %d messages)", before, len(msgs)), nil
	}
	s.agentCtx.Messages = res.Messages
	source := "checkpoint"
	if !res.FromCheckpoint {
		source = "compaction (no checkpoint)"
	}
	return fmt.Sprintf("context rebuilt from %s: %d → %d tokens, collapsed %d messages, kept %d",
		source, res.TokensBefore, res.TokensAfter, res.SummarizedCount, res.KeptCount), nil
}

// prompt to the growing context as a user message, then hands the context and a
// freshly-built config to the event bridge (bridge.startRun → runtime.StartRun +
// DrainStream on a goroutine), returning the bridge channel and the first
// waitForEvent Cmd so Update can pump the run's events. The context grows in
// place (agentCtx is a pointer), so the next turn continues the conversation.
func (s *runSession) startRun(prompt string) (chan tea.Msg, tea.Cmd) {
	content, err := ui.BuildUserContent(prompt)
	if err != nil {
		// A malformed image reference must not swallow the turn: fall back to the
		// raw prompt as plain text so the run still starts.
		content = agentcore.ContentList{agentcore.NewTextContent(prompt)}
	}
	// UserPromptSubmit runs before the prompt is committed to the context: a block
	// aborts the turn (emitting a runEndMsg carrying the reason) without leaving a
	// dangling user message; additionalContext is injected into this turn only.
	if s.dispatcher != nil {
		pc := runtime.RunConfig{Reminders: s.reminders}
		if block, reason := run.DispatchUserPromptSubmit(context.Background(), s.dispatcher, &pc, s.hookDeps, prompt); block {
			ch := newEventChan()
			go func() { ch <- runEndMsg{err: fmt.Errorf("prompt blocked by hook: %s", reason)} }()
			return ch, waitForEvent(ch)
		}
		s.reminders = pc.Reminders
	}
	s.agentCtx.Messages = append(s.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   content,
	})
	// Use a cancellable context so the two-stage interrupt (FR-14) can stop this
	// run: cancelling propagates through StartRun/DrainStream, which then emits a
	// runEndMsg and the model returns to idle.
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelRun = cancel
	return startRun(ctx, s.agentCtx, s.buildConfig(), s.onEvent)
}

// interrupt cancels the in-flight run, if any. It is bound to Model.interruptFn
// by withSession so pressing Esc / Ctrl+C while running stops the current run
// instead of quitting the program (FR-14). Safe to call when no run is active.
func (s *runSession) interrupt() {
	if s.cancelRun != nil {
		s.cancelRun()
	}
}

// persist writes the messages produced since the last persist as a new branch
// descending from the active leaf, advancing the leaf and the persisted cursor.
// It mirrors cli.PersistTurn: growing the on-disk tree with AppendBranch (rather
// than a linear rewrite) keeps history intact. A no-op when nothing new was
// produced, so an idle turn-end never regenerates entry ids.
//
// T3.3 marker-entry model: compaction inserts a marker into the live list
// (always after the persisted cursor) instead of rewriting it, so the plain
// tail append below carries the marker into the tree. The old flatten-and-save
// branch is gone — it was the defect that physically destroyed abandoned
// branches after every auto-compaction.
func (s *runSession) persist() error {
	tail := s.agentCtx.Messages[s.persisted:]
	if len(tail) == 0 {
		return nil
	}
	s.header.UpdatedAt = time.Now().UTC()
	s.header.Model = s.live.Model
	s.header.Provider = s.live.ProviderName
	leaf, err := s.store.AppendBranch(s.header, s.curLeaf, tail)
	if err != nil {
		return err
	}
	s.curLeaf = leaf
	s.persisted = len(s.agentCtx.Messages)
	return nil
}

// seedTranscript replays a resumed session's prior messages into the transcript
// so the user sees the conversation so far before re-prompting (the TUI analogue
// of repl.replayTranscript). User and assistant text become their respective
// blocks; assistant tool calls render as system lines (tool cards land in #389).
// Tool-result messages are omitted here — their content is echoed live during a
// run, and replaying raw results would clutter the resumed view.
func seedTranscript(t *transcript, history []agentcore.Message) {
	for _, m := range history {
		switch msg := m.(type) {
		case agentcore.UserMessage:
			if text := agentcore.ContentToText(msg.Content); text != "" {
				t.addUser(text)
			}
		case agentcore.AssistantMessage:
			if text := agentcore.ContentToText(msg.Content); text != "" {
				t.finalizeTurn(msg)
			}
			for _, c := range msg.ToolCalls() {
				t.addSystem("· " + c.Name)
			}
		}
	}
}
