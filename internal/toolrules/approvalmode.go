// approvalmode.go is the session-scoped approval posture (T7.6): plan /
// ask / all. The engine consumes only the plan arm (its read-only gate);
// the ask/all distinction lives in the drivers' TrustedFunc closure, which
// already owns the trust fast path. Mode is per-session state — it is
// derived at startup (--approve seeds all, everything else ask) and cycled
// at runtime (shift+tab, /mode); it is never persisted.
package toolrules

import (
	"fmt"
	"sync/atomic"
)

// ApprovalMode is the session's approval posture (T7.6).
type ApprovalMode int

const (
	// ModeAsk asks per effect call (the zero value: the restricted
	// posture). Durable trust grants (trust.json / session trust) still
	// fast-path their directories — they are the user's standing decision.
	ModeAsk ApprovalMode = iota
	// ModePlan allows read-only investigation and blocks every effect call
	// at the engine's plan gate with a plan-directed message.
	ModePlan
	// ModeAll runs side-effect tools without per-call confirmation (the
	// --approve semantics applied to the whole session).
	ModeAll
)

// String renders the display name ("always-approve" for all — the S14
// composer tag's established word for the blanket grant).
func (m ApprovalMode) String() string {
	switch m {
	case ModePlan:
		return "plan"
	case ModeAll:
		return "always-approve"
	default:
		return "ask"
	}
}

// ParseApprovalMode maps a /mode argument onto the posture. "all" and its
// display alias "always-approve" are accepted; everything else is a usage
// refusal (fail loud, no silent fallback).
func ParseApprovalMode(s string) (ApprovalMode, error) {
	switch s {
	case "ask":
		return ModeAsk, nil
	case "plan":
		return ModePlan, nil
	case "all", "always-approve":
		return ModeAll, nil
	default:
		return ModeAsk, fmt.Errorf("mode: unknown approval mode %q (want plan|ask|all)", s)
	}
}

// Next returns the next posture in the shift+tab ring: ask → plan → all →
// ask (grok's Normal → Plan → Always-Approve ring; its classifier Auto arm
// has no pigo equivalent).
func (m ApprovalMode) Next() ApprovalMode {
	switch m {
	case ModeAsk:
		return ModePlan
	case ModePlan:
		return ModeAll
	default:
		return ModeAsk
	}
}

// ModeState is a concurrency-safe ApprovalMode holder. The engine reads it
// from tool-executor goroutines while the front-end loop writes it, so a
// plain field would race; the atomic keeps both sides honest.
type ModeState struct {
	v atomic.Int32
}

// NewModeState seeds a holder.
func NewModeState(m ApprovalMode) *ModeState {
	s := &ModeState{}
	s.v.Store(int32(m))
	return s
}

// Mode reads the current posture.
func (s *ModeState) Mode() ApprovalMode { return ApprovalMode(s.v.Load()) }

// Set writes the posture.
func (s *ModeState) Set(m ApprovalMode) { s.v.Store(int32(m)) }
