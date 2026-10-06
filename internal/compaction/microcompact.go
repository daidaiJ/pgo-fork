// Microcompaction (T3.3): evict old, REGENERABLE tool results from the request
// view to make room before full compaction becomes necessary. zcode/qwen fusion
// as registered in wiki/port/micro-compaction.md §2:
//
//   - Trigger (zcode double gate): a derived pressure line
//     min(0.9×autoLine, autoLine−2K) with autoLine = window − reserveTokens, or
//     ≥60 minutes of idle (the prompt cache is cold anyway — eviction is free).
//   - Candidates: tool results grouped by assistant turn; only a whitelist of
//     regenerable tools is eligible; error results and results carrying image
//     blocks are exempt; the most recent KeepRecentTurns groups are always kept.
//   - Savings floor (zcode 256 tokens): below it nothing happens — evicting a
//     tiny result is not worth breaking the prompt-cache prefix.
//   - Low water (qwen): under pressure, evict oldest-first until the view is
//     under half the pressure line; on idle, evict everything eligible.
//
// The decision is durable and sticky: one MicrocompactMessage is appended to
// the live list (and thus the session tree via the next PersistTurn). The
// projection (ProjectView) replaces the matching tool results with one-line
// placeholders; cleared results never resurrect because the marker never goes
// away.
package compaction

import (
	"github.com/smallnest/pigo/internal/agentcore"
)

// Microcompact tuning constants (fusion values; see spec §2.3/§2.4).
const (
	// MicrocompactKeepRecentTurns: the newest N assistant tool-turns are never
	// candidates (qwen/zcode both keep 5).
	MicrocompactKeepRecentTurns = 5
	// MicrocompactMinSavingsTokens: aggregate savings floor (zcode strict tier,
	// the prompt-cache-break guard).
	MicrocompactMinSavingsTokens = 256
	// MicrocompactIdleMillis: idle gap that opens the cache-cold eviction window.
	MicrocompactIdleMillis int64 = 60 * 60 * 1000
	// microPressureMarginK: the pressure line sits 2K under the full-compaction
	// line so microcompaction fires first.
	microPressureMarginK = 2000
	// microPlaceholderTokens: the estimated footprint of one placeholder line.
	microPlaceholderTokens = 30
)

// microcompactWhitelist: regenerable tool outputs only (pigo tool face —
// qwen's 10-tool list ∪ zcode's 9-tool list, clipped to what pigo ships;
// control/task/edit/write/todo/goal/schedule tools are excluded, which folds
// minimax's "control tools exempt" discipline into the whitelist's complement).
var microcompactWhitelist = map[string]bool{
	"read":          true,
	"bash":          true,
	"bash_output":   true,
	"grep":          true,
	"find":          true,
	"ls":            true,
	"webfetch":      true,
	"websearch":     true,
	"memory_search": true,
}

// MicrocompactPressureLine derives the microcompaction trigger line from the
// full-compaction line (zcode model: the micro line must fire first):
// min(0.9×autoLine, autoLine−2K) with autoLine = contextWindow − reserveTokens.
// Returns 0 when the window is unknown or the derivation is degenerate.
func MicrocompactPressureLine(contextWindow, reserveTokens int) int {
	autoLine := contextWindow - reserveTokens
	if autoLine <= microPressureMarginK {
		return 0
	}
	nineTenths := (autoLine * 9) / 10
	withMargin := autoLine - microPressureMarginK
	if nineTenths < withMargin {
		return nineTenths
	}
	return withMargin
}

// microGroup is one assistant tool-turn: the assistant message and the results
// that follow it.
type microGroup struct {
	// start/end are view indices: start = the assistant message, results span
	// (start, end].
	start, end int
	// callIDs/results are the evictable results (whitelist-passing already).
	callIDs  []string
	savings  int
	evictAll bool // true when every call in the group is evictable
}

// collectMicroGroups walks the view and returns the assistant tool-turn groups
// (oldest first). A group is evictAll=true when ALL of its results are
// whitelist tools, non-error, image-free, and not already cleared; one
// disqualifying result makes the whole group ineligible (zcode whole-group
// discipline — no parallel-call orphans).
func collectMicroGroups(view agentcore.MessageList, cleared map[string]bool) []microGroup {
	type pending struct {
		start    int
		callIDs  []string
		savings  int
		evictAll bool
	}
	var groups []microGroup
	var cur *pending
	for i, m := range view {
		switch t := m.(type) {
		case agentcore.AssistantMessage:
			if cur != nil {
				groups = append(groups, microGroup{start: cur.start, end: i - 1, callIDs: cur.callIDs, savings: cur.savings, evictAll: cur.evictAll && len(cur.callIDs) > 0})
				cur = nil
			}
			calls := t.ToolCalls()
			if len(calls) == 0 {
				continue
			}
			p := &pending{start: i, evictAll: true}
			for _, c := range calls {
				if !microcompactWhitelist[c.Name] {
					p.evictAll = false
					continue
				}
				p.callIDs = append(p.callIDs, c.ID)
			}
			cur = p
		case agentcore.ToolResultMessage:
			if cur == nil {
				continue
			}
			savings := EstimateTokens(m) - microPlaceholderTokens
			if t.IsError || hasImageBlock(t.Content) || cleared[t.ToolCallID] || savings <= 0 {
				cur.evictAll = false
				continue
			}
			cur.savings += savings
		default:
			if cur != nil {
				groups = append(groups, microGroup{start: cur.start, end: i - 1, callIDs: cur.callIDs, savings: cur.savings, evictAll: cur.evictAll && len(cur.callIDs) > 0})
				cur = nil
			}
		}
	}
	if cur != nil {
		groups = append(groups, microGroup{start: cur.start, end: len(view) - 1, callIDs: cur.callIDs, savings: cur.savings, evictAll: cur.evictAll && len(cur.callIDs) > 0})
	}
	return groups
}

// hasImageBlock reports whether the content list contains an image block
// (zcode: media-bearing results are permanently exempt — the model cannot
// re-view an evicted image from a text placeholder).
func hasImageBlock(content agentcore.ContentList) bool {
	for _, c := range content {
		if _, ok := c.(agentcore.ImageContent); ok {
			return true
		}
	}
	return false
}

// MicrocompactDecision is the outcome of a microcompaction pass.
type MicrocompactDecision struct {
	// ClearedCallIDs are the tool-call ids evicted by the new marker (empty
	// when nothing was evicted).
	ClearedCallIDs []string
	// SavedTokens is the estimated token footprint removed from the view.
	SavedTokens int
	// Reason is "pressure" or "idle"; empty when no eviction happened.
	Reason string
	// SkipReason is set when the pass was triggered but evicted nothing.
	SkipReason SkipReason
}

// DecideMicrocompact picks the eviction set. view is the request view; tokens
// its current estimate; pressureLine the derived trigger line (0 disables the
// pressure gate). Under pressure the oldest eligible groups are evicted until
// the view is under half the line (qwen low water); on idle everything
// eligible is evicted (the cache is cold — eviction is free).
func DecideMicrocompact(view agentcore.MessageList, tokens, pressureLine int, idle bool) MicrocompactDecision {
	if pressureLine <= 0 && !idle {
		return MicrocompactDecision{}
	}
	underPressure := !idle && tokens >= pressureLine
	if !underPressure && !idle {
		return MicrocompactDecision{}
	}
	cleared := ExistingClearedCallIDs(view)
	groups := collectMicroGroups(view, cleared)
	// Never touch the newest KeepRecentTurns groups.
	eligible := groups
	if len(groups) > MicrocompactKeepRecentTurns {
		eligible = groups[:len(groups)-MicrocompactKeepRecentTurns]
	} else {
		eligible = nil
	}

	var decision MicrocompactDecision
	remaining := tokens
	switch {
	case idle:
		// Cache-cold window: evict everything eligible regardless of size.
		for _, g := range eligible {
			if !g.evictAll {
				continue
			}
			decision.ClearedCallIDs = append(decision.ClearedCallIDs, g.callIDs...)
			decision.SavedTokens += g.savings
		}
		decision.Reason = "idle"
	case underPressure:
		// Oldest-first until below half the line (qwen low water); a group
		// whose eviction would overshoot is still taken (zcode semantics:
		// whole-group eviction, no partial groups).
		target := pressureLine / 2
		for _, g := range eligible {
			if remaining < target {
				break
			}
			if !g.evictAll {
				continue
			}
			decision.ClearedCallIDs = append(decision.ClearedCallIDs, g.callIDs...)
			decision.SavedTokens += g.savings
			remaining -= g.savings
		}
		decision.Reason = "pressure"
	}
	if len(decision.ClearedCallIDs) == 0 {
		decision.Reason = ""
		decision.SkipReason = SkipNoCandidates
		return decision
	}
	if decision.SavedTokens < MicrocompactMinSavingsTokens {
		// Not worth breaking the prompt-cache prefix (zcode 256-token floor).
		decision.ClearedCallIDs = nil
		decision.SavedTokens = 0
		decision.Reason = ""
		decision.SkipReason = SkipBelowMicroSavings
		return decision
	}
	return decision
}

// ExistingClearedCallIDs unions the call ids of every MicrocompactMessage in
// msgs — the sticky eviction set. Already-cleared results are never re-cleared
// (their placeholder footprint is tiny, and a fresh marker would be noise).
func ExistingClearedCallIDs(msgs agentcore.MessageList) map[string]bool {
	cleared := make(map[string]bool)
	for _, m := range msgs {
		if mc, ok := m.(agentcore.MicrocompactMessage); ok {
			for _, id := range mc.ClearedCallIDs {
				cleared[id] = true
			}
		}
	}
	return cleared
}

// Marker builds the durable decision record for the loop to append.
func (d MicrocompactDecision) Marker(now int64) agentcore.MicrocompactMessage {
	return agentcore.MicrocompactMessage{
		RoleField:      agentcore.RoleMicrocompact,
		ClearedCallIDs: d.ClearedCallIDs,
		SavedTokens:    d.SavedTokens,
		Timestamp:      now,
	}
}

// LastActivityMillis returns the newest message timestamp in msgs, or 0 when
// none carries one (unknown activity never opens the idle window).
func LastActivityMillis(msgs agentcore.MessageList) int64 {
	var newest int64
	for _, m := range msgs {
		var ts int64
		switch t := m.(type) {
		case agentcore.UserMessage:
			ts = t.Timestamp
		case agentcore.AssistantMessage:
			ts = t.Timestamp
		case agentcore.ToolResultMessage:
			ts = t.Timestamp
		case agentcore.CompactionMessage:
			ts = t.Timestamp
		case agentcore.MicrocompactMessage:
			ts = t.Timestamp
		}
		if ts > newest {
			newest = ts
		}
	}
	return newest
}

// IdleMillis reports the idle gap in milliseconds given the wall clock;
// returns 0 when no message carries a timestamp (never idle on unknowns).
func IdleMillis(msgs agentcore.MessageList, now int64) int64 {
	last := LastActivityMillis(msgs)
	if last <= 0 {
		return 0
	}
	if now <= last {
		return 0
	}
	return now - last
}
