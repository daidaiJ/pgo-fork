// Typed compaction skip/failure reasons (T3.3 随件, minimax SkipReason 原则 +
// qwen/grok 失败分类): every "compaction did not happen" outcome carries a
// machine-readable reason on the CompactionEvent, and classifiable failures are
// wrapped in SkipError so the loop can count them for the circuit breaker
// without string-matching error text.
package compaction

import "fmt"

// SkipReason is the typed reason a compaction or microcompaction pass did not
// run, or the class of a failure that aborted it.
type SkipReason string

const (
	// SkipDisabled: compaction is disabled in settings.
	SkipDisabled SkipReason = "disabled"
	// SkipWindowUnknown: the model's context window is unknown (<= 0).
	SkipWindowUnknown SkipReason = "window-unknown"
	// SkipBelowThreshold: usage is under the trigger line.
	SkipBelowThreshold SkipReason = "below-threshold"
	// SkipNoCut: no valid cut point exists (nothing can be summarized).
	SkipNoCut SkipReason = "no-cut"
	// SkipNothingToSummarize: the summarization range is empty.
	SkipNothingToSummarize SkipReason = "nothing-to-summarize"
	// SkipDegenerateSummary: the summary came back degenerate (empty or under
	// the grok-derived 500-char floor) — treated as a failed attempt.
	SkipDegenerateSummary SkipReason = "degenerate-summary"
	// SkipInflated: the rebuild would not shrink the context (inflation guard,
	// qwen) — applying it would risk a compact→restore→recompact loop.
	SkipInflated SkipReason = "inflated"
	// SkipAPIError: the summarization request failed (transport/provider).
	SkipAPIError SkipReason = "api-error"
	// SkipCircuitOpen: consecutive-failure circuit breaker is open (3 strikes).
	SkipCircuitOpen SkipReason = "circuit-open"
	// SkipBelowMicroSavings: microcompaction candidates would save fewer than
	// the 256-token floor — not worth breaking the prompt-cache prefix.
	SkipBelowMicroSavings SkipReason = "below-micro-savings"
	// SkipNoCandidates: microcompaction found no eligible assistant-turn group.
	SkipNoCandidates SkipReason = "no-candidates"
)

// SkipError marks a compaction failure with its SkipReason class.
type SkipError struct {
	Reason SkipReason
	Err    error
}

func (e *SkipError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("compaction: %s", e.Reason)
	}
	return fmt.Sprintf("compaction: %s: %v", e.Reason, e.Err)
}

func (e *SkipError) Unwrap() error { return e.Err }

// NewSkipError wraps err under a SkipReason class.
func NewSkipError(reason SkipReason, err error) *SkipError {
	return &SkipError{Reason: reason, Err: err}
}

// SkipReasonOf extracts the SkipReason class from err, or "" when err carries
// none (e.g. a context cancellation or an unclassified error).
func SkipReasonOf(err error) SkipReason {
	var se *SkipError
	if !asSkipError(err, &se) {
		return ""
	}
	return se.Reason
}

// asSkipError walks the unwrap chain for a *SkipError (local errors.As to keep
// the package dependency-free of anything but agentcore/provider).
func asSkipError(err error, target **SkipError) bool {
	for err != nil {
		if se, ok := err.(*SkipError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// minSummaryChars is the degenerate-summary floor (grok's MIN_SUMMARY_SEED_CHARS
// equivalent): a cleaned summary shorter than this is treated as a failed
// attempt rather than applied, because a one-liner cannot carry the collapsed
// history forward.
const minSummaryChars = 500
