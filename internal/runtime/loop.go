// This file implements pi's two-layer agent loop (US-006, FR-1). It strings
// together streaming assistant responses, batch tool execution, and the loop's
// six hooks with control flow kept faithful to pi's runLoop:
//
//   - Inner loop: one turn = stream an assistant response → execute its tool
//     calls → feed the results back, repeating until an assistant message has no
//     tool calls (a natural turn end).
//   - Outer loop: after the inner loop settles, pull getFollowUpMessages; if any
//     are returned they become the next pending input and the inner loop runs
//     again, otherwise the run ends.
//
// Per-turn hooks after each turn_end: getSteeringMessages (pulled after tool
// execution and injected before the next turn), prepareNextTurn (may swap
// context / model / thinkingLevel), shouldStopAfterTurn (true ⇒ agent_end +
// exit). Two stop reasons are handled specially: length (the response was
// truncated by the token cap) fails every tool call so the model resends
// (failToolCallsFromTruncatedMessage); error / aborted end the run immediately.
//
// agentLoop starts a fresh run from a prompt already appended to the context.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/reqdump"
	"github.com/smallnest/pigo/internal/tooldecl"
)

// nowMillis returns the current Unix time in milliseconds, the timestamp unit
// used for CompactionMessage checkpoints and message stamps.
func nowMillis() int64 { return time.Now().UnixMilli() }

// stampedMessage records the append wall clock on a message the loop adds to
// the live list (T3.5 随件② fix): the microcompaction idle gate reads the
// newest message timestamp, and no production path stamped one before — the
// provider decoders and the drivers all leave it zero — so the 60-minute
// idle window could never open. Zero timestamps on restored history are left
// alone (backfilling "now" would fake activity; zero = never idle, the
// conservative direction).
func stampedMessage(m agentcore.Message) agentcore.Message {
	switch t := m.(type) {
	case agentcore.UserMessage:
		if t.Timestamp == 0 {
			t.Timestamp = nowMillis()
			return t
		}
	case agentcore.AssistantMessage:
		if t.Timestamp == 0 {
			t.Timestamp = nowMillis()
			return t
		}
	case agentcore.ToolResultMessage:
		if t.Timestamp == 0 {
			t.Timestamp = nowMillis()
			return t
		}
	}
	return m
}

// TurnUpdate is the optional result of PrepareNextTurn: any non-nil field
// replaces the corresponding piece of loop state before the next turn. It lets
// a caller swap the trimmed context, system prompt, tool set, model, or
// thinking level between turns (FR-6).
type TurnUpdate struct {
	Messages      *agentcore.MessageList
	SystemPrompt  *string
	Tools         *[]agentcore.AgentTool
	Model         *string
	ThinkingLevel *agentcore.ThinkingLevel
}

// StopDecision is the result of the OnStop seam. Block=true prevents the run
// from ending; Guidance, when non-empty, is appended as a user-role message to
// steer the forced continuation (the Stop / SubagentStop hook's reason). The
// zero value (Block=false) lets the run end.
type StopDecision struct {
	Block    bool
	Guidance string
}

// RunConfig is the full configuration for a loop run: the per-turn streaming
// config (embedded LoopConfig), the batch tool-execution config, and the four
// loop-level hooks. Every hook is optional (nil = default behavior).
type RunConfig struct {
	LoopConfig
	// Batch holds the tool registry and the prepare/before/after hooks used to
	// execute each assistant message's tool calls.
	Batch agenttool.BatchConfig

	// GetFollowUpMessages is consulted after the inner loop settles (an assistant
	// message with no tool calls). Returning messages continues the outer loop
	// with them as the next input; returning none ends the run (FR-9).
	GetFollowUpMessages func(ctx context.Context, agentCtx *agentcore.AgentContext) []agentcore.AgentMessage
	// GetSteeringMessages is pulled after each turn's tool execution and injected
	// before the next turn (pi per-turn semantics, FR-8).
	GetSteeringMessages func(ctx context.Context) []agentcore.AgentMessage
	// PrepareNextTurn runs after each turn_end and may swap context / model /
	// thinkingLevel for the next turn (FR-6).
	PrepareNextTurn func(ctx context.Context, agentCtx *agentcore.AgentContext) *TurnUpdate
	// ShouldStopAfterTurn runs after each turn_end; true ends the run with an
	// agent_end event (FR-7).
	ShouldStopAfterTurn func(ctx context.Context, agentCtx *agentcore.AgentContext) bool

	// OnStop, when set, is consulted right before the run would end naturally (no
	// tool calls and no follow-up messages). Returning a decision with Block=true
	// keeps the loop running: any Guidance is appended as a user-role message to
	// steer the continued run (Stop / SubagentStop hooks, US-008/009, FR-10). The
	// seam carries no loop-protection itself — the caller's decorator owns the
	// consecutive-block counter and the FR-12 force-stop limit, so an ill-behaved
	// hook cannot loop forever. nil or a non-blocking decision lets the run end.
	OnStop func(ctx context.Context, agentCtx *agentcore.AgentContext) *StopDecision

	// Reminders holds the per-turn system-reminder providers (US-002, FR-1/FR-2).
	// When non-empty, ephemeral <system-reminder> messages are injected into each
	// turn's LLM request through the existing TransformContext seam, so they never
	// enter the persisted history. nil / empty = no injection.
	Reminders *ReminderRegistry

	// EventBuffer is the buffer size of the emitted EventStream. 0 gives fully
	// synchronous back-pressure (matching pi's awaited emit).
	EventBuffer int

	// SessionID, when set, is carried in the run's agent_start event so a
	// stream-json consumer sees the backing session id in the first event and can
	// resume the run later (mirrors pi/Claude Code). It is also the session key
	// under which auto-compaction checkpoints are persisted (see MemoryRoot).
	SessionID string

	// MemoryRoot, when non-empty (together with SessionID), enables checkpoint
	// persistence for the "infinite context" feature (#480/#481): after a
	// successful auto-compaction the collapsed prefix's summary is written as a
	// checkpoint under <MemoryRoot>/sessions/<SessionID>/checkpoint.md so a later
	// run can reload it instead of replaying the whole transcript. It is left ""
	// when persistent memory is disabled (memory.enabled=false), which fully
	// disables checkpoint writing. A checkpoint write failure is non-fatal.
	MemoryRoot string

	// PersistedCount reports how many leading messages of agentCtx.Messages the
	// driver has already persisted to the session tree (the driver's cursor).
	// Auto-compaction uses it to insert the compaction marker at the branch tip
	// (the only place AppendBranch can chain into), which is what keeps the
	// persisted tree append-only: the marker must land after the last persisted
	// entry so PersistTurn's plain tail append carries it into the tree. nil
	// (side runs, tests) is treated as 0: the marker always lands in the
	// unpersisted tail, which is conservative and still correct.
	PersistedCount func() int

	// ToolDeclaration, when non-nil with at least one deferred tool, activates
	// the deferred tool declaration machinery (T4.1): the declared face is
	// filtered per the plan, search_tools claims enter on the next turn, and
	// the announcement rides the request as an ephemeral system-reminder.
	// nil (or a plan with no deferred tools) = direct declaration, zero
	// overhead — the capability gate resolves at assembly time, so a run that
	// reaches the loop with a plan already passed it.
	ToolDeclaration *tooldecl.Plan
}

// LoopEventStream is the stream returned by the loop entry points: it carries
// AgentEvents and yields the messages newly produced during the run.
type LoopEventStream = agentcore.EventStream[agentcore.AgentEvent, []agentcore.AgentMessage]

// agentLoop starts a fresh run. The caller has already appended the initiating
// user message(s) to agentCtx.Messages. It returns immediately with an
// EventStream; a producer goroutine drives the loop and closes the stream when
// the run ends.
func agentLoop(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig) *LoopEventStream {
	// Publish the run's session id to the dump recorder (reqdump) before any
	// provider call: a connect-time request failure is dumped under the session
	// that made it. This is the single funnel — StartRun (the out-of-package
	// entry) and RunHeadless both pass through here.
	reqdump.SetSession(cfg.SessionID)
	stream := agentcore.NewEventStream[agentcore.AgentEvent, []agentcore.AgentMessage](cfg.EventBuffer)
	go runLoop(ctx, agentCtx, cfg, stream)
	return stream
}

// StartRun is the exported entry point for a fresh run, used by out-of-package
// drivers (the interactive REPL, US-022). It is a thin wrapper over agentLoop so
// the loop internals stay unexported while callers outside the package can
// still launch a run and consume its event stream.
func StartRun(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig) *LoopEventStream {
	return agentLoop(ctx, agentCtx, cfg)
}

// runLoop is the producer: it drives the two-layer loop, emitting events onto
// stream and setting the stream result to the messages produced during the run.
func runLoop(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig, stream *LoopEventStream) {
	// readFileState ledger (T3.5): lazy-initialized so every AgentContext
	// constructor gets it without opt-in — read/write/edit ledger their effects
	// through the loop-injected context, and microcompaction revokes residency
	// when it evicts read results. In-process only; never persisted.
	if agentCtx.ReadFiles == nil {
		agentCtx.ReadFiles = agentcore.NewReadFileState()
	}
	// tel accumulates structured telemetry (turn count, per-tool durations,
	// truncation count, compaction count, latest context-utilization ratio) from
	// the events emitted below, surfaced as a TelemetryEvent at run end.
	tel := newTelemetry()
	emit := func(ev agentcore.AgentEvent) error {
		tel.observe(ev)
		return stream.Emit(ctx, ev)
	}
	// emitFrom wraps the raw stream.Emit callback handed to streamAssistantResponse
	// and ExecuteToolCalls so telemetry observes those events (message_* and
	// tool_execution_*) too, without changing their signatures.
	emitFrom := func(c context.Context, ev agentcore.AgentEvent) error {
		tel.observe(ev)
		return stream.Emit(c, ev)
	}
	// pipe owns the compaction orchestration (T3.3.1): trigger judgment, marker
	// insertion, circuit breaker, post-compaction reminder, microcompaction.
	// The loop keeps exactly two call sites — pipe.RequestView before each
	// request and pipe.AfterTurn at each turn boundary — and injects the seams
	// that are RunConfig knowledge (Summarizer, checkpoint store, persist
	// cursor, telemetry observation, wall clock).
	pipe := compaction.NewPipeline(compaction.PipelineConfig{
		Settings:        cfg.Compaction,
		ContextWindow:   cfg.ContextWindow,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Model:           func() string { return cfg.Model },
		Emit:            emit,
		Now:             nowMillis,
		RecordContext:   tel.recordContext,
		Summarize: func(ctx context.Context, view agentcore.MessageList, prevIdx int, prevSummary string, prevDetails *compaction.CompactionDetails) (*compaction.CompactionResult, error) {
			return runCompaction(ctx, view, &cfg, prevIdx, prevSummary, prevDetails)
		},
		PersistedCount: cfg.PersistedCount,
		PersistCheckpoint: func(ctx context.Context, view agentcore.MessageList, res *compaction.CompactionResult) {
			writeCompactionCheckpoint(ctx, view, res, &cfg)
		},
	})
	// Deferred tool declaration (T4.1): when a plan is wired, restore claims
	// from persisted history and rebuild the declared face in the same batch —
	// the resume-atomicity contract (spec deferred-tool-exposure.md §2.4):
	// replaying the ToolClaimMessage entries and mounting the face happen
	// together, so a claimed tool is directly callable after resume with no
	// observable "claims exist, tools absent" intermediate. The executor gate
	// makes the dispatch side structural (an unclaimed deferred call gets the
	// search_tools guidance instead of executing; a hidden call looks unknown),
	// and every request re-checks the claim revision so a claim made this turn
	// enters the face on the next one ("下一轮进声明面").
	var declState *tooldecl.State
	declRev := int64(0)
	declAll := agentCtx.Tools
	if cfg.ToolDeclaration != nil && cfg.ToolDeclaration.Len() > 0 {
		declState = tooldecl.NewState(*cfg.ToolDeclaration)
		for _, m := range agentCtx.Messages {
			if c, ok := m.(agentcore.ToolClaimMessage); ok {
				declState.Restore(c.Tools)
			}
		}
		declRev = declState.Revision()
		agentCtx.Tools = tooldecl.DeclaredTools(declAll, declState)
		cfg.Batch.ToolGate = declState
		// Bind the state into the search_tools instance(s) so the claim path
		// can reach the shared state (the tool is a direct face member — it
		// must always stay declared for the model to claim anything). The
		// registry holds its own instance from the executor's lookups, so bind
		// both surfaces.
		bindSearchTools := func(tools []agentcore.AgentTool) {
			for _, t := range tools {
				if st, ok := t.(*agenttool.SearchToolsTool); ok {
					st.Bind(declState)
				}
			}
		}
		bindSearchTools(agentCtx.Tools)
		if cfg.Batch.Registry != nil {
			bindSearchTools(cfg.Batch.Registry.List())
		}
	}
	refreshDeclaredFace := func() {
		if declState != nil && declState.Revision() != declRev {
			declRev = declState.Revision()
			agentCtx.Tools = tooldecl.DeclaredTools(declAll, declState)
		}
	}
	// Wire per-turn system-reminder injection (US-002) onto the TransformContext
	// seam. Reminders are appended to the request-shaped copy only, so they stay
	// ephemeral: never written back to agentCtx.Messages, never persisted, never
	// swept into a compaction summary.
	// Post-compaction live-state re-injection (T3.3 随件): the one-shot
	// reminder set by the last auto-compaction rides the next request as an
	// ephemeral <system-reminder>, ahead of the standing reminder registry
	// (so reminders still land last at the tail).
	inner := cfg.TransformContext
	cfg.TransformContext = func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList {
		if inner != nil {
			msgs = inner(ctx, msgs)
		}
		if note := pipe.TakePostCompactReminder(); note != "" {
			msgs = append(msgs, agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(note)},
			})
		}
		// Deferred-tool announcement (T4.1): the unclaimed deferred set rides
		// the request as an ephemeral system-reminder. The body is a pure
		// function of the unclaimed set, so a turn with no claims produces a
		// byte-identical announcement — the cache-friendly contract (验收 6).
		if declState != nil {
			if ann := declState.Announcement(); ann != "" {
				msgs = append(msgs, agentcore.UserMessage{
					RoleField: agentcore.RoleUser,
					Content:   agentcore.ContentList{agentcore.NewTextContent(agentcore.WrapSystemReminder(ann))},
				})
			}
		}
		return msgs
	}
	if !cfg.Reminders.Empty() {
		cfg.TransformContext = cfg.Reminders.wrapTransform(cfg.TransformContext)
	}
	startIdx := len(agentCtx.Messages)
	// newMessages returns the messages appended since the run began.
	newMessages := func() []agentcore.AgentMessage {
		if len(agentCtx.Messages) <= startIdx {
			return nil
		}
		out := make([]agentcore.AgentMessage, len(agentCtx.Messages)-startIdx)
		copy(out, agentCtx.Messages[startIdx:])
		return out
	}

	// finish emits the telemetry summary then agent_end (unless suppressed by a
	// prior emit error), records the run result, and closes the stream exactly
	// once. Telemetry is emitted first so a consumer sees the run's structured
	// metrics immediately before the terminal event.
	finish := func() {
		_ = emit(tel.summary())
		msgs := newMessages()
		_ = emit(agentcore.AgentEndEvent{Messages: msgs})
		stream.SetResult(msgs)
		stream.Close()
	}
	// finishErr is finish for a loop that ended because an emit failed. When
	// the failure was the run context being cancelled, the cancellation is
	// recorded as the stream outcome: otherwise stream.Result's select races
	// its closed result channel against ctx.Done() and a consumer (headless
	// termination via the shellguard seam, T2.1) can observe a nil error for
	// a cancelled run about half the time.
	finishErr := func(err error) {
		if errors.Is(err, context.Canceled) {
			stream.SetError(err)
		}
		finish()
	}

	if err := emit(agentcore.AgentStartEvent{SessionID: cfg.SessionID}); err != nil {
		finish()
		return
	}

	for { // outer loop: pending / follow-up messages
		for { // inner loop: turns until no tool calls
			// A search_tools claim from the previous turn swaps the declared
			// face before this request (T4.1: "命中者下一轮进声明面").
			refreshDeclaredFace()
			// RequestView (T3.3.1) runs just before the request: one
			// microcompaction pass — the double gate (token pressure on the
			// derived line, or the 60-minute cache-cold idle window) evicts old
			// regenerable tool results via a durable marker — followed by the
			// projection, yielding the request view in one shot.
			view := pipe.RequestView(ctx, agentCtx)
			if err := emit(agentcore.TurnStartEvent{}); err != nil {
				finishErr(err)
				return
			}

			assistant, err := streamAssistantResponse(ctx, agentCtx, cfg.LoopConfig, emitFrom, view)
			if err != nil {
				// emit was cancelled mid-stream; end the run.
				finishErr(err)
				return
			}

			switch assistant.StopReason {
			case agentcore.StopReasonLength:
				// Truncated by the token cap: fail every tool call so the model
				// resends, then continue feeding back.
				toolResults := failToolCallsFromTruncatedMessage(agentCtx, assistant)
				if err := emit(agentcore.TurnEndEvent{Message: assistant, ToolResults: toolResults}); err != nil {
					finishErr(err)
					return
				}
				if afterTurn(ctx, agentCtx, &cfg, true, pipe) {
					finish()
					return
				}
				continue
			case agentcore.StopReasonError, agentcore.StopReasonAborted:
				// Terminal failure: emit the turn end and stop.
				_ = emit(agentcore.TurnEndEvent{Message: assistant})
				finish()
				return
			}

			calls := toAgentToolCalls(assistant.ToolCalls())
			if len(calls) == 0 {
				// Natural turn end: no tools to run.
				if err := emit(agentcore.TurnEndEvent{Message: assistant}); err != nil {
					finishErr(err)
					return
				}
				if afterTurn(ctx, agentCtx, &cfg, false, pipe) {
					finish()
					return
				}
				break // exit inner loop → consult follow-up messages
			}

			// Inject the run-level emitter into the context so tools (notably the
			// generic task tool) can retrieve it via ProgressEmitterFromContext and
			// surface a dispatched sub-agent's progress up this parent event stream.
			// emitFrom feeds the parent stream and is run-scoped, so a child's
			// SubAgentProgressEvent lands on the right run's stream. The AgentContext
			// rides along for tools that must read or extend the live conversation
			// (the context_edit tool, T3.4).
			toolCtx := agentcore.WithAgentContext(
				agentcore.WithProgressEmitter(ctx, emitFrom), agentCtx)
			toolResults, allTerminate := agenttool.ExecuteToolCalls(toolCtx, cfg.Batch, calls, emitFrom)
			for _, tr := range toolResults {
				agentCtx.Messages = append(agentCtx.Messages, stampedMessage(tr))
			}
			if err := emit(agentcore.TurnEndEvent{Message: assistant, ToolResults: toolResults}); err != nil {
				finishErr(err)
				return
			}
			if allTerminate {
				// Every tool asked to terminate the run.
				finish()
				return
			}
			if afterTurn(ctx, agentCtx, &cfg, true, pipe) {
				finish()
				return
			}
			// Feed the tool results back into the next turn.
		}

		// Inner loop settled: consult follow-up messages.
		if cfg.GetFollowUpMessages != nil {
			if follow := cfg.GetFollowUpMessages(ctx, agentCtx); len(follow) > 0 {
				for _, m := range follow {
					agentCtx.Messages = append(agentCtx.Messages, stampedMessage(m))
				}
				continue // outer loop with the follow-ups as new input
			}
		}
		// Stop hook: the run is about to end naturally. A hook may block the end
		// and force a continuation, feeding its guidance back as the next input
		// (US-008/009, FR-10). The consecutive-block counter and force-stop limit
		// (FR-12) live in the decorator behind OnStop, so this seam stays simple.
		if cfg.OnStop != nil {
			if dec := cfg.OnStop(ctx, agentCtx); dec != nil && dec.Block {
				if dec.Guidance != "" {
					agentCtx.Messages = append(agentCtx.Messages, stampedMessage(agentcore.UserMessage{
						RoleField: agentcore.RoleUser,
						Content:   agentcore.ContentList{agentcore.NewTextContent(dec.Guidance)},
					}))
				}
				continue // outer loop: keep the run alive
			}
		}
		break
	}

	finish()
}

// afterTurn runs the per-turn hooks after a turn_end. When hadToolExecution is
// true it first pulls getSteeringMessages and injects them before the next turn
// (pi per-turn semantics). It then applies prepareNextTurn, hands the turn
// boundary to the compaction pipeline (pipe.AfterTurn: full-compaction trigger
// judgment + execution + the post-settle utilization observation — the loop
// only marks the boundary), and finally consults shouldStopAfterTurn, returning
// true when the run should end.
func afterTurn(ctx context.Context, agentCtx *agentcore.AgentContext, cfg *RunConfig, hadToolExecution bool, pipe *compaction.Pipeline) (stop bool) {
	if hadToolExecution && cfg.GetSteeringMessages != nil {
		if steer := cfg.GetSteeringMessages(ctx); len(steer) > 0 {
			for _, m := range steer {
				agentCtx.Messages = append(agentCtx.Messages, stampedMessage(m))
			}
		}
	}
	if cfg.PrepareNextTurn != nil {
		if upd := cfg.PrepareNextTurn(ctx, agentCtx); upd != nil {
			applyTurnUpdate(agentCtx, cfg, upd)
		}
	}
	pipe.AfterTurn(ctx, agentCtx)
	if cfg.ShouldStopAfterTurn != nil {
		return cfg.ShouldStopAfterTurn(ctx, agentCtx)
	}
	return false
}

// runCompaction invokes compaction.Compact over the request view with the
// loop's summarization config, falling back to the primary Stream/Model when
// the summary-specific fields are unset. It is injected into the compaction
// pipeline as the Summarizer seam (T3.3.1) and also drives the rebuild
// fallback path. prevCompactionIndex/prevSummary/
// prevDetails carry the view's previous compaction marker so successive
// compactions chain (T3.3 defect-① fix): summarization starts after it, the
// prior summary feeds the update template, and its file lists seed this one.
func runCompaction(ctx context.Context, view agentcore.MessageList, cfg *RunConfig, prevIdx int, prevSummary string, prevDetails *compaction.CompactionDetails) (*compaction.CompactionResult, error) {
	stream := cfg.SummaryStream
	if stream == nil {
		stream = cfg.Stream
	}
	model := cfg.SummaryModel
	if model.ID == "" {
		model = provider.Model{Provider: cfg.Provider, ID: cfg.Model, ContextWindow: cfg.ContextWindow}
	}
	// Resolve the API key the same way the primary turn does (dynamic key wins,
	// static APIKey is the fallback) so the summarization stream authenticates
	// against auth-requiring providers instead of failing with "missing API key".
	key := cfg.APIKey
	if cfg.GetAPIKey != nil {
		if dyn := cfg.GetAPIKey(ctx, cfg.Provider); dyn != "" {
			key = dyn
		}
	}
	scfg := provider.StreamConfig{APIKey: key, ThinkingLevel: cfg.ThinkingLevel}
	return compaction.Compact(ctx, stream, model, view, cfg.Compaction, prevIdx, prevDetails, prevSummary, scfg)
}

// writeCompactionCheckpoint persists the just-produced compaction summary as a
// session checkpoint so a later run can reload the collapsed prefix instead of
// replaying it (#480/#481). It is a no-op unless checkpoint persistence is wired
// (MemoryRoot and SessionID both set) — which is how memory.enabled=false keeps
// the whole subsystem inert. It reuses res.Summary (no extra summarization call)
// via BuildCheckpoint, tagging the checkpoint with the compaction cut point as
// its watermark. All failures are non-fatal: they are logged to stderr and the
// run continues on the compacted context (WriteCheckpoint's log-and-continue
// contract).
func writeCompactionCheckpoint(ctx context.Context, msgs agentcore.MessageList, res *compaction.CompactionResult, cfg *RunConfig) {
	if cfg.MemoryRoot == "" || cfg.SessionID == "" || res == nil {
		return
	}
	watermark := res.FirstKeptIndex
	if watermark < 0 {
		watermark = 0
	}
	if watermark > len(msgs) {
		watermark = len(msgs)
	}
	// summarize returns the summary the compaction already computed, so
	// BuildCheckpoint records an honest CoveredMessages count without a second
	// LLM round-trip.
	summarize := func(context.Context, []agentcore.Message) (string, error) {
		return res.Summary, nil
	}
	cp, err := BuildCheckpoint(ctx, msgs[:watermark], watermark, time.Now(), summarize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: checkpoint: build for session %s: %v\n", cfg.SessionID, err)
		return
	}
	if err := WriteCheckpoint(cfg.SessionID, cfg.MemoryRoot, cp); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: checkpoint: write for session %s: %v\n", cfg.SessionID, err)
	}
}

// applyTurnUpdate applies a non-nil TurnUpdate to the mutable loop state: any
// set field replaces the current context / config value for the next turn.
func applyTurnUpdate(agentCtx *agentcore.AgentContext, cfg *RunConfig, upd *TurnUpdate) {
	if upd.Messages != nil {
		agentCtx.Messages = *upd.Messages
	}
	if upd.SystemPrompt != nil {
		agentCtx.SystemPrompt = *upd.SystemPrompt
	}
	if upd.Tools != nil {
		agentCtx.Tools = *upd.Tools
	}
	if upd.Model != nil {
		cfg.Model = *upd.Model
	}
	if upd.ThinkingLevel != nil {
		cfg.ThinkingLevel = *upd.ThinkingLevel
	}
}

// failToolCallsFromTruncatedMessage produces an error tool-result message for
// every tool call in a truncated (stopReason=length) assistant message, telling
// the model the response was cut off and to resend. The results are appended to
// the context and returned. Mirrors pi's failToolCallsFromTruncatedMessage.
func failToolCallsFromTruncatedMessage(agentCtx *agentcore.AgentContext, assistant agentcore.AssistantMessage) []agentcore.ToolResultMessage {
	calls := assistant.ToolCalls()
	if len(calls) == 0 {
		return nil
	}
	results := make([]agentcore.ToolResultMessage, 0, len(calls))
	for _, c := range calls {
		results = append(results, agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: c.ID,
			ToolName:   c.Name,
			Content: agentcore.ContentList{agentcore.NewTextContent(
				"The previous response was truncated because it hit the output token limit, " +
					"so this tool call was not executed. Please send a shorter response and retry.")},
			IsError: true,
		})
	}
	for _, r := range results {
		agentCtx.Messages = append(agentCtx.Messages, r)
	}
	return results
}

// toAgentToolCalls converts the assistant message's ToolCallContent blocks into
// the loop-level AgentToolCall view executeToolCalls consumes.
func toAgentToolCalls(blocks []agentcore.ToolCallContent) []agentcore.AgentToolCall {
	if len(blocks) == 0 {
		return nil
	}
	calls := make([]agentcore.AgentToolCall, len(blocks))
	for i, b := range blocks {
		calls[i] = agentcore.AgentToolCall{ID: b.ID, Name: b.Name, Arguments: b.Arguments}
	}
	return calls
}
