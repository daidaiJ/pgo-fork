// This file defines LiveConfig, the mutable run configuration a control command
// may change mid-session. It was moved verbatim from cmd/pigo (the former
// liveRunConfig) and exported so the run, repl, btw, status and goal
// subpackages can read and mutate it through the Host contract. The run closure
// reads it on every prompt, so a /model switch takes effect on the next turn.
// It carries no lock: it is read and written only on the REPL's single main
// goroutine (slash actions and the run are both invoked synchronously from the
// REPL loop, never concurrently).
package cli

import (
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/provider"
)

// LiveConfig is the mutable run configuration a control command may change
// mid-session.
type LiveConfig struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	Protocol     string
	// ThinkingLevel is the reasoning-effort level applied to each turn. It is
	// seeded from the resolved config chain and read on every prompt.
	ThinkingLevel agentcore.ThinkingLevel
	// ContextWindow is the model's total context-token budget, used to gate
	// automatic compaction. When 0 the window is unknown and auto-compaction is
	// disabled; the REPL seeds it with a conservative default so long sessions
	// still compact rather than overflow. Drivers resolve it via
	// ResolveContextWindow: the selected model's catalog window when known,
	// else DefaultContextWindow, lowered by an explicit [compaction] max_context.
	ContextWindow int
	// MaxContext is the user's [compaction] max_context setting carried into
	// the session so a mid-session /model switch re-resolves the effective
	// window against the same explicit cap (config stays highest priority).
	MaxContext config.MaxContext

	// FetchedModels is the online model catalog pulled from the live provider's
	// endpoint by an explicit "/models fetch" (issue #566), sorted and
	// deduplicated. It is session-lifetime state: /model prefers it over the
	// heuristic chain for ids it contains (so a fetched id stays on the gateway
	// that serves it) and /models fetch refreshes it. Nil until the first
	// successful fetch; never persisted.
	FetchedModels []string
	FetchedAt     time.Time
}

// DefaultContextWindow is the fallback context-token budget used when a model's
// true window is unknown. It is deliberately large so auto-compaction only fires
// on genuinely long sessions (threshold = window - ReserveTokens), never on
// ordinary short exchanges.
const DefaultContextWindow = 128000

// ResolveContextWindow resolves the effective compaction window for a run:
//
//  1. the selected model's catalog window (provider.Model.ContextWindow, seeded
//     from the preset catalog) when known — the compaction trigger derives from
//     the model's real budget rather than a hardcoded constant;
//  2. else DefaultContextWindow, the conservative fallback;
//  3. an explicit [compaction] max_context in the user config wins over both
//     when it lowers the value (fraction "0.80", percent "80%", or absolute
//     "100K" — see config.ParseMaxContext). It never raises the window: the
//     setting can only lower the auto-compaction trigger, matching the
//     "always clamped by the provider window" contract.
func ResolveContextWindow(prov provider.Provider, model string, maxCtx config.MaxContext) int {
	base := DefaultContextWindow
	if prov != nil {
		for _, m := range prov.Models() {
			if m.ID == model && m.ContextWindow > 0 {
				base = m.ContextWindow
				break
			}
		}
	}
	if v := maxCtx.Resolve(base); v > 0 && v < base {
		return v
	}
	return base
}
