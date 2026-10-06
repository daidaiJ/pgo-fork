package runtime

import (
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// This file implements the sub-agent result envelope (T5.1, kimi semantics per
// wiki/port/subagent-result-envelope.md): when a task sub-agent settles, the
// parent receives a structured envelope (agent_id / status / stop_reason /
// next_step) instead of a bare text-or-error, so a weak parent model can tell
// WHY the child stopped and what to do next rather than improvising.
//
// Semantics (kimi formatForegroundAgentSuccess / NEXT_STEP_BY_REASON, adapted):
//
//   - completed (end_turn + non-empty text): the final message is the sole
//     handoff — the tool result text is returned verbatim (no envelope prose
//     interleaved) and the envelope rides in AgentToolResult.Details, so the
//     happy path is byte-identical to the pre-envelope behavior.
//   - every other outcome (max_tokens / error / cancelled / no_final_message)
//     formats as a fixed-field text envelope prepended to the (capped) body,
//     returned as a normal (non-error) tool result. The failure is carried by
//     the structured fields, not by Go-error plumbing, so the parent model can
//     read and act on next_step.
//
// Deviations from kimi are registered in the spec (§7): v1 has no resume
// channel (child sessions are not persisted), so resume_hint/next_step wording
// is re-dispatch oriented and the resume ownership checks land with resume in
// a later iteration.

// maxEnvelopeBody caps the free-text body embedded in a failed envelope (kimi
// caps reason text at 2000 chars): an error dump or partial output is context
// for the parent's decision, not a replacement for the child's report.
const maxEnvelopeBody = 2000

// Sub-agent status values (envelope "status" field).
const (
	SubAgentStatusCompleted = "completed"
	SubAgentStatusFailed    = "failed"
)

// SubAgentEnvelope is the structured outcome of one task sub-agent run. It is
// attached to every task tool result in AgentToolResult.Details (so the TUI and
// telemetry can consume it without parsing text) and, for non-completed
// outcomes, is also rendered as the fixed-field header of the result text.
type SubAgentEnvelope struct {
	// AgentID is the parent task call's tool-call id. It keys the TUI panel row
	// and will key resume ownership checks when the resume channel lands.
	AgentID string `json:"agent_id"`
	// Status is "completed" or "failed".
	Status string `json:"status"`
	// StopReason is the normalized stop vocabulary: "completed",
	// "max_tokens", "error", "cancelled", or "no_final_message".
	StopReason string `json:"stop_reason"`
	// NextStep is the guidance for the parent model (empty when completed —
	// the final message below the envelope is the only handoff needed).
	NextStep string `json:"next_step,omitempty"`
}

// nextStepFor maps a normalized stop reason to the parent-model guidance,
// following kimi's NEXT_STEP_BY_REASON table adapted for a v1 without resume
// (re-dispatch instead of "resume the same child").
func nextStepFor(stopReason string) string {
	switch stopReason {
	case SubAgentStatusCompleted, agentcore.StopReasonEndTurn:
		return ""
	case "max_tokens":
		return "The sub-agent hit its output limit before finishing; its partial output is below. " +
			"Split the remaining work into a smaller follow-up task, or complete it yourself; " +
			"if neither is feasible, report the partial result and the shortfall to the user."
	case "error":
		return "The sub-agent failed. Re-dispatch with a rewritten or smaller task, or handle it yourself; " +
			"if neither works, report the failure and the reason above to the user."
	case "cancelled":
		return "The sub-agent was stopped by the user. Do not restart it unless the user asks."
	case "no_final_message":
		return "The sub-agent produced no final report. Re-dispatch with a more explicit instruction to report its findings, " +
			"or verify the outcome yourself."
	default:
		return "Re-dispatch with a rewritten or smaller task, or handle it yourself; " +
			"if neither works, report the failure to the user."
	}
}

// stopReasonOf normalizes the child's final assistant message into the envelope
// stop vocabulary. An empty final message means the child never produced an
// assistant turn (no_final_message); "length" maps to max_tokens and "aborted"
// to cancelled (kimi vocabulary).
func stopReasonOf(final *agentcore.AssistantMessage) string {
	if final == nil {
		return "no_final_message"
	}
	switch final.StopReason {
	case agentcore.StopReasonEndTurn, agentcore.StopReasonToolUse:
		return SubAgentStatusCompleted
	case agentcore.StopReasonLength:
		return "max_tokens"
	case agentcore.StopReasonAborted:
		return "cancelled"
	case agentcore.StopReasonError:
		return "error"
	default:
		return "error"
	}
}

// buildEnvelope assembles the envelope for a settled child run. body is the
// child's final text (or the synthesized error text); it is returned capped for
// non-completed outcomes so a runaway failure does not flood the parent.
func buildEnvelope(agentID string, final *agentcore.AssistantMessage, body string) (SubAgentEnvelope, string) {
	reason := stopReasonOf(final)
	env := SubAgentEnvelope{AgentID: agentID, StopReason: reason}
	if reason == SubAgentStatusCompleted && strings.TrimSpace(body) != "" {
		env.Status = SubAgentStatusCompleted
		return env, body
	}
	env.Status = SubAgentStatusFailed
	if reason == SubAgentStatusCompleted { // end_turn but no text
		reason = "no_final_message"
		env.StopReason = reason
	}
	env.NextStep = nextStepFor(reason)
	// Body fallback chain (mirrors the child-side diagnostic): when the final
	// message carries no content, the loop's synthesized diagnostic lives in
	// ErrorMessage — surface it so the envelope always carries its cause. A
	// bare end_turn-without-text stays bodyless (the no_final_message
	// next_step is the actionable part).
	if strings.TrimSpace(body) == "" && final != nil && final.ErrorMessage != "" {
		body = final.ErrorMessage
	}
	return env, capBody(body)
}

// capBody truncates s to maxEnvelopeBody bytes on a rune boundary, appending an
// explicit truncation marker so the parent knows the body was clipped.
func capBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxEnvelopeBody {
		return s
	}
	// Walk back to a rune boundary before cutting.
	cut := maxEnvelopeBody
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n… (truncated)"
}

// Format renders the envelope as the fixed-field header of a failed result,
// followed by "---" and the (capped) body. The field-per-line layout is the
// weak-model-friendly contract: status/stop_reason/next_step are greppable and
// cannot be paraphrased away by the child's prose.
func (e SubAgentEnvelope) Format(body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[subagent result]\nagent_id: %s\nstatus: %s\nstop_reason: %s\n", e.AgentID, e.Status, e.StopReason)
	if e.NextStep != "" {
		fmt.Fprintf(&b, "next_step: %s\n", e.NextStep)
	}
	b.WriteString("---\n")
	b.WriteString(body)
	return b.String()
}
