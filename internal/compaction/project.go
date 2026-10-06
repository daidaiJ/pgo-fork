// ProjectView derives the request-shaped view of a context: the projection
// layer of the T3.3 marker-entry model. The persisted history (and the live
// in-memory list) stays append-only and lossless; everything lossy happens
// here, on the copy handed to the provider (grok's "durable layer never lossy,
// loss only in derived views" invariant).
//
// Three projections compose, in order:
//
//  1. Full-compaction markers: the LAST CompactionMessage stands in for the
//     history before its cut; kept entries recorded before it on the persisted
//     path (marker.KeptBefore) are restored ahead of it, because the marker is
//     persisted at the branch tip and therefore sits after kept entries that
//     were already on disk when compaction ran.
//  2. Microcompact markers (S2): tool results whose call ids any
//     MicrocompactMessage cleared are replaced in place by a one-line
//     placeholder; the markers themselves never enter the view.
//  3. Half-pair repair (grok ④): a trailing assistant message with tool calls
//     lacking results gets a synthetic error result per unanswered call, so a
//     mid-run abort never produces a provider 400 for unpaired tool_use.
package compaction

import (
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
)

// halfPairRepairText is the synthetic result content filled in for tool calls
// that never received a result (run interrupted mid-flight). It mirrors grok's
// repair_dangling_tool_calls synthesis; the wording tells the model the call
// can be retried.
const halfPairRepairText = "[Tool result missing: the run was interrupted before this call completed. " +
	"You may retry the call if it is still needed.]"

// LastMarkerAnchor returns the position of the LAST compaction marker in msgs
// and its KeptBefore (m0 = -1 when none exists). It is the mapping anchor
// callers need to convert a view-coordinate cut back into raw-list coordinates
// via ViewIndexToRaw.
func LastMarkerAnchor(msgs agentcore.MessageList) (m0, k0 int) {
	m0, k0 = -1, 0
	for i, m := range msgs {
		if c, ok := m.(agentcore.CompactionMessage); ok {
			m0, k0 = i, c.KeptBefore
		}
	}
	return m0, k0
}

// ViewIndexToRaw converts a view-coordinate index into raw-list coordinates.
// The view is the projection anchored at the raw list's last compaction marker
// (position m0, KeptBefore k0): view[0] = marker, view[1..k0] =
// raw[m0-k0..m0-1], view[k0+1..] = raw[m0+1..]. A no-marker view (m0 < 0) is
// the identity. The result is clamped into range against a corrupt marker.
func ViewIndexToRaw(c, m0, k0 int) int {
	if m0 < 0 {
		return c
	}
	if c <= k0 {
		if i := m0 - k0 + c - 1; i >= 0 {
			return i
		}
		return 0
	}
	return m0 + c - k0
}

// ProjectView returns the request view of msgs. It never mutates msgs and
// returns msgs itself unchanged when no marker and no dangling tool call is
// present (the overwhelmingly common case, at zero allocation).
func ProjectView(msgs agentcore.MessageList) agentcore.MessageList {
	view := projectCompactionMarker(msgs)
	view = projectMicrocompact(view)
	view = repairDanglingToolCalls(view)
	return view
}

// projectCompactionMarker applies the last full-compaction marker: view =
// [marker] + kept-before wrap + everything after it. Earlier markers are
// dropped from the view: their content is inherited by the last marker's
// summary chain (each compaction folds the previous summary in via
// previousSummary), so rendering them would only duplicate tokens.
func projectCompactionMarker(msgs agentcore.MessageList) agentcore.MessageList {
	last := -1
	var marker agentcore.CompactionMessage
	for i, m := range msgs {
		if c, ok := m.(agentcore.CompactionMessage); ok {
			last, marker = i, c
		}
	}
	if last < 0 {
		return msgs
	}
	k := marker.KeptBefore
	if k > last {
		k = last // defensive: a corrupt marker must not index out of range
	}
	if k < 0 {
		k = 0
	}
	out := make(agentcore.MessageList, 0, len(msgs)-last+k)
	out = append(out, marker)
	for _, m := range msgs[last-k : last] {
		if _, isMarker := m.(agentcore.CompactionMessage); isMarker {
			continue // superseded marker inside the kept wrap: chain inherited it
		}
		out = append(out, m)
	}
	for _, m := range msgs[last+1:] {
		if _, isMarker := m.(agentcore.CompactionMessage); isMarker {
			continue
		}
		out = append(out, m)
	}
	return out
}

// projectMicrocompact replaces tool results cleared by any MicrocompactMessage
// with a one-line placeholder and drops the markers from the view. Cleared
// results stay verbatim in the persisted history; only this view loses them.
func projectMicrocompact(msgs agentcore.MessageList) agentcore.MessageList {
	cleared := make(map[string]bool)
	for _, m := range msgs {
		if mc, ok := m.(agentcore.MicrocompactMessage); ok {
			for _, id := range mc.ClearedCallIDs {
				cleared[id] = true
			}
		}
	}
	if len(cleared) == 0 {
		return msgs
	}
	out := make(agentcore.MessageList, 0, len(msgs))
	for _, m := range msgs {
		if _, ok := m.(agentcore.MicrocompactMessage); ok {
			continue // projection metadata, never a request message
		}
		if tr, ok := m.(agentcore.ToolResultMessage); ok && cleared[tr.ToolCallID] {
			tr.Content = agentcore.ContentList{agentcore.NewTextContent(
				fmt.Sprintf("[Old %s result cleared to reduce context; re-run the tool if you need its content]", tr.ToolName))}
			tr.IsError = false
			out = append(out, tr)
			continue
		}
		out = append(out, m)
	}
	return out
}

// repairDanglingToolCalls scans the whole view for assistant messages whose
// tool calls lack a following ToolResultMessage and synthesizes an error
// result per unanswered call, so the request is always a well-formed
// tool_use/tool_result pairing. Strict providers (DeepSeek and kin) reject a
// request containing ANY dangling tool_use, not just at the tail, so historical
// gaps — an interrupted run's partial message kept in history, a rewind into a
// half-finished turn — must be repaired too, not only the tail. Synthetics are
// inserted at the end of the assistant's result run (just before the next
// non-tool-result message, or the end of the view), never ahead of a real
// result that already follows. Idempotent by construction: a repaired view has
// no dangling calls left.
func repairDanglingToolCalls(msgs agentcore.MessageList) agentcore.MessageList {
	answered := make(map[string]bool)
	for _, m := range msgs {
		if tr, ok := m.(agentcore.ToolResultMessage); ok {
			answered[tr.ToolCallID] = true
		}
	}
	var pending []agentcore.ToolCallContent
	dangling := 0
	for _, m := range msgs {
		if a, ok := m.(agentcore.AssistantMessage); ok {
			for _, c := range a.ToolCalls() {
				if !answered[c.ID] {
					dangling++
				}
			}
		}
	}
	if dangling == 0 {
		return msgs
	}
	out := make(agentcore.MessageList, 0, len(msgs)+dangling)
	flush := func() {
		for _, c := range pending {
			out = append(out, agentcore.ToolResultMessage{
				RoleField:  agentcore.RoleToolResult,
				ToolCallID: c.ID,
				ToolName:   c.Name,
				Content:    agentcore.ContentList{agentcore.NewTextContent(halfPairRepairText)},
				IsError:    true,
			})
		}
		pending = nil
	}
	for _, m := range msgs {
		if _, isResult := m.(agentcore.ToolResultMessage); !isResult {
			flush() // end of the assistant's result run: emit synthetics first
		}
		out = append(out, m)
		if a, ok := m.(agentcore.AssistantMessage); ok {
			for _, c := range a.ToolCalls() {
				if !answered[c.ID] {
					pending = append(pending, c)
				}
			}
		}
	}
	flush() // trailing dangling calls
	return out
}
