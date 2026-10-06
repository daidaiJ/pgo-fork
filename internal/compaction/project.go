// ProjectView derives the request-shaped view of a context: the projection
// layer of the T3.3 marker-entry model. The persisted history (and the live
// in-memory list) stays append-only and lossless; everything lossy happens
// here, on the copy handed to the provider (grok's "durable layer never lossy,
// loss only in derived views" invariant).
//
// Four projections compose, in order:
//
//  1. Full-compaction markers: the LAST CompactionMessage stands in for the
//     history before its cut; kept entries recorded before it on the persisted
//     path (marker.KeptBefore) are restored ahead of it, because the marker is
//     persisted at the branch tip and therefore sits after kept entries that
//     were already on disk when compaction ran.
//  2. Microcompact markers (S2): tool results whose call ids any
//     MicrocompactMessage cleared are replaced in place by a one-line
//     placeholder; the markers themselves never enter the view.
//
//  3. Context edits (T3.4): explicit model-requested visibility edits carried
//     by ContextEditMessage tree entries — replace targets show the model's
//     digest, hidden targets drop from the view; the edit records themselves
//     never enter the view. They apply after microcompact (spec §7.2: the
//     automatic regenerable-output pass first, the semantic layer above it).
//
//  3b. Tool claims (T4.1): search_tools claim records (ToolClaimMessage tree
//     entries) drop from the view — the claim rebuilds the declared face at the
//     tools parameter, it is not itself a request message.
//
//  4. Half-pair repair (grok ④): a trailing assistant message with tool calls
//     lacking results gets a synthetic error result per unanswered call, so a
//     mid-run abort never produces a provider 400 for unpaired tool_use.
//
// Every stage that drops or appends entries also maintains the parallel
// raw-index map (ProjectViewMapped): stages 2 and 3 drop projection-metadata
// markers from the view, so a view coordinate cannot be mapped back to the raw
// list by arithmetic alone once any of them has fired. Callers that convert a
// view cut (compaction FirstKeptIndex, checkpoint watermark) back into raw
// coordinates MUST go through the map, not the marker-anchor formula — the
// formula is only exact for the pure marker topology.
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
// and its KeptBefore (m0 = -1 when none exists). It remains the description of
// the pure marker topology (and the anchor ViewIndexToRaw maps against); for
// general view→raw conversion use ProjectViewMapped, whose map stays exact
// when microcompact markers or context edits have also dropped entries.
func LastMarkerAnchor(msgs agentcore.MessageList) (m0, k0 int) {
	m0, k0 = -1, 0
	for i, m := range msgs {
		if c, ok := m.(agentcore.CompactionMessage); ok {
			m0, k0 = i, c.KeptBefore
		}
	}
	return m0, k0
}

// ViewIndexToRaw converts a view-coordinate index into raw-list coordinates
// under the PURE marker topology: the view is the projection anchored at the
// raw list's last compaction marker (position m0, KeptBefore k0): view[0] =
// marker, view[1..k0] = raw[m0-k0..m0-1], view[k0+1..] = raw[m0+1..]. A
// no-marker view (m0 < 0) is the identity. The result is clamped into range
// against a corrupt marker. When microcompact markers or context edits are in
// play the formula drifts — use ProjectViewMapped instead.
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
// returns msgs itself unchanged when no marker, no context edit, and no
// dangling tool call is present (the overwhelmingly common case, at zero
// allocation).
func ProjectView(msgs agentcore.MessageList) agentcore.MessageList {
	view, _ := ProjectViewMapped(msgs)
	return view
}

// ProjectViewMapped returns the request view of msgs together with the
// raw-index map: rawOf[i] is the index of view[i] in the ORIGINAL msgs (-1 for
// the half-pair repair synthetics, which have no raw origin). nil rawOf means
// the identity (the view is msgs unchanged). Never mutates msgs.
func ProjectViewMapped(msgs agentcore.MessageList) (agentcore.MessageList, []int) {
	view, rawOf := projectCompactionMarkerMapped(msgs)
	view, rawOf = projectMicrocompactMapped(view, rawOf)
	view, rawOf = projectToolClaimsMapped(view, rawOf)
	view, rawOf = projectContextEditsMapped(view, rawOf)
	return repairDanglingToolCallsMapped(view, rawOf)
}

// projectToolClaimsMapped drops ToolClaimMessage entries from the view (T4.1):
// a search_tools claim record is history (it persists and replays) but never a
// request message — the loop rebuilds the declared face from the declaration
// state instead. Dropping here keeps the claim record from ever reaching a
// provider, exactly like the microcompact/context-edit metadata markers.
func projectToolClaimsMapped(msgs agentcore.MessageList, rawOf []int) (agentcore.MessageList, []int) {
	has := false
	for _, m := range msgs {
		if _, ok := m.(agentcore.ToolClaimMessage); ok {
			has = true
			break
		}
	}
	if !has {
		return msgs, rawOf
	}
	out := make(agentcore.MessageList, 0, len(msgs))
	idx := concreteIdx(rawOf, len(msgs))
	outIdx := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if _, ok := m.(agentcore.ToolClaimMessage); ok {
			continue // projection metadata, never a request message
		}
		out = append(out, m)
		outIdx = append(outIdx, idx[i])
	}
	return out, outIdx
}

// identityIdx reports whether rawOf is the nil-encoded identity map.
func identityIdx(rawOf []int) bool { return rawOf == nil }

// concreteIdx materializes a nil identity map into a real slice; stages that
// filter or reorder call this before mutating.
func concreteIdx(rawOf []int, n int) []int {
	if rawOf != nil {
		return rawOf
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// projectCompactionMarkerMapped applies the last full-compaction marker: view =
// [marker] + kept-before wrap + everything after it. Earlier markers are
// dropped from the view: their content is inherited by the last marker's
// summary chain (each compaction folds the previous summary in via
// previousSummary), so rendering them would only duplicate tokens.
func projectCompactionMarkerMapped(msgs agentcore.MessageList) (agentcore.MessageList, []int) {
	last := -1
	var marker agentcore.CompactionMessage
	for i, m := range msgs {
		if c, ok := m.(agentcore.CompactionMessage); ok {
			last, marker = i, c
		}
	}
	if last < 0 {
		return msgs, nil
	}
	k := marker.KeptBefore
	if k > last {
		k = last // defensive: a corrupt marker must not index out of range
	}
	if k < 0 {
		k = 0
	}
	out := make(agentcore.MessageList, 0, len(msgs)-last+k)
	rawOf := make([]int, 0, len(msgs)-last+k)
	out = append(out, marker)
	rawOf = append(rawOf, last)
	for i := last - k; i < last; i++ {
		if _, isMarker := msgs[i].(agentcore.CompactionMessage); isMarker {
			continue // superseded marker inside the kept wrap: chain inherited it
		}
		out = append(out, msgs[i])
		rawOf = append(rawOf, i)
	}
	for i := last + 1; i < len(msgs); i++ {
		if _, isMarker := msgs[i].(agentcore.CompactionMessage); isMarker {
			continue
		}
		out = append(out, msgs[i])
		rawOf = append(rawOf, i)
	}
	return out, rawOf
}

// projectMicrocompactMapped replaces tool results cleared by any
// MicrocompactMessage with a one-line placeholder and drops the markers from
// the view. Cleared results stay verbatim in the persisted history; only this
// view loses them.
func projectMicrocompactMapped(msgs agentcore.MessageList, rawOf []int) (agentcore.MessageList, []int) {
	cleared := make(map[string]bool)
	for _, m := range msgs {
		if mc, ok := m.(agentcore.MicrocompactMessage); ok {
			for _, id := range mc.ClearedCallIDs {
				cleared[id] = true
			}
		}
	}
	if len(cleared) == 0 {
		return msgs, rawOf
	}
	out := make(agentcore.MessageList, 0, len(msgs))
	idx := concreteIdx(rawOf, len(msgs))
	outIdx := make([]int, 0, len(msgs))
	for i, m := range msgs {
		if _, ok := m.(agentcore.MicrocompactMessage); ok {
			continue // projection metadata, never a request message
		}
		if tr, ok := m.(agentcore.ToolResultMessage); ok && cleared[tr.ToolCallID] {
			tr.Content = agentcore.ContentList{agentcore.NewTextContent(
				fmt.Sprintf("[Old %s result cleared to reduce context; re-run the tool if you need its content]", tr.ToolName))}
			tr.IsError = false
			out = append(out, tr)
			outIdx = append(outIdx, idx[i])
			continue
		}
		out = append(out, m)
		outIdx = append(outIdx, idx[i])
	}
	return out, outIdx
}

// repairDanglingToolCallsMapped scans the whole view for assistant messages
// whose tool calls lack a following ToolResultMessage and synthesizes an error
// result per unanswered call, so the request is always a well-formed
// tool_use/tool_result pairing. Strict providers (DeepSeek and kin) reject a
// request containing ANY dangling tool_use, not just at the tail, so historical
// gaps — an interrupted run's partial message kept in history, a rewind into a
// half-finished turn — must be repaired too, not only the tail. Synthetics are
// inserted at the end of the assistant's result run (just before the next
// non-tool-result message, or the end of the view), never ahead of a real
// result that already follows, and map to raw index -1. Idempotent by
// construction: a repaired view has no dangling calls left.
func repairDanglingToolCallsMapped(msgs agentcore.MessageList, rawOf []int) (agentcore.MessageList, []int) {
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
		return msgs, rawOf
	}
	out := make(agentcore.MessageList, 0, len(msgs)+dangling)
	idx := concreteIdx(rawOf, len(msgs))
	outIdx := make([]int, 0, len(msgs)+dangling)
	flush := func() {
		for _, c := range pending {
			out = append(out, agentcore.ToolResultMessage{
				RoleField:  agentcore.RoleToolResult,
				ToolCallID: c.ID,
				ToolName:   c.Name,
				Content:    agentcore.ContentList{agentcore.NewTextContent(halfPairRepairText)},
				IsError:    true,
			})
			outIdx = append(outIdx, -1)
		}
		pending = nil
	}
	for i, m := range msgs {
		if _, isResult := m.(agentcore.ToolResultMessage); !isResult {
			flush() // end of the assistant's result run: emit synthetics first
		}
		out = append(out, m)
		outIdx = append(outIdx, idx[i])
		if a, ok := m.(agentcore.AssistantMessage); ok {
			for _, c := range a.ToolCalls() {
				if !answered[c.ID] {
					pending = append(pending, c)
				}
			}
		}
	}
	flush() // trailing dangling calls
	return out, outIdx
}

// ViewRawOf converts a view-coordinate index into raw-list coordinates via a
// ProjectViewMapped map. Index -1 entries (repair synthetics) resolve to the
// nearest following real entry (the synthetics sit just before it), then the
// nearest preceding one, so a cut landing among synthetics still yields a
// usable raw anchor; out-of-range indices clamp to the list bounds.
func ViewRawOf(rawOf []int, c int) int {
	if identityIdx(rawOf) {
		return c
	}
	if c < 0 {
		return 0
	}
	if c >= len(rawOf) {
		return rawOf[len(rawOf)-1]
	}
	if rawOf[c] >= 0 {
		return rawOf[c]
	}
	for i := c + 1; i < len(rawOf); i++ {
		if rawOf[i] >= 0 {
			return rawOf[i]
		}
	}
	for i := c - 1; i >= 0; i-- {
		if rawOf[i] >= 0 {
			return rawOf[i]
		}
	}
	return 0
}
