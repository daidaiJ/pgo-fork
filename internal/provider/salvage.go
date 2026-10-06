// This file implements tool-call truncation salvage (T6.7, grok参照
// LengthPolicy=CompleteToolCalls default, wiki/prototype/grok-build-proxy-
// sampler.md §1.3): when a provider cuts a response at max_tokens, the stream
// often already carries COMPLETE tool calls followed by nothing usable — a
// stop reason of "length" would make the loop treat the turn as a dead end
// even though the tool calls are executable. The salvage rewrites such a turn
// into a normal tool-call stop, dropping the unusable tail.
//
// Scope: this is the parse-layer rescue only. A turn truncated BEFORE every
// tool call is complete (invalid/partial JSON arguments, missing tool name)
// keeps its length stop — there is nothing safe to execute (the grok
// CompletePartial state, which would pad partial JSON at the cut point, is
// deliberately not implemented; registered as a spec deviation).
package provider

import (
	"encoding/json"

	"github.com/smallnest/pigo/internal/agentcore"
)

// SalvageLengthTruncation rewrites a max_tokens-truncated assistant message
// into an executable tool-call turn when possible, reporting whether it did.
//
// The salvage applies when msg stopped on length AND carries at least one tool
// call AND every tool call is complete: a non-empty name and arguments that
// are either empty (normalized to "{}" by the decoders) or valid JSON. On
// success the stop reason becomes tool_use and any content blocks after the
// LAST tool call — the truncated残尾 (partial text the cut left behind) — are
// dropped. Everything before the tool calls (thinking, the pre-call text) is
// preserved so history and the TUI stay faithful.
//
// A message without tool calls, or with any incomplete tool call, is returned
// unchanged (false): a plain truncated answer and a cut-off JSON argument both
// stay "length" so the loop's existing length handling applies.
func SalvageLengthTruncation(msg *agentcore.AssistantMessage) bool {
	if msg == nil || msg.StopReason != agentcore.StopReasonLength {
		return false
	}
	lastTool := -1
	for i, c := range msg.Content {
		if tc, ok := c.(agentcore.ToolCallContent); ok {
			if tc.Name == "" {
				return false // a call the executor could not dispatch
			}
			if len(tc.Arguments) != 0 && !json.Valid(tc.Arguments) {
				return false // cut mid-JSON: nothing safe to execute
			}
			lastTool = i
		}
	}
	if lastTool < 0 {
		return false // plain truncated text: nothing to salvage
	}
	// Drop the残尾: whatever the truncation left after the last complete tool
	// call cannot be executed and must not linger as a dangling partial turn.
	if lastTool < len(msg.Content)-1 {
		msg.Content = msg.Content[:lastTool+1]
	}
	msg.StopReason = agentcore.StopReasonToolUse
	return true
}
