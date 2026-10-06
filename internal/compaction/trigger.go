// Trigger-line and output-budget formulas (T4.4 余项). The full-compaction
// trigger leaves pi's flat `window − reserve` in favour of the minimax fusion
// registered in context-compaction-comparison.md D1/D10:
//
//   - A 线 (main formula): window − max(reserve, perTurn + margin) — the line
//     sinks below the pi baseline by the per-turn output budget, so a turn with
//     a large output cap triggers compaction early enough for that output to
//     fit (minimax settings.ts:18-37).
//   - B 线 (overflow pre-defense): min(0.95·w, w − reserve, w − out − margin) —
//     a defensive lower bound that only binds on very large windows or large
//     output budgets (minimax provider-budget.ts:17-42).
//   - per-model override table: product-defined ratio lines replace the
//     generic formula for the models that ship one (kimi strategy.ts
//     triggerRatio 0.85; MiniMax M3 512K/1M 90% product line).
//   - dynamic max_tokens: min(configured, max(outputFloor, window − estimate −
//     margin − thinkingReserve)) per request, the consume-once snapshot being
//     the per-request computation itself (minimax provider-budget.ts
//     resolveDynamicMaxTokens; see runtime's stamp site for the deviation note).
package compaction

import "strings"

// DefaultMarginTokens matches minimax's settings.ts margin: the safety gap
// between the trigger line and the per-turn output budget.
const DefaultMarginTokens = 2048

// OutputFloorTokens is the dynamic-output floor (minimax 输出下限保护): a
// window-derived max_tokens never sinks below it, so a nearly-full context
// cannot squeeze the model's reply to nothing.
const OutputFloorTokens = 4096

// triggerOverrides is the per-model trigger-ratio table (kimi/minimax 形态).
// Matching is a case-insensitive substring test on the model id, mirroring how
// kimi keys its overrides on model families rather than exact ids. Only lines
// with a recorded source ship here; an unmatched id falls through to the
// generic A/B formula.
var triggerOverrides = []struct {
	match []string
	ratio float64
}{
	// MiniMax M3 512K/1M modes use the product-defined 90% line
	// (context-compaction-comparison.md D1 minimax row).
	{match: []string{"minimax"}, ratio: 0.90},
	// kimi: used ≥ 0.85×window triggerRatio (kimi-code strategy.ts:18-28).
	{match: []string{"kimi", "moonshot"}, ratio: 0.85},
}

// ResolveTriggerRatio returns the per-model trigger-ratio override for a model
// id, or 0 when the generic formula applies.
func ResolveTriggerRatio(model string) float64 {
	m := strings.ToLower(model)
	for _, o := range triggerOverrides {
		for _, k := range o.match {
			if strings.Contains(m, k) {
				return o.ratio
			}
		}
	}
	return 0
}

// CompactionTriggerLine computes the generic full-compaction trigger line:
// min(A 线, B 线). With no per-turn output budget (perTurnTokens <= 0) it is
// exactly pi's baseline window − ReserveTokens, so callers that never seed an
// output cap keep the old behaviour bit-for-bit. A non-positive window returns
// 0 (unknown window ⇒ no trigger); a line that falls at or below zero for a
// known window means "always compact", matching ShouldCompact's semantics for
// tiny windows.
func CompactionTriggerLine(contextWindow, perTurnTokens int, s CompactionSettings) int {
	if contextWindow <= 0 {
		return 0
	}
	reserve := s.ReserveTokens
	if perTurnTokens <= 0 {
		return contextWindow - reserve
	}
	margin := s.MarginTokens
	// A 线: window − max(reserve, perTurn + margin).
	a := contextWindow - max(reserve, perTurnTokens+margin)
	// B 线 (overflow 前置防线): min(0.95w, w − reserve, w − out − margin).
	b := min(contextWindow-contextWindow/20, contextWindow-reserve, contextWindow-perTurnTokens-margin)
	return min(a, b)
}

// CompactionLine resolves the effective full-compaction trigger line for a
// run: the per-model ratio override wins when the model id matches the table,
// otherwise the generic A/B formula applies. The window is the effective
// (already max_context-clamped) window.
func CompactionLine(contextWindow, perTurnTokens int, s CompactionSettings, model string) int {
	if contextWindow <= 0 {
		return 0
	}
	if r := ResolveTriggerRatio(model); r > 0 {
		return int(float64(contextWindow) * r)
	}
	return CompactionTriggerLine(contextWindow, perTurnTokens, s)
}

// ResolveDynamicMaxTokens computes the per-request output budget (minimax
// resolveDynamicMaxTokens): min(configured, max(outputFloor, window − estimate
// − margin − thinkingReserve)). The configured cap (the model's declared
// per-response output limit) wins when it is the tighter bound; the
// window-derived bound keeps the reply inside what the remaining context can
// absorb, with the thinking reserve deducted first so reasoning models do not
// eat the visible reply's headroom. A non-positive window returns 0 (no hint);
// an unknown configured cap (0) leaves the budget purely window-derived.
func ResolveDynamicMaxTokens(contextWindow, estimateTokens, configured, thinkingReserve, margin int) int {
	if contextWindow <= 0 {
		return 0
	}
	derived := contextWindow - estimateTokens - margin - thinkingReserve
	if derived < OutputFloorTokens {
		derived = OutputFloorTokens
	}
	if configured > 0 && configured < derived {
		return configured
	}
	return derived
}
