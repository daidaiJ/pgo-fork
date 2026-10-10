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
	// MaxOutputTokens is the selected model's declared per-response output cap
	// (0 = unknown). It feeds the per-model compaction trigger line and the
	// dynamic max_tokens budget stamp (T4.4); re-resolved on /model switches
	// together with ContextWindow.
	MaxOutputTokens int

	// FetchedModels is the online model catalog pulled from the live provider's
	// endpoint by an explicit "/models fetch" (issue #566), sorted and
	// deduplicated. It is session-lifetime state: /model prefers it over the
	// heuristic chain for ids it contains (so a fetched id stays on the gateway
	// that serves it) and /models fetch refreshes it. Nil until the first
	// successful fetch; never persisted.
	FetchedModels []string
	FetchedAt     time.Time

	// ModelProfiles is the config.toml [models."<id>"] face (T7.3 实测反馈):
	// the /model switcher's face of record — the switcher lists these ids and
	// a switch to one rebuilds the provider from the profile. Empty when the
	// config declares no profiles (the preset catalog + fetched ids stay the
	// fallback face). Seeded at startup; keys never rendered into logs.
	ModelProfiles map[string]config.ModelProfile
	// ProviderConfigs is the config.toml [provider."<id>"] face (T8.1): the
	// connection sections a profile's provider reference inherits from
	// (per-field) when a mid-session switch re-resolves the connection. Nil
	// when the config declares no sections; never logged.
	ProviderConfigs map[string]config.ProviderSpec
	// Proxy is the startup connection's egress proxy URL (T8.1): the resolved
	// model profile's proxy, else its provider's. A bare-model /model switch
	// re-resolves with it so the new driver keeps riding the proxy; a profile
	// switch recomputes it from the profile/provider face. Empty = the
	// default transport.
	Proxy string
}

// ProfileFor resolves a model profile by id (exact → case-insensitive → wire
// model). It is the switch path's lookup over the seeded config face.
func (l *LiveConfig) ProfileFor(id string) (string, config.ModelProfile, bool) {
	if l == nil {
		return "", config.ModelProfile{}, false
	}
	return config.FileConfig{Models: l.ModelProfiles}.ProfileFor(id)
}

// SeedContextWindow resolves the startup compaction window: an explicit
// config-profile value wins, otherwise the model's catalog window (fallback
// DefaultContextWindow) lowered by the [compaction] max_context cap.
func SeedContextWindow(prov provider.Provider, model string, maxCtx config.MaxContext, explicit int) int {
	if explicit > 0 {
		return explicit
	}
	return ResolveContextWindow(prov, model, maxCtx)
}

// SeedMaxOutputTokens resolves the startup output cap the same way.
func SeedMaxOutputTokens(prov provider.Provider, model string, explicit int) int {
	if explicit > 0 {
		return explicit
	}
	return ResolveMaxOutputTokens(prov, model)
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

// ResolveMaxOutputTokens resolves the selected model's declared per-response
// output cap from the provider's model catalog (0 = unknown). Same discipline
// as ResolveContextWindow: only catalog-declared values are used — an unknown
// cap leaves the dynamic max_tokens stamp and the trigger-line perTurn term
// inert instead of guessing.
func ResolveMaxOutputTokens(prov provider.Provider, model string) int {
	if prov == nil {
		return 0
	}
	for _, m := range prov.Models() {
		if m.ID == model && m.MaxOutputTokens > 0 {
			return m.MaxOutputTokens
		}
	}
	return 0
}
