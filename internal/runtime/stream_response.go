// This file implements streamAssistantResponse (US-003): it shapes the context
// into a provider request, resolves the API key dynamically, drives the
// provider stream, and back-fills the partial assistant message into the
// context while emitting message_start / message_update / message_end events.
package runtime

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
)

// streamRecoveryHint is the projection-only recovery prompt folded into the
// next user message after a mid-stream interruption (T1.2). It carries the
// step-Code five points: interrupted tools were not executed / confirmed work
// stands / shrink the next response / build large files incrementally /
// permission constraints still apply.
const streamRecoveryHint = "[Stream recovery] Your previous response was interrupted mid-stream, " +
	"so any tool calls in it were NOT executed. Work already confirmed by earlier tool results " +
	"remains valid — do not redo it. Keep your next response much smaller (about 50 lines / a few KB); " +
	"for large files, build them incrementally with write/edit chunks instead of resending the whole " +
	"payload. All permission constraints still apply."

// foldStreamRecovery returns the request projection with the recovery hint
// folded verbatim into the trailing user message when the context ends with a
// mid-stream interrupted failure: a stopReason-error assistant whose message
// classifies as a stream interruption (provider.IsStreamInterruption), or the
// silent-close EmptyResponse sentinel (agentcore.ErrStreamIncomplete).
//
// Projection-only hard semantics: the hint lives only in the request copy —
// never in agentCtx.Messages, never in the session file — so replay, rewind,
// and resume never inherit stale recovery state (it is recomputed from the
// context each request), and consecutive interruptions cannot stack copies.
// Because the hint rides the existing user message it inherits that message's
// timestamp; no synthetic history entry is created. Anything that does not
// match the shape (in particular no trailing user message to fold into) is
// returned unchanged: zero false positives beats coverage.
func foldStreamRecovery(msgs agentcore.MessageList) agentcore.MessageList {
	n := len(msgs)
	if n < 2 {
		return msgs
	}
	user, ok := msgs[n-1].(agentcore.UserMessage)
	if !ok {
		return msgs
	}
	failed, ok := msgs[n-2].(agentcore.AssistantMessage)
	if !ok || failed.StopReason != agentcore.StopReasonError {
		return msgs
	}
	if !provider.IsStreamInterruption(failed.ErrorMessage) &&
		failed.ErrorMessage != agentcore.ErrStreamIncomplete.Error() {
		return msgs
	}
	folded := user
	folded.Content = append(append(agentcore.ContentList{}, user.Content...),
		agentcore.NewTextContent(streamRecoveryHint))
	out := make(agentcore.MessageList, n)
	copy(out, msgs)
	out[n-1] = folded
	return out
}

// LoopConfig holds the pluggable behavior of the agent loop. Every hook is
// optional (nil = use the default). The pointer/func-field pattern mirrors pi's
// optional callbacks.
type LoopConfig struct {
	// Model is the model id passed to StreamFn.
	Model string
	// APIKey is the static fallback key when GetAPIKey is nil or returns "".
	APIKey string
	// ThinkingLevel is the reasoning effort for requests.
	ThinkingLevel agentcore.ThinkingLevel
	// Stream produces the provider stream. Required (defaults are wired by
	// callers/tests, e.g. a fake provider).
	Stream provider.StreamFn

	// TransformContext optionally rewrites the message list before conversion
	// (context trimming/injection). Contract: must not error; on failure return
	// a safe fallback. Runs first.
	TransformContext func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList
	// ConvertToLlm optionally filters UI-only messages. Defaults to identity.
	// Contract: must not error.
	ConvertToLlm func(msgs agentcore.MessageList) agentcore.MessageList
	// GetAPIKey optionally resolves a fresh key per request (handles short-lived
	// token expiry). Falls back to APIKey when nil or empty.
	GetAPIKey func(ctx context.Context, provider string) string
	// Provider is the provider name passed to GetAPIKey.
	Provider string

	// ContextWindow is the model's total context-token budget, used to decide
	// automatic compaction. When <= 0 the window is unknown and auto-compaction
	// is disabled (ShouldCompact returns false), so the loop behaves exactly as
	// before for callers that do not plumb it through.
	ContextWindow int
	// Compaction holds the thresholds/retention knobs for auto-compaction. Its
	// Enabled flag gates the feature independently of ContextWindow.
	Compaction compaction.CompactionSettings
	// SummaryStream produces the provider stream used to generate compaction
	// summaries. Defaults to Stream when nil.
	SummaryStream provider.StreamFn
	// SummaryModel is the model used for summarization. When zero, a model is
	// synthesized from Model/ContextWindow.
	SummaryModel provider.Model

	// Extra is forwarded to StreamConfig.Extra.
	Extra map[string]any
}

// streamAssistantResponse runs one assistant turn: it builds the request from
// agentCtx, streams the provider response, back-fills the partial into
// agentCtx.Messages, and returns the final assistant message. The sequence
// (transformContext → convertToLlm → resolve key → stream → drain) is kept
// identical to pi. It never returns an error for a request failure — such
// failures arrive as a terminal assistant message with stopReason error/aborted.
func streamAssistantResponse(ctx context.Context, agentCtx *agentcore.AgentContext, cfg LoopConfig, emit agentcore.EmitFunc) (agentcore.AssistantMessage, error) {
	// 1. derive the request view (T3.3 marker-entry model): compaction markers
	// collapse the summarized prefix, microcompact markers evict old tool
	// results, dangling tool calls get synthetic results. The raw context is
	// never touched — projection only.
	msgs := compaction.ProjectView(agentCtx.Messages)
	// 2. fold the projection-only stream-recovery hint (T1.2) before any
	// TransformContext runs: reminders append ephemeral messages at the tail,
	// so the interruption trigger must be evaluated on the real context tail.
	msgs = foldStreamRecovery(msgs)
	// 3. transformContext (optional, must not error).
	if cfg.TransformContext != nil {
		msgs = cfg.TransformContext(ctx, msgs)
	}
	// 4. convertToLlm (filter UI-only; default identity).
	if cfg.ConvertToLlm != nil {
		msgs = cfg.ConvertToLlm(msgs)
	}
	// 5. shape the LLM context.
	llm := provider.LlmContext{
		SystemPrompt: agentCtx.SystemPrompt,
		Messages:     msgs,
		Tools:        agentCtx.Tools,
	}
	// 6. resolve API key dynamically, fall back to static.
	key := cfg.APIKey
	if cfg.GetAPIKey != nil {
		if dyn := cfg.GetAPIKey(ctx, cfg.Provider); dyn != "" {
			key = dyn
		}
	}
	// 7. build the provider stream.
	stream, err := cfg.Stream(ctx, cfg.Model, llm, provider.StreamConfig{
		APIKey:        key,
		ThinkingLevel: cfg.ThinkingLevel,
		Extra:         cfg.Extra,
	})
	if err != nil {
		// Early "cannot build stream" failure: synthesize a terminal message so
		// the loop has a uniform assistant message to record.
		return newErrorAssistantMessage(cfg, err), nil
	}

	// 8. drain the stream, back-filling the partial into the context.
	addedPartial := false
	backfill := func(partial agentcore.AssistantMessage) {
		if !addedPartial {
			agentCtx.Messages = append(agentCtx.Messages, stampedMessage(partial))
			addedPartial = true
		} else {
			agentCtx.Messages[len(agentCtx.Messages)-1] = stampedMessage(partial)
		}
	}

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case provider.StreamStartEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageStartEvent{Message: e.Partial}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamTextEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageUpdateEvent{Message: e.Partial, AssistantMessageEvent: e}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamThinkingEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageUpdateEvent{Message: e.Partial, AssistantMessageEvent: e}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamToolCallEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageUpdateEvent{Message: e.Partial, AssistantMessageEvent: e}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamDoneEvent:
			finalizeMessage(agentCtx, e.Message, &addedPartial)
			if err := emit(ctx, agentcore.MessageEndEvent{Message: e.Message}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
			return e.Message, nil
		case provider.StreamErrorEvent:
			finalizeMessage(agentCtx, e.Message, &addedPartial)
			if err := emit(ctx, agentcore.MessageEndEvent{Message: e.Message}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
			return e.Message, nil
		}
	}

	// 9. stream ended without done/error: fall back to the stream result.
	final, resErr := stream.Result(ctx)
	if resErr != nil {
		return newErrorAssistantMessage(cfg, resErr), nil
	}
	finalizeMessage(agentCtx, final, &addedPartial)
	if err := emit(ctx, agentcore.MessageEndEvent{Message: final}); err != nil {
		return agentcore.AssistantMessage{}, err
	}
	return final, nil
}

// finalizeMessage replaces the placeholder partial with the final message, or
// appends it if the provider sent done/error without a prior start.
func finalizeMessage(agentCtx *agentcore.AgentContext, final agentcore.AssistantMessage, addedPartial *bool) {
	if *addedPartial {
		agentCtx.Messages[len(agentCtx.Messages)-1] = stampedMessage(final)
	} else {
		agentCtx.Messages = append(agentCtx.Messages, stampedMessage(final))
		*addedPartial = true
	}
}

// newErrorAssistantMessage builds a terminal assistant message for an early
// failure that never produced a provider stream.
func newErrorAssistantMessage(cfg LoopConfig, err error) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField:    agentcore.RoleAssistant,
		Model:        cfg.Model,
		Provider:     cfg.Provider,
		StopReason:   agentcore.StopReasonError,
		ErrorMessage: err.Error(),
	}
}
