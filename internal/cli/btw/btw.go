// This file implements the /btw command (mirrors Claude Code's /btw and the pi
// agent extension @narumitw/pi-btw): a throwaway "side thread" for asking the
// model a quick side question that must NOT pollute the main conversation.
//
// /btw is intercepted in the REPL loop rather than routed through a slash Action
// closure because it must run an agent stream and read the live main context —
// none of which a pure string→string Action can do, exactly like /compact and
// /goal. It reaches the session's collaborators and mutable state through the
// cli.Host contract and reads follow-up lines through cli.Editor, so it need not
// import the concrete replDeps aggregate that assembles them.
//
// Isolation contract (the whole point of the feature): a side thread runs on a
// COPY of the main conversation as background, and its question/answer are only
// ever appended to that copy — never to host.AgentCtx().Messages. The MAIN
// session is never written: no store.Save against it, no change to the persisted
// cursor / current leaf / header timestamp. Switching sessions discards the
// in-process side thread.
//
// Persistence (T4.3): since a side thread is worth keeping, its own Q&A is
// appended to a peek session — a separate peek_* session file that Store.List()
// hides, so it is durable and reopenable after a restart without ever showing up
// in --list-sessions / --continue / /dream / the TUI picker. Only the side Q&A
// lands there, never a copy of the main background, and a bare "/btw" in a later
// process recovers the thread from that file. When no store is available the
// pre-T4.3 behavior (memory-only, discarded on exit) still applies.
//
// Scope: /btw is intercepted in the REPL loop and runs a side question against a
// copy of the main context (#279); it supports multi-turn follow-ups in the same
// ephemeral thread (#280), bare-/btw reopen of the most recent side thread this
// process (#281), and an optional model/thinking override config (#282, see
// btw_config.go) that affects only the side thread.
package btw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/trust"
)

// btwHeader is the fixed banner shown when entering a side thread, so the user
// always knows the current input is a throwaway side question, not the main
// conversation (mirrors pi-btw's "btw · side thread" header).
const btwHeader = "btw · side thread"

// BtwHeader exposes the side-thread banner text so callers (and tests) can
// recognize it in output.
const BtwHeader = btwHeader

// btwPrompt is the input prompt shown for follow-up questions inside a side
// thread, distinguishing it from the main "pigo(model)>" prompt.
const btwPrompt = "btw> "

// RunBtw handles a /btw invocation. With an argument it starts a fresh side
// thread, asks that question, then enters a follow-up loop so the user can keep
// asking in the same ephemeral thread. Bare "/btw" reopens the most recent side
// thread from this process — replaying its Q&A history — and drops back into the
// follow-up loop; if none exists yet it guides the user to supply a question
// (US-004, #281). setCancel publishes the active run's cancel func so the REPL's
// SIGINT handler can interrupt the side run, reusing the same plumbing as a
// normal turn.
//
// The main context is never mutated: RunBtw builds a private side AgentContext
// seeded with a copy of the main messages, runs every turn against that copy,
// and returns without touching host.AgentCtx() or persisting anything. The side
// thread is retained in-process (host.LastBtw()) so a later bare /btw can reopen
// it, but it is never written to disk — restarting pigo discards it.
func RunBtw(setCancel func(context.CancelFunc), out io.Writer, host cli.Host, editor cli.Editor, line string) {
	question := strings.TrimSpace(strings.TrimPrefix(line, "/btw"))
	// Resolve the side thread's model/thinking once per invocation from the
	// session defaults overlaid with btw.json (#282). Re-read each call so an
	// edit takes effect next time with no restart.
	settings := ResolveBtwSettings(out, host)
	if question == "" {
		// Bare /btw: reopen the most recent side thread — from this process if it
		// is still live, otherwise from its peek session file on disk (T4.3) —
		// replaying its history; when there is none, guide the user to ask one.
		side, peek, ok := reopenSideThread(out, host)
		if !ok {
			fmt.Fprintln(out, "usage: /btw <question> — ask a quick side question without touching the main conversation")
			return
		}
		printBtwHeader(out)
		replaySideHistory(out, side, host.LastBtwBase())
		if editor != nil {
			btwFollowUpLoop(setCancel, out, host, editor, side, settings, peek)
		}
		return
	}

	side := NewSideContext(host.AgentCtx())
	// The side thread's durable home (nil when there is no store: memory-only).
	peek := openPeekFor(out, host)
	// Remember this thread so a later bare /btw can reopen it. LastBtwBase marks
	// where the copied background ends and the side Q&A begins, so a reopen only
	// replays the side turns, not the whole main transcript.
	host.SetLastBtw(side)
	host.SetLastBtwBase(len(side.Messages))
	host.SetPeek(peek)
	printBtwHeader(out)
	askSide(setCancel, out, host, side, settings, question, peek)
	// Follow-up loop: keep answering in the same ephemeral thread until the user
	// exits. A nil editor (direct test callers that only ask one question) skips
	// the loop entirely, so a single /btw asks exactly one question and returns.
	if editor != nil {
		btwFollowUpLoop(setCancel, out, host, editor, side, settings, peek)
	}
}

// openPeekFor creates the peek session that will hold this side thread's own
// Q&A. It returns nil (memory-only side thread, the pre-T4.3 behavior) when the
// driver has no store or the session has no id yet, and when creating the peek
// file fails — persistence is best-effort and must never break the question.
func openPeekFor(out io.Writer, host cli.Host) *session.PeekSession {
	store := host.Store()
	if store == nil || host.Header().ID == "" {
		return nil
	}
	peek, err := session.OpenPeek(store, host.Header(), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(out, "pigo: side thread will not be saved: %v\n", err)
		return nil
	}
	return peek
}

// reopenSideThread resolves a bare "/btw": the live in-process side thread when
// there is one, else the most recent peek session branched from the current
// session, whose persisted Q&A is replayed onto a fresh background copy. It
// reports false when no side thread exists at all; the caller then prints usage.
func reopenSideThread(out io.Writer, host cli.Host) (*agentcore.AgentContext, *session.PeekSession, bool) {
	if side := host.LastBtw(); side != nil {
		return side, host.Peek(), true
	}
	store := host.Store()
	if store == nil || host.Header().ID == "" {
		return nil, nil, false
	}
	peek, err := store.LatestPeek(host.Header().ID)
	if err != nil {
		fmt.Fprintf(out, "pigo: reopen side thread: %v\n", err)
		return nil, nil, false
	}
	if peek == nil {
		return nil, nil, false
	}
	history, err := peek.Messages()
	if err != nil {
		fmt.Fprintf(out, "pigo: reopen side thread: %v\n", err)
		return nil, nil, false
	}
	// The background is re-seeded from the LIVE main context, so a peek file
	// never carries (and can never resurrect) a stale copy of it.
	side := NewSideContext(host.AgentCtx())
	base := len(side.Messages)
	side.Messages = append(side.Messages, history...)
	host.SetLastBtw(side)
	host.SetLastBtwBase(base)
	host.SetPeek(peek)
	return side, peek, true
}

// replaySideHistory prints the side thread's own Q&A (everything after the
// copied main-conversation background at index base) when a bare /btw reopens a
// prior thread, so the user can browse earlier answers before continuing. Only
// user questions and assistant text are shown; tool activity is omitted to keep
// the recap compact.
func replaySideHistory(out io.Writer, side *agentcore.AgentContext, base int) {
	if base > len(side.Messages) {
		base = len(side.Messages)
	}
	for _, msg := range side.Messages[base:] {
		switch m := msg.(type) {
		case agentcore.UserMessage:
			fmt.Fprintf(out, "%s %s\n", ui.Colorize(ui.Enabled(), ui.Dim, "you:"), agentcore.ContentToText(m.Content))
		case agentcore.AssistantMessage:
			if text := agentcore.ContentToText(m.Content); text != "" {
				rendered := ui.RenderMarkdown(text)
				fmt.Fprint(out, rendered)
				if !strings.HasSuffix(rendered, "\n") {
					fmt.Fprintln(out)
				}
			}
		}
	}
}

// btwFollowUpLoop reads follow-up questions and answers them in the same side
// context, so each answer sees the prior side Q&A (FR-4). It exits on /exit,
// /quit, EOF, or an idle Ctrl+C (errLineInterrupted) — the same exit affordances
// as the main REPL, but confined to the side thread (FR-5). A blank line is
// ignored (stays in the thread). Nothing here touches the main context.
func btwFollowUpLoop(setCancel func(context.CancelFunc), out io.Writer, host cli.Host, editor cli.Editor, side *agentcore.AgentContext, settings BtwRunSettings, peek *session.PeekSession) {
	for {
		raw, err := editor.ReadLine(btwPrompt)
		if errors.Is(err, cli.ErrLineInterrupted) {
			// Idle Ctrl+C at the side prompt leaves the thread (a Ctrl+C during a
			// run is handled inside askSide via the SIGINT cancel plumbing).
			leaveSideThread(out, peek)
			return
		}
		q := strings.TrimSpace(raw)
		if err != nil && q == "" {
			// EOF or read error with no partial line: leave the thread.
			leaveSideThread(out, peek)
			return
		}
		if q == "/exit" || q == "/quit" {
			leaveSideThread(out, peek)
			return
		}
		if q == "" {
			continue
		}
		askSide(setCancel, out, host, side, settings, q, peek)
	}
}

// leaveSideThread closes the side thread, naming the peek session it was saved
// to when there is one (so the user can find it again with a bare /btw even
// though the session list hides it).
func leaveSideThread(out io.Writer, peek *session.PeekSession) {
	if peek != nil {
		fmt.Fprintf(out, "left side thread (saved as %s)\n", peek.ID())
		return
	}
	fmt.Fprintln(out, "left side thread")
}

// NewSideContext builds the side thread's private AgentContext. Its Messages are
// a fresh slice seeded with a shallow COPY of the main messages (the elements
// are immutable value/interface messages, so a copied slice header is enough to
// guarantee appends to the side thread never reach the main context's Messages).
// The system prompt and tools are shared by value; only Messages diverges.
func NewSideContext(main *agentcore.AgentContext) *agentcore.AgentContext {
	msgs := make(agentcore.MessageList, len(main.Messages))
	copy(msgs, main.Messages)
	return &agentcore.AgentContext{
		SystemPrompt: main.SystemPrompt,
		Messages:     msgs,
		Tools:        main.Tools,
	}
}

// printBtwHeader prints the side-thread banner.
func printBtwHeader(out io.Writer) {
	fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Dim, btwHeader))
}

// AskSide appends the question to the side context and streams one answer,
// mirroring streamRun's rendering but targeting the side context so nothing is
// written back to the main conversation. It reuses the REPL's SIGINT cancel
// plumbing via setCancel. The model/provider/thinking come from settings
// (session defaults overlaid with btw.json, #282), never from host.Live(), so a
// /btw override cannot leak into the main session.
//
// AskSide is the memory-only entry point: it keeps no peek session, so direct
// callers (tests, drivers without a store) get the pre-T4.3 behavior.
func AskSide(setCancel func(context.CancelFunc), out io.Writer, host cli.Host, side *agentcore.AgentContext, settings BtwRunSettings, question string) {
	askSide(setCancel, out, host, side, settings, question, nil)
}

// askSide is AskSide with the peek session this turn's Q&A is appended to (T4.3,
// nil = memory-only). Only the messages the side run produced are persisted, so
// the peek file stays a pure side transcript.
func askSide(setCancel func(context.CancelFunc), out io.Writer, host cli.Host, side *agentcore.AgentContext, settings BtwRunSettings, question string, peek *session.PeekSession) {
	start := len(side.Messages)
	content, err := ui.BuildUserContent(question)
	if err != nil {
		fmt.Fprintf(out, "pigo: %v\n", err)
		return
	}
	side.Messages = append(side.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   content,
	})

	runCtx, cancel := context.WithCancel(context.Background())
	setCancel(cancel)
	defer func() {
		cancel()
		setCancel(nil)
	}()

	// Show a transient status while the model works (FR-9). It is printed on its
	// own line; the streamed answer follows below it.
	fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Dim, "Answering…"))

	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         settings.Model,
			Provider:      settings.ProviderName,
			ThinkingLevel: settings.ThinkingLevel,
			Stream:        provider.StreamFnFromProvider(settings.Provider),
			GetAPIKey:       host.Creds().GetAPIKey,
			ContextWindow:   host.Live().ContextWindow,
			MaxOutputTokens: host.Live().MaxOutputTokens,
			Compaction:      compaction.DefaultCompactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{
				Registry:       host.Registry(),
				BeforeToolCall: trust.BeforeToolCall(host.Trust(), host.Cwd(), host.Input(), out, host.ConfirmMu()),
			},
		},
		Reminders: host.Reminders(),
	}
	// Wire the per-turn hook seams onto the side run's cfg; nil dispatcher is a
	// no-op (FR-18).
	if d := host.Dispatcher(); d != nil {
		run.InstallSeams(&cfg, d, host.HookDeps())
	}
	stream := runtime.StartRun(runCtx, side, cfg)
	drainSideStream(runCtx, out, host, stream)
	// Persist exactly what this turn produced — the question and the answer —
	// into the side thread's peek session. Nothing here touches the main
	// session, and a nil peek (no store) is a silent no-op.
	if err := peek.Append(side.Messages[start:]); err != nil {
		fmt.Fprintf(out, "pigo: side thread not saved: %v\n", err)
	}
}

// chainBtwEvent returns the OnEvent observer for a /btw side run: the plugin
// notifier, with the SessionEnd/PreCompact hook notifier chained after it when
// hooks are configured, mirroring the REPL's OnEvent composition.
func chainBtwEvent(host cli.Host) func(agentcore.AgentEvent) {
	notifier := host.NotifierHandle()
	d := host.Dispatcher()
	if d == nil {
		return notifier
	}
	deps := host.HookDeps()
	hookEvent := hooks.NewHookNotifier(d, deps.SessionID, deps.ProjectDir).Handle
	if notifier == nil {
		return hookEvent
	}
	return func(ev agentcore.AgentEvent) {
		notifier(ev)
		hookEvent(ev)
	}
}

// drainSideStream prints the streamed assistant text and tool activity of a side
// run, mirroring streamRun/drainGoalStream. It blocks until the run ends. Unlike
// the main loop it persists nothing.
func drainSideStream(ctx context.Context, out io.Writer, host cli.Host, stream *runtime.LoopEventStream) {
	var reply strings.Builder
	flushReply := func() {
		if reply.Len() == 0 {
			return
		}
		rendered := ui.RenderMarkdown(reply.String())
		fmt.Fprint(out, rendered)
		if !strings.HasSuffix(rendered, "\n") {
			fmt.Fprintln(out)
		}
		reply.Reset()
	}
	_, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{
		OnEvent: chainBtwEvent(host),
		OnText: func(delta string) {
			reply.WriteString(delta)
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage) {
			flushReply()
			for _, c := range msg.ToolCalls() {
				fmt.Fprintf(out, "  %s %s\n", ui.Colorize(ui.Enabled(), ui.Green, "→ tool:"), ui.ToolCallLabel(c))
			}
			for _, tr := range results {
				ui.RenderToolResult(out, tr)
			}
		},
	})
	flushReply()
	if err != nil {
		if ctx.Err() != nil {
			// A Ctrl+C during the run cancels just this answer; the follow-up loop
			// then returns to the btw prompt so the user can ask again or exit with
			// another Ctrl+C (FR-5).
			fmt.Fprintln(out, "^C interrupted — answer cancelled")
		} else {
			fmt.Fprintf(out, "error: %v\n", err)
		}
	}
}
