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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
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
}

// LoopEventStream is the stream returned by the loop entry points: it carries
// AgentEvents and yields the messages newly produced during the run.
type LoopEventStream = agentcore.EventStream[agentcore.AgentEvent, []agentcore.AgentMessage]

// agentLoop starts a fresh run. The caller has already appended the initiating
// user message(s) to agentCtx.Messages. It returns immediately with an
// EventStream; a producer goroutine drives the loop and closes the stream when
// the run ends.
func agentLoop(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig) *LoopEventStream {
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
	// cmp carries the run-scoped compaction circuit breaker and the one-shot
	// post-compaction reminder (T3.3 随件).
	cmp := &compactor{}
	// readFileState ledger (T3.5): lazy-initialized so every AgentContext
	// constructor gets it without opt-in — read/write/edit ledger their effects
	// through the loop-injected context, and microcompaction revokes residency
	// when it evicts read results. In-process only; never persisted.
	if agentCtx.ReadFiles == nil {
		agentCtx.ReadFiles = agentcore.NewReadFileState()
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
		if note := cmp.takePostCompactReminder(); note != "" {
			msgs = append(msgs, agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(note)},
			})
		}
		return msgs
	}
	if !cfg.Reminders.Empty() {
		cfg.TransformContext = cfg.Reminders.wrapTransform(cfg.TransformContext)
	}
	startIdx := len(agentCtx.Messages)
	// tel accumulates structured telemetry (turn count, per-tool durations,
	// truncation count, compaction count, latest context-utilization ratio) from
	// the events emitted below, surfaced as a TelemetryEvent at run end.
	tel := newTelemetry()
	// newMessages returns the messages appended since the run began.
	newMessages := func() []agentcore.AgentMessage {
		if len(agentCtx.Messages) <= startIdx {
			return nil
		}
		out := make([]agentcore.AgentMessage, len(agentCtx.Messages)-startIdx)
		copy(out, agentCtx.Messages[startIdx:])
		return out
	}
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
			// Microcompaction (T3.3) runs just before the request: the double
			// gate (token pressure on the derived line, or the 60-minute
			// cache-cold idle window) evicts old regenerable tool results via a
			// durable marker; the projection turns them into placeholders.
			maybeMicrocompact(ctx, agentCtx, &cfg, emit)
			if err := emit(agentcore.TurnStartEvent{}); err != nil {
				finishErr(err)
				return
			}

			assistant, err := streamAssistantResponse(ctx, agentCtx, cfg.LoopConfig, emitFrom)
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
				if afterTurn(ctx, agentCtx, &cfg, true, emit, tel, cmp) {
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
				if afterTurn(ctx, agentCtx, &cfg, false, emit, tel, cmp) {
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
			if afterTurn(ctx, agentCtx, &cfg, true, emit, tel, cmp) {
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
// (pi per-turn semantics). It then applies prepareNextTurn, runs auto-compaction
// when the context has outgrown its window, and finally consults
// shouldStopAfterTurn, returning true when the run should end.
func afterTurn(ctx context.Context, agentCtx *agentcore.AgentContext, cfg *RunConfig, hadToolExecution bool, emit func(agentcore.AgentEvent) error, tel *telemetry, cmp *compactor) (stop bool) {
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
	maybeAutoCompact(ctx, agentCtx, cfg, emit, tel, cmp)
	// Record the latest context-utilization ratio once the turn has settled (after
	// any compaction), so the telemetry summary reports the current used/window
	// figure. This runs even when auto-compaction is disabled so utilization is
	// still observable whenever the context window is known. The ratio reads the
	// request view (T3.3): raw-list size would count history the view collapses.
	if tel != nil && cfg.ContextWindow > 0 {
		tokens := compaction.EstimateContextTokens(compaction.ProjectView(agentCtx.Messages)).Tokens
		tel.recordContext(tokens, cfg.ContextWindow)
	}
	if cfg.ShouldStopAfterTurn != nil {
		return cfg.ShouldStopAfterTurn(ctx, agentCtx)
	}
	return false
}

// compactor carries per-run compaction state across turns: the consecutive
// failure count feeding the circuit breaker (T3.3 随件, qwen/zcode 3-strike
// value). It is scoped to one run: pigo's loop keeps no cross-run state, and a
// failed run already terminates, so a fresh run starts with a clean breaker.
type compactor struct {
	failures int
	// postCompactReminder, when non-empty, is a one-shot ephemeral
	// system-reminder injected into the next turn's request (and only that
	// one) after a successful full compaction: the recently-read file paths
	// whose results the summary replaced (zcode's post-compaction live-state
	// re-injection, reference-hint form).
	postCompactReminder string
}

// takePostCompactReminder pops the pending post-compaction reminder (one-shot).
func (c *compactor) takePostCompactReminder() string {
	if c == nil || c.postCompactReminder == "" {
		return ""
	}
	t := c.postCompactReminder
	c.postCompactReminder = ""
	return t
}

// circuitBreakerLimit is how many consecutive compaction failures open the
// breaker for the rest of the run.
const circuitBreakerLimit = 3

// maybeAutoCompact checks whether the request view has outgrown its usable
// window and, if so, compacts: it inserts a CompactionMessage marker into the
// live list (T3.3 marker-entry model) instead of rewriting it, so the persisted
// tree stays append-only and the request view is derived by projection.
//
// Compaction is a no-op when disabled, when the context window is unknown
// (<= 0), or when usage is under threshold. A compaction failure is non-fatal:
// the original context is preserved and a CompactionEvent carrying the typed
// SkipReason and ErrorMessage is emitted so the failure is observable without
// aborting the run (US-004).
func maybeAutoCompact(ctx context.Context, agentCtx *agentcore.AgentContext, cfg *RunConfig, emit func(agentcore.AgentEvent) error, tel *telemetry, cmp *compactor) {
	if !cfg.Compaction.Enabled || cfg.ContextWindow <= 0 {
		return
	}
	persisted := 0
	if cfg.PersistedCount != nil {
		persisted = cfg.PersistedCount()
	}
	if persisted > len(agentCtx.Messages) {
		persisted = len(agentCtx.Messages) // defensive against a stale driver cursor
	}
	// Every decision and the summarization input run on the request view (T3.3):
	// the raw list may hold superseded markers and pre-compaction history that
	// the view collapses. The view→raw map (not the marker-anchor formula)
	// converts the cut back: microcompact markers and context edits also drop
	// entries from the view, so the formula drifts once they are present.
	view, rawOf := compaction.ProjectViewMapped(agentCtx.Messages)
	before := compaction.EstimateContextTokens(view).Tokens
	// Record pre-compaction utilization so the ratio reflects the peak that
	// triggered (or nearly triggered) compaction even when the summary is read
	// mid-run. afterTurn overwrites it with the post-settle figure.
	if tel != nil {
		tel.recordContext(before, cfg.ContextWindow)
	}
	if !compaction.ShouldCompact(before, cfg.ContextWindow, cfg.Compaction) {
		return
	}
	if cmp != nil && cmp.failures >= circuitBreakerLimit {
		_ = emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(compaction.SkipCircuitOpen),
			ErrorMessage: "compaction circuit breaker open after 3 consecutive failures",
		})
		return
	}
	// Signal the start so a front-end can show an in-progress indicator while the
	// summarization request (an LLM call that blocks the loop) is in flight.
	_ = emit(agentcore.CompactionStartEvent{Reason: "threshold", TokensBefore: before})

	// Iterative chain (defect-① fix): the view's leading marker, when present,
	// is the previous compaction — summarize only what came after it, seeding
	// the file lists and feeding its summary into the update template.
	prevIdx := -1
	var prevSummary string
	var prevDetails *compaction.CompactionDetails
	if len(view) > 0 {
		if c, ok := view[0].(agentcore.CompactionMessage); ok {
			prevIdx = 0
			prevSummary = c.Summary
			if d, err := unmarshalDetails(c.Details); err == nil && (len(d.ReadFiles) > 0 || len(d.ModifiedFiles) > 0) {
				prevDetails = d
			}
		}
	}
	res, err := runCompaction(ctx, view, cfg, prevIdx, prevSummary, prevDetails)
	if err != nil {
		if cmp != nil {
			cmp.failures++
		}
		_ = emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(compaction.SkipReasonOf(err)),
			ErrorMessage: err.Error(),
		})
		return
	}
	if res == nil {
		_ = emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(compaction.SkipNothingToSummarize),
		})
		return
	}
	if cmp != nil {
		cmp.failures = 0
	}

	cut := res.FirstKeptIndex // view coordinates
	fullCut := compaction.ViewRawOf(rawOf, cut)
	newList, marker, insertAt := insertCompactionMarker(agentCtx.Messages, res, fullCut, persisted)
	after := compaction.EstimateContextTokens(compaction.ProjectView(newList)).Tokens
	if after >= before {
		// Inflation guard (qwen): never apply a compaction that does not shrink
		// the view — a non-shrinking "compaction" risks a
		// compact→restore→recompact loop while paying for the summary call.
		_ = emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(compaction.SkipInflated),
			ErrorMessage: "compaction rejected: post-compaction view would not shrink",
		})
		return
	}
	marker.TokensAfter = after
	newList[insertAt] = marker
	// Live-state re-injection (zcode 随件): surface the recently-read files the
	// summary replaced so the model re-reads before trusting stale memory.
	if cmp != nil {
		cmp.postCompactReminder = postCompactReminder(view[max(prevIdx+1, 0):cut], agentCtx.ReadFiles)
	}
	// Persist a checkpoint of the collapsed prefix before inserting the marker so
	// a later run can reload it (infinite context, #480/#481). It reuses the
	// summary compaction just produced — no extra LLM call — and is best-effort.
	writeCompactionCheckpoint(ctx, view, res, cfg)
	agentCtx.Messages = newList
	_ = emit(agentcore.CompactionEvent{
		Reason:                "threshold",
		TokensBefore:          before,
		TokensAfter:           after,
		SummarizedCount:       max(0, cut-1),
		KeptCount:             len(view) - cut,
		SummaryUsage:          &res.SummaryUsage,
		WillRetriggerNextTurn: compaction.ShouldCompact(after, cfg.ContextWindow, cfg.Compaction),
	})
}

// insertCompactionMarker returns msgs with res's compaction marker inserted at
// the T3.3 topology position, plus the inserted marker (FirstKeptIndex /
// KeptBefore / TokensAfter stamped). Rules:
//
//   - fullCut is the cut in raw-list coordinates (mapped from the view cut).
//   - The marker is inserted at max(fullCut, persisted): when the cut reaches
//     into unpersisted messages the marker sits at the cut; when the kept
//     window starts inside already-persisted territory the marker sits at the
//     branch tip (the only place PersistTurn's tail append can chain it into
//     the tree), and KeptBefore records how many kept entries precede it on
//     the path so replay projection restores them.
func insertCompactionMarker(msgs agentcore.MessageList, res *compaction.CompactionResult, fullCut, persisted int) (agentcore.MessageList, agentcore.CompactionMessage, int) {
	insertAt := fullCut
	if insertAt < persisted {
		insertAt = persisted
	}
	marker := res.Message(nowMillis())
	marker.FirstKeptIndex = fullCut
	marker.KeptBefore = insertAt - fullCut
	out := make(agentcore.MessageList, 0, len(msgs)+1)
	out = append(out, msgs[:insertAt]...)
	out = append(out, marker)
	out = append(out, msgs[insertAt:]...)
	return out, marker, insertAt
}

// runCompaction invokes compaction.Compact over the request view with the
// loop's summarization config, falling back to the primary Stream/Model when
// the summary-specific fields are unset. prevCompactionIndex/prevSummary/
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

// maybeMicrocompact runs one microcompaction pass before a turn's request
// (T3.3): the zcode double gate — token pressure on the derived line
// min(0.9×autoLine, autoLine−2K), or the 60-minute idle window where the
// prompt cache is cold and eviction is free — decides whether to run; the
// decision evicts old regenerable tool results by appending one durable
// MicrocompactMessage marker (sticky: cleared results never resurrect, and
// the next PersistTurn carries the marker into the tree). Everything is
// decided on the request view; the raw list only ever grows. A pass is a
// no-op when compaction is disabled or the window is unknown.
func maybeMicrocompact(ctx context.Context, agentCtx *agentcore.AgentContext, cfg *RunConfig, emit func(agentcore.AgentEvent) error) {
	if !cfg.Compaction.Enabled || cfg.ContextWindow <= 0 {
		return
	}
	view := compaction.ProjectView(agentCtx.Messages)
	tokens := compaction.EstimateContextTokens(view).Tokens
	line := compaction.MicrocompactPressureLine(cfg.ContextWindow, cfg.Compaction.ReserveTokens)
	idle := compaction.IdleMillis(view, time.Now().UnixMilli()) >= compaction.MicrocompactIdleMillis
	if line <= 0 && !idle {
		return
	}
	if tokens < line && !idle {
		return
	}
	dec := compaction.DecideMicrocompact(view, tokens, line, idle)
	if len(dec.ClearedCallIDs) == 0 {
		if dec.SkipReason != "" {
			_ = emit(agentcore.MicrocompactEvent{Reason: dec.Reason, SkipReason: string(dec.SkipReason)})
		}
		return
	}
	agentCtx.Messages = append(agentCtx.Messages, dec.Marker(nowMillis()))
	// #4239 eviction rule (T3.5): the evicted read results no longer evidence
	// their files' residency — the next edit on those files is refused until
	// the model re-reads. Needs the pre-eviction view (the assistant calls live
	// there) and the ledger.
	revokeResidencyForEvictions(agentCtx, view, dec.ClearedCallIDs)
	_ = emit(agentcore.MicrocompactEvent{
		Reason:       dec.Reason,
		ClearedCount: len(dec.ClearedCallIDs),
		SavedTokens:  dec.SavedTokens,
	})
}

// revokeResidencyForEvictions applies the #4239 eviction rule (T3.5): every
// evicted tool call that was a read withdraws that call's residency vote from
// the ledger. The reverse index identifies each read call; a read the ledger
// cannot identify (recorded before the ledger existed, e.g. a restored
// session) triggers the defensive revoke-everything branch. Non-read tools
// (bash/grep/…) carry no residency evidence and change nothing.
func revokeResidencyForEvictions(agentCtx *agentcore.AgentContext, view agentcore.MessageList, cleared []string) {
	st := agentCtx.ReadFiles
	if st == nil || len(cleared) == 0 {
		return
	}
	clearedSet := make(map[string]bool, len(cleared))
	for _, id := range cleared {
		clearedSet[id] = true
	}
	var readCalls []string
	for _, m := range view {
		a, ok := m.(agentcore.AssistantMessage)
		if !ok {
			continue
		}
		for _, c := range a.ToolCalls() {
			if clearedSet[c.ID] && c.Name == "read" {
				readCalls = append(readCalls, c.ID)
			}
		}
	}
	if len(readCalls) > 0 {
		st.RevokeEvicted(readCalls)
	}
}

// reminderMaxFileChars / reminderMaxTotalChars are the zcode cap table for the
// post-compaction content re-injection: per-file and aggregate snapshot
// budgets (5 files / 5K per file / 50K total; the aggregate cannot be reached
// under the per-file cap and file count — it guards the table changing).
const (
	reminderMaxFileChars  = 5 * 1024
	reminderMaxTotalChars = 50 * 1024
)

// postCompactReminder renders the one-shot system-reminder for the files read
// in the compacted range (newest first, capped at 5 — zcode's cap table).
// When the readFileState ledger holds content snapshots for them (T3.5 随件③,
// closing the D-5 deviation), the reminder carries the snapshot contents so
// the model keeps working without an immediate re-read; without a snapshot
// (restored session, snapshot evicted) it degrades to the reference-hint form.
// Snapshots are from the last read and may be stale — the body says so.
// Returns "" when the range read nothing.
func postCompactReminder(rangeMsgs []agentcore.Message, st *agentcore.ReadFileState) string {
	var paths []string
	seen := make(map[string]bool)
	for i := len(rangeMsgs) - 1; i >= 0 && len(paths) < 5; i-- {
		a, ok := rangeMsgs[i].(agentcore.AssistantMessage)
		if !ok {
			continue
		}
		for _, call := range a.ToolCalls() {
			if call.Name != "read" || len(paths) >= 5 {
				continue
			}
			if p := readToolPath(call.Arguments); p != "" && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		return ""
	}
	var snaps []string
	anySnap := false
	if st != nil {
		snaps = make([]string, len(paths))
		for i, p := range paths {
			snaps[i], _ = st.ContentByArgPath(p)
			if snaps[i] != "" {
				anySnap = true
			}
		}
	}
	var b strings.Builder
	if !anySnap {
		b.WriteString("Your earlier tool results were compacted into a summary. You recently read these files, whose exact contents may no longer be in context — re-read them before relying on precise details:\n")
		for _, p := range paths {
			b.WriteString("- " + p + "\n")
		}
		return WrapSystemReminder(strings.TrimRight(b.String(), "\n"))
	}
	b.WriteString("Your earlier tool results were compacted into a summary. You recently read these files; the content below is a snapshot from your last read and may be stale — re-read a file before relying on precise details:\n")
	total := 0
	for i, p := range paths {
		snap := snaps[i]
		if snap == "" {
			b.WriteString("- " + p + " (no content snapshot; re-read if needed)\n")
			continue
		}
		if len(snap) > reminderMaxFileChars {
			snap = snap[:reminderMaxFileChars] + "\n… (snapshot truncated)"
		}
		if total+len(snap) > reminderMaxTotalChars {
			b.WriteString("- " + p + " (snapshot budget exhausted; re-read if needed)\n")
			continue
		}
		total += len(snap)
		b.WriteString("- " + p + "\n" + snap + "\n")
	}
	return WrapSystemReminder(strings.TrimRight(b.String(), "\n"))
}

// readToolPath extracts the string "path" argument from a read tool call.
func readToolPath(args json.RawMessage) string {
	var decoded struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &decoded); err != nil {
		return ""
	}
	return decoded.Path
}
