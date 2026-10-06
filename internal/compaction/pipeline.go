// The Pipeline is the single owner of run-scoped compaction orchestration
// (T3.3.1): the strategy-pipeline layer the loop consults instead of
// orchestrating compaction itself. The loop keeps exactly two call sites:
//
//   - RequestView, just before each LLM request: one microcompaction pass
//     (the zcode double gate — token pressure on the derived line, or the
//     60-minute cache-cold idle window) followed by the request-view
//     projection (compaction markers, microcompact eviction, context edits,
//     half-pair repair) returned in one shot.
//   - AfterTurn, at each turn boundary: full auto-compaction when the request
//     view has outgrown its trigger line, then the post-settle utilization
//     observation.
//
// Everything the loop used to scatter across runLoop/afterTurn lives here:
// trigger-line resolution (T4.4 model-aware), the summarization chain, marker
// insertion (T3.3 marker-entry model), the inflation guard, the circuit
// breaker, eviction residency revocation, and the one-shot post-compaction
// reminder. The seams the loop injects at assembly time keep this package free
// of loop knowledge: the Summarizer (stream/model/key resolution is a RunConfig
// concern), checkpoint persistence (a runtime store concern), event emission
// and telemetry, and the wall clock.
//
// This is a behavior-preserving move from internal/runtime/loop.go (T3.3.1);
// the comment blocks travel with the code they describe.
package compaction

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// circuitBreakerLimit is how many consecutive compaction failures open the
// breaker for the rest of the run (qwen/zcode 3-strike value).
const circuitBreakerLimit = 3

// SummarizeFunc is the Summarizer seam: it runs the summarization LLM call for
// a full compaction over the request view. The loop injects it because the
// stream/model/API-key resolution behind it is RunConfig knowledge.
type SummarizeFunc func(
	ctx context.Context,
	view agentcore.MessageList,
	prevIdx int,
	prevSummary string,
	prevDetails *CompactionDetails,
) (*CompactionResult, error)

// PipelineConfig wires a Pipeline to its run environment. The loop assembly
// (runtime runLoop) builds it once per run; the settings fields are snapshots
// (the loop never mutates them mid-run) except Model, which is resolved live.
type PipelineConfig struct {
	// Settings holds the compaction thresholds/retention knobs.
	Settings CompactionSettings
	// ContextWindow is the effective (max_context-clamped) window; <= 0
	// disables both compaction passes.
	ContextWindow int
	// MaxOutputTokens is the model's declared per-response output cap (0 =
	// unknown); it feeds the trigger line's per-turn budget term.
	MaxOutputTokens int
	// Model resolves the current model id live at each decision — not a
	// snapshot: a mid-run model swap (PrepareNextTurn / /model) must move the
	// per-model trigger override with it.
	Model func() string

	// Emit forwards compaction lifecycle events (MicrocompactEvent,
	// CompactionStartEvent, CompactionEvent). Required.
	Emit func(ev agentcore.AgentEvent) error
	// Now supplies the wall clock for marker timestamps (the loop's
	// nowMillis; injectable for tests).
	Now func() int64
	// RecordContext, when set, observes context-utilization at the same two
	// points the loop's telemetry recorded before the move: the pre-compaction
	// peak (inside the full pass) and the post-settle figure (AfterTurn tail).
	RecordContext func(tokens, window int)
	// Summarize is the Summarizer seam (see SummarizeFunc). Required for the
	// full pass; a run that never crosses the line never calls it.
	Summarize SummarizeFunc
	// PersistedCount reports how many leading messages of the live list the
	// driver has already persisted (the marker-placement cursor). nil = 0.
	PersistedCount func() int
	// PersistCheckpoint, when set, persists the just-produced summary as a
	// session checkpoint after a successful full compaction (runtime owns the
	// checkpoint store; the pipeline only decides when).
	PersistCheckpoint func(ctx context.Context, view agentcore.MessageList, res *CompactionResult)
}

// Pipeline carries the run-scoped compaction state (the consecutive-failure
// count feeding the circuit breaker, the one-shot post-compaction reminder)
// plus the injected seams. Scoped to one run: a failed run already terminates,
// so a fresh run starts with a clean breaker.
type Pipeline struct {
	settings        CompactionSettings
	window          int
	maxOutputTokens int
	model           func() string

	emit             func(ev agentcore.AgentEvent) error
	now              func() int64
	recordContext    func(tokens, window int)
	summarize        SummarizeFunc
	persistedCount   func() int
	persistCheckpoint func(ctx context.Context, view agentcore.MessageList, res *CompactionResult)

	failures            int
	postCompactReminder string
}

// NewPipeline assembles the run-scoped compaction pipeline from its config.
func NewPipeline(cfg PipelineConfig) *Pipeline {
	model := cfg.Model
	if model == nil {
		model = func() string { return "" }
	}
	now := cfg.Now
	if now == nil {
		now = func() int64 { return 0 }
	}
	return &Pipeline{
		settings:          cfg.Settings,
		window:            cfg.ContextWindow,
		maxOutputTokens:   cfg.MaxOutputTokens,
		model:             model,
		emit:              cfg.Emit,
		now:               now,
		recordContext:     cfg.RecordContext,
		summarize:         cfg.Summarize,
		persistedCount:    cfg.PersistedCount,
		persistCheckpoint: cfg.PersistCheckpoint,
	}
}

// RequestView runs the pre-request half of the pipeline: one microcompaction
// pass, then the request-view projection. The returned view is what the caller
// hands to the provider, so the projection happens exactly once per request.
func (p *Pipeline) RequestView(ctx context.Context, agentCtx *agentcore.AgentContext) agentcore.MessageList {
	p.microcompact(ctx, agentCtx)
	return ProjectView(agentCtx.Messages)
}

// AfterTurn runs the post-turn half of the pipeline: the full auto-compaction
// pass, then the post-settle utilization observation (the loop's telemetry
// recorded the settled ratio after any compaction so the summary reports the
// current used/window figure; the observation reads the request view, whose
// projection collapses compacted history).
func (p *Pipeline) AfterTurn(ctx context.Context, agentCtx *agentcore.AgentContext) {
	p.autoCompact(ctx, agentCtx)
	if p.recordContext != nil && p.window > 0 {
		tokens := EstimateContextTokens(ProjectView(agentCtx.Messages)).Tokens
		p.recordContext(tokens, p.window)
	}
}

// TakePostCompactReminder pops the pending post-compaction reminder (one-shot;
// empty when no compaction ran since the last take).
func (p *Pipeline) TakePostCompactReminder() string {
	if p == nil || p.postCompactReminder == "" {
		return ""
	}
	t := p.postCompactReminder
	p.postCompactReminder = ""
	return t
}

// microcompact runs one microcompaction pass (T3.3): the zcode double gate —
// token pressure on the derived line min(0.9×autoLine, autoLine−2K), or the
// 60-minute idle window where the prompt cache is cold and eviction is free —
// decides whether to run; the decision evicts old regenerable tool results by
// appending one durable MicrocompactMessage marker (sticky: cleared results
// never resurrect, and the next PersistTurn carries the marker into the tree).
// Everything is decided on the request view; the raw list only ever grows. A
// pass is a no-op when compaction is disabled or the window is unknown.
func (p *Pipeline) microcompact(ctx context.Context, agentCtx *agentcore.AgentContext) {
	if !p.settings.Enabled || p.window <= 0 {
		return
	}
	view := ProjectView(agentCtx.Messages)
	tokens := EstimateContextTokens(view).Tokens
	// T4.4: derive the micro line from the model-aware full-compaction line
	// (zcode derivation), so per-model override lines propagate to the
	// microcompaction gate too.
	line := MicrocompactPressureLineFor(
		CompactionLine(p.window, p.maxOutputTokens, p.settings, p.model()))
	idle := IdleMillis(view, p.now()) >= MicrocompactIdleMillis
	if line <= 0 && !idle {
		return
	}
	if tokens < line && !idle {
		return
	}
	dec := DecideMicrocompact(view, tokens, line, idle)
	if len(dec.ClearedCallIDs) == 0 {
		if dec.SkipReason != "" && p.emit != nil {
			_ = p.emit(agentcore.MicrocompactEvent{Reason: dec.Reason, SkipReason: string(dec.SkipReason)})
		}
		return
	}
	agentCtx.Messages = append(agentCtx.Messages, dec.Marker(p.now()))
	// #4239 eviction rule (T3.5): the evicted read results no longer evidence
	// their files' residency — the next edit on those files is refused until
	// the model re-reads. Needs the pre-eviction view (the assistant calls live
	// there) and the ledger.
	revokeResidencyForEvictions(agentCtx, view, dec.ClearedCallIDs)
	if p.emit != nil {
		_ = p.emit(agentcore.MicrocompactEvent{
			Reason:       dec.Reason,
			ClearedCount: len(dec.ClearedCallIDs),
			SavedTokens:  dec.SavedTokens,
		})
	}
}

// autoCompact checks whether the request view has outgrown its usable window
// and, if so, compacts: it inserts a CompactionMessage marker into the live
// list (T3.3 marker-entry model) instead of rewriting it, so the persisted
// tree stays append-only and the request view is derived by projection.
//
// Compaction is a no-op when disabled, when the context window is unknown
// (<= 0), or when usage is under threshold. A compaction failure is non-fatal:
// the original context is preserved and a CompactionEvent carrying the typed
// SkipReason and ErrorMessage is emitted so the failure is observable without
// aborting the run (US-004).
func (p *Pipeline) autoCompact(ctx context.Context, agentCtx *agentcore.AgentContext) {
	if !p.settings.Enabled || p.window <= 0 {
		return
	}
	persisted := 0
	if p.persistedCount != nil {
		persisted = p.persistedCount()
	}
	if persisted > len(agentCtx.Messages) {
		persisted = len(agentCtx.Messages) // defensive against a stale driver cursor
	}
	// Every decision and the summarization input run on the request view (T3.3):
	// the raw list may hold superseded markers and pre-compaction history that
	// the view collapses. The view→raw map (not the marker-anchor formula)
	// converts the cut back: microcompact markers and context edits also drop
	// entries from the view, so the formula drifts once they are present.
	view, rawOf := ProjectViewMapped(agentCtx.Messages)
	before := EstimateContextTokens(view).Tokens
	// Record pre-compaction utilization so the ratio reflects the peak that
	// triggered (or nearly triggered) compaction even when the summary is read
	// mid-run. The post-settle observation overwrites it.
	if p.recordContext != nil {
		p.recordContext(before, p.window)
	}
	// T4.4: the trigger line is model-aware — per-model ratio override
	// (kimi/minimax) or the generic A/B formula (window − max(reserve,
	// perTurn+margin), B-line pre-defense). With no output cap seeded and an
	// unmatched model id this is pi's baseline window − reserve.
	line := CompactionLine(p.window, p.maxOutputTokens, p.settings, p.model())
	if before <= line {
		return
	}
	if p.failures >= circuitBreakerLimit {
		_ = p.emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(SkipCircuitOpen),
			ErrorMessage: "compaction circuit breaker open after 3 consecutive failures",
		})
		return
	}
	// Signal the start so a front-end can show an in-progress indicator while the
	// summarization request (an LLM call that blocks the loop) is in flight.
	_ = p.emit(agentcore.CompactionStartEvent{Reason: "threshold", TokensBefore: before})

	// Iterative chain (defect-① fix): the view's leading marker, when present,
	// is the previous compaction — summarize only what came after it, seeding
	// the file lists and feeding its summary into the update template.
	prevIdx := -1
	var prevSummary string
	var prevDetails *CompactionDetails
	if len(view) > 0 {
		if c, ok := view[0].(agentcore.CompactionMessage); ok {
			prevIdx = 0
			prevSummary = c.Summary
			if d, err := unmarshalDetails(c.Details); err == nil && (len(d.ReadFiles) > 0 || len(d.ModifiedFiles) > 0) {
				prevDetails = d
			}
		}
	}
	res, err := p.summarize(ctx, view, prevIdx, prevSummary, prevDetails)
	if err != nil {
		p.failures++
		_ = p.emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(SkipReasonOf(err)),
			ErrorMessage: err.Error(),
		})
		return
	}
	if res == nil {
		_ = p.emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(SkipNothingToSummarize),
		})
		return
	}
	p.failures = 0

	cut := res.FirstKeptIndex // view coordinates
	fullCut := ViewRawOf(rawOf, cut)
	newList, marker, insertAt := InsertCompactionMarker(agentCtx.Messages, res, fullCut, persisted, p.now())
	after := EstimateContextTokens(ProjectView(newList)).Tokens
	if after >= before {
		// Inflation guard (qwen): never apply a compaction that does not shrink
		// the view — a non-shrinking "compaction" risks a
		// compact→restore→recompact loop while paying for the summary call.
		_ = p.emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			SkipReason:   string(SkipInflated),
			ErrorMessage: "compaction rejected: post-compaction view would not shrink",
		})
		return
	}
	marker.TokensAfter = after
	newList[insertAt] = marker
	// Live-state re-injection (zcode 随件): surface the recently-read files the
	// summary replaced so the model re-reads before trusting stale memory.
	p.postCompactReminder = PostCompactReminderText(view[max(prevIdx+1, 0):cut], agentCtx.ReadFiles)
	// Persist a checkpoint of the collapsed prefix before inserting the marker so
	// a later run can reload it (infinite context, #480/#481). It reuses the
	// summary compaction just produced — no extra LLM call — and is best-effort.
	if p.persistCheckpoint != nil {
		p.persistCheckpoint(ctx, view, res)
	}
	agentCtx.Messages = newList
	_ = p.emit(agentcore.CompactionEvent{
		Reason:                "threshold",
		TokensBefore:          before,
		TokensAfter:           after,
		SummarizedCount:       max(0, cut-1),
		KeptCount:             len(view) - cut,
		SummaryUsage:          &res.SummaryUsage,
		WillRetriggerNextTurn: after > line,
	})
}

// InsertCompactionMarker returns msgs with res's compaction marker inserted at
// the T3.3 topology position, plus the inserted marker (FirstKeptIndex /
// KeptBefore / TokensAfter stamped) and its insert position. Rules:
//
//   - fullCut is the cut in raw-list coordinates (mapped from the view cut).
//   - The marker is inserted at max(fullCut, persisted): when the cut reaches
//     into unpersisted messages the marker sits at the cut; when the kept
//     window starts inside already-persisted territory the marker sits at the
//     branch tip (the only place PersistTurn's tail append can chain it into
//     the tree), and KeptBefore records how many kept entries precede it on
//     the path so replay projection restores them.
//
// Exported because the rebuild path (runtime /rebuild) shares the exact
// topology; now is the marker timestamp (the loop's nowMillis).
func InsertCompactionMarker(msgs agentcore.MessageList, res *CompactionResult, fullCut, persisted int, now int64) (agentcore.MessageList, agentcore.CompactionMessage, int) {
	insertAt := fullCut
	if insertAt < persisted {
		insertAt = persisted
	}
	marker := res.Message(now)
	marker.FirstKeptIndex = fullCut
	marker.KeptBefore = insertAt - fullCut
	out := make(agentcore.MessageList, 0, len(msgs)+1)
	out = append(out, msgs[:insertAt]...)
	out = append(out, marker)
	out = append(out, msgs[insertAt:]...)
	return out, marker, insertAt
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

// PostCompactReminderText renders the one-shot system-reminder for the files
// read in the compacted range (newest first, capped at 5 — zcode's cap table).
// When the readFileState ledger holds content snapshots for them (T3.5 随件③,
// closing the D-5 deviation), the reminder carries the snapshot contents so
// the model keeps working without an immediate re-read; without a snapshot
// (restored session, snapshot evicted) it degrades to the reference-hint form.
// Snapshots are from the last read and may be stale — the body says so.
// Returns "" when the range read nothing.
func PostCompactReminderText(rangeMsgs []agentcore.Message, st *agentcore.ReadFileState) string {
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
		return agentcore.WrapSystemReminder(strings.TrimRight(b.String(), "\n"))
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
	return agentcore.WrapSystemReminder(strings.TrimRight(b.String(), "\n"))
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

// unmarshalDetails decodes a compaction marker's opaque Details JSON into
// CompactionDetails (the pipeline's iterative chain).
func unmarshalDetails(raw json.RawMessage) (*CompactionDetails, error) {
	var d CompactionDetails
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
