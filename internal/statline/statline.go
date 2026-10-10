// Package statline implements the session-level token accounting (T7.3c / O1):
// one Record per accounted provider turn, the grok-aligned aggregation over a
// session's records, and the on-disk ledger (ledger.go) that makes the
// aggregate survive reload and resume.
//
// The package is a leaf: it depends on agentcore for the assistant-message
// shape and on nothing else in this repository, so the aggregation is a pure
// function that tests drive without a provider, a session store, or a terminal
// (R10). The status line, /usage and /stats all read the same Stats value — the
// single source of truth O1 asked for.
package statline

import (
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// Record is one accounted provider turn: the four token buckets, the timing the
// status line's perf segment reports, and the attribution flags a sub-agent
// turn carries. Fields are additive JSON with omitempty, so an older ledger
// keeps loading and a newer one never breaks an older reader.
type Record struct {
	// At is when the turn settled (UTC).
	At time.Time `json:"at"`
	// Model / Provider identify the endpoint the turn ran against. Empty when
	// the driver could not resolve them.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`

	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	CacheRead  int `json:"cacheRead,omitempty"`
	CacheWrite int `json:"cacheWrite,omitempty"`

	// TTFTMs is the time from request start to the first streamed byte;
	// DurationMs is the whole turn's wall time. 0 = unknown (a failed request,
	// or a driver that does not measure it).
	TTFTMs     int `json:"ttftMs,omitempty"`
	DurationMs int `json:"durationMs,omitempty"`

	// ThinkChars / StreamChars are the streamed reasoning / reply character
	// counts. The common wire reports no reasoning tokens, so the session think
	// share is estimated from characters (≈4 chars/token on both sides).
	ThinkChars  int `json:"thinkChars,omitempty"`
	StreamChars int `json:"streamChars,omitempty"`

	// Retries is the number of connect-time retry resubmissions this turn made
	// (the transport's 429/503/529 budget).
	Retries int `json:"retries,omitempty"`

	// Err marks a failed turn (provider error or aborted): it counts toward the
	// ✗ tally, and no cache-miss is inferred from a turn that never read a
	// prompt.
	Err bool `json:"err,omitempty"`

	// Subagent marks a record attributed to a task sub-agent's own loop;
	// AgentID names that child (the parent tool call's id). Incomplete marks a
	// turn that did not settle (errored or cancelled), mirroring the fork's
	// RecordSubagentUsage marking.
	Subagent   bool   `json:"subagent,omitempty"`
	AgentID    string `json:"agentId,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

// Timing carries the per-turn observations the loop measures itself and the
// record builder folds in alongside the provider usage payload.
type Timing struct {
	// TTFT is request-start to first-streamed-byte; Duration is the whole turn.
	TTFT     time.Duration
	Duration time.Duration
	// ThinkChars / StreamChars are the streamed reasoning / reply character
	// counts observed while the turn was in flight.
	ThinkChars  int
	StreamChars int
	// Retries is the number of connect-time retry resubmissions this turn made.
	Retries int
}

// FromTurn builds the record for one settled assistant turn (successful or
// failed) from its final message and the loop's own timing observations.
// model and provider are the request's resolved values, used when the message
// does not carry them (an early failure never reached a decoder).
func FromTurn(m agentcore.AssistantMessage, timing Timing, model, provider string) Record {
	r := Record{
		At:          time.Now().UTC(),
		Model:       m.Model,
		Provider:    m.Provider,
		TTFTMs:      millis(timing.TTFT),
		DurationMs:  millis(timing.Duration),
		ThinkChars:  timing.ThinkChars,
		StreamChars: timing.StreamChars,
		Retries:     timing.Retries,
	}
	if r.Model == "" {
		r.Model = model
	}
	if r.Provider == "" {
		r.Provider = provider
	}
	if u := m.Usage; u != nil {
		r.Input = u.InputTokens
		r.Output = u.OutputTokens
		r.CacheRead = u.CacheReadTokens
		r.CacheWrite = u.CacheWriteTokens
	}
	switch m.StopReason {
	case agentcore.StopReasonError, agentcore.StopReasonAborted:
		r.Err = true
	}
	return r
}

// millis converts a duration to whole milliseconds, floored at 0.
func millis(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(d.Milliseconds())
}

// Stats is the session-cumulative view the status line, /usage and /stats all
// read. Its semantics follow grok's default status line: everything is
// cumulative over the session (not per-run), retries and cache misses are
// counted only when they happened, and the perf fields describe the last
// settled turn rather than the current run.
type Stats struct {
	// Calls is the number of accounted turns, failures included; Errors is the
	// failed subset. The row renders "✓ (Calls-Errors)" plus "✗ Errors".
	Calls  int
	Errors int
	// Retries is the summed connect-time retry resubmissions.
	Retries int
	// CacheMisses counts uncached model calls beyond the session's first: turns
	// after the first whose prompt was served with no cache read at all.
	CacheMisses int

	Input      int
	Output     int
	CacheRead  int
	CacheWrite int

	ThinkChars  int
	StreamChars int

	// SubagentCalls / SubagentErrors count the records attributed to task
	// sub-agents; SubagentIncomplete counts those that did not settle.
	SubagentCalls      int
	SubagentErrors     int
	SubagentIncomplete int

	// LastTTFTMs / LastDurationMs / LastOutput describe the last settled turn,
	// the basis of the perf segment. Zero means unknown.
	LastTTFTMs     int
	LastDurationMs int
	LastOutput     int
	LastModel      string

	// Since / Until bound the accounted records (zero when there are none).
	Since time.Time
	Until time.Time
}

// Aggregate folds a session's records into the cumulative Stats. It is a pure
// function: the ledger, the live session and the tests all feed it the same
// slice and get the same answer.
func Aggregate(recs []Record) Stats {
	var s Stats
	for i, r := range recs {
		s.Calls++
		s.Retries += r.Retries
		s.Input += r.Input
		s.Output += r.Output
		s.CacheRead += r.CacheRead
		s.CacheWrite += r.CacheWrite
		s.ThinkChars += r.ThinkChars
		s.StreamChars += r.StreamChars
		if r.Err {
			s.Errors++
		}
		if r.Subagent {
			s.SubagentCalls++
			if r.Err {
				s.SubagentErrors++
			}
			if r.Incomplete {
				s.SubagentIncomplete++
			}
		}
		// Cache misses: grok's "uncached model calls beyond the session's first"
		// — the first call pays for the cold prefix by definition, and a failed
		// call never read a prompt at all.
		if i > 0 && !r.Err && r.CacheRead == 0 {
			s.CacheMisses++
		}
		if !r.At.IsZero() {
			if s.Since.IsZero() || r.At.Before(s.Since) {
				s.Since = r.At
			}
			if r.At.After(s.Until) {
				s.Until = r.At
			}
		}
		s.LastTTFTMs = r.TTFTMs
		s.LastDurationMs = r.DurationMs
		s.LastOutput = r.Output
		s.LastModel = r.Model
	}
	return s
}

// OK is the ✓ tally: turns that completed (a failed turn is counted by Errors
// instead).
func (s Stats) OK() int {
	if s.Calls <= s.Errors {
		return 0
	}
	return s.Calls - s.Errors
}

// CachePercent is the cache-read share of the session's prompt tokens and
// whether it is known. The denominator adds the separate cache-read bucket
// (anthropic splits it out of the prompt count; openai folds it in and reports
// the bucket as zero, in which case the share reads 0% — the honest "no cache
// read observed" value).
func (s Stats) CachePercent() (float64, bool) {
	denom := s.Input + s.CacheRead
	if denom <= 0 {
		return 0, false
	}
	return 100 * float64(s.CacheRead) / float64(denom), true
}

// ThinkPercent is the estimated reasoning share of the session's streamed
// output, and whether it is known.
func (s Stats) ThinkPercent() (float64, bool) {
	denom := s.StreamChars + s.ThinkChars
	if denom <= 0 {
		return 0, false
	}
	return 100 * float64(s.ThinkChars) / float64(denom), true
}

// LastTPS derives the last turn's output rate from its generation window
// (duration minus first-byte latency) and whether it is known.
func (s Stats) LastTPS() (float64, bool) {
	gen := s.LastDurationMs - s.LastTTFTMs
	if gen <= 0 || s.LastOutput <= 0 {
		return 0, false
	}
	return float64(s.LastOutput) / (float64(gen) / 1000), true
}
