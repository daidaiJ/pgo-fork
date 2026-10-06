// Canonical context edit projection (T3.4, wiki/port/canonical-context-edit.md):
// explicit, model-requested visibility edits over earlier history. The raw
// history is never touched — a ContextEditMessage is a durable tree entry (the
// edit is itself history, so replay re-applies it and forked branches inherit
// it) — and this projection derives the visible face: replace targets show the
// model's digest, hidden targets drop from the view.
//
// Edits apply AFTER the compaction-marker and microcompact projections (spec
// §7.2: microcompact first — it owns regenerable output; context edit is the
// semantic layer above it) and BEFORE the half-pair repair, so a replaced or
// hidden tool result still participates in pairing repair.
//
// Target resolution is coordinate-free on purpose: markers dropped by the
// earlier projections and entries folded by compaction shift positions, so an
// edit is re-located by its anchor — the tool-call id for tool results (exact,
// shift-proof), otherwise the content digest with the recorded seq as a
// proximity hint among equal-content candidates. An edit whose anchor no
// longer resolves (target folded away by a later compaction, rewind onto a
// branch without it) is skipped silently — the raw entry is gone from the view
// either way, which is the grok needle-miss no-op semantic (spec §7.3).
package compaction

import (
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
)

// replacePlaceholder / hidePlaceholder render the visible replacement for an
// edited tool result. Tool results are never dropped outright: removing one
// orphans its tool_use half and strict providers 400 on the pairing. The
// "#seq" is the target's index in the request view at edit time — the handle
// the model itself used.
func replacePlaceholder(e agentcore.ContextEdit) string {
	return fmt.Sprintf("[result #%d summarized: %s]", e.TargetSeq, e.Digest)
}

func hidePlaceholder(e agentcore.ContextEdit) string {
	return fmt.Sprintf("[result #%d hidden from context]", e.TargetSeq)
}

// projectContextEditsMapped applies every ContextEditMessage in msgs (in
// encounter order, first-edit-wins per target) and drops the edit records from
// the view, maintaining the raw-index map for every drop and in-place
// replacement. It returns msgs unchanged when the list carries no edit entries
// — the overwhelmingly common case.
func projectContextEditsMapped(msgs agentcore.MessageList, rawOf []int) (agentcore.MessageList, []int) {
	hasEdits := false
	for _, m := range msgs {
		if _, ok := m.(agentcore.ContextEditMessage); ok {
			hasEdits = true
			break
		}
	}
	if !hasEdits {
		return msgs, rawOf
	}

	// First-edit-wins (spec §2/§7.1): the first edit record for a target wins;
	// later edits for the same target are ignored. Keyed by call id when the
	// edit carries one, else by content digest.
	seen := make(map[string]bool)
	type resolved struct {
		idx  int
		edit agentcore.ContextEdit
	}
	var plan []resolved
	for _, m := range msgs {
		ce, ok := m.(agentcore.ContextEditMessage)
		if !ok {
			continue
		}
		for _, e := range ce.Edits {
			key := e.TargetCallID
			if key == "" {
				key = e.ContentHash
			}
			if key == "" || seen[key] {
				continue
			}
			if idx, ok := locateTarget(msgs, e); ok {
				seen[key] = true
				plan = append(plan, resolved{idx: idx, edit: e})
			}
		}
	}

	inIdx := concreteIdx(rawOf, len(msgs))
	out := make(agentcore.MessageList, 0, len(msgs))
	outIdx := make([]int, 0, len(msgs))
	byIdx := make(map[int]agentcore.ContextEdit, len(plan))
	for _, r := range plan {
		byIdx[r.idx] = r.edit
	}
	for i, m := range msgs {
		if _, ok := m.(agentcore.ContextEditMessage); ok {
			continue // projection metadata, never a request message
		}
		e, edited := byIdx[i]
		if !edited {
			out = append(out, m)
			outIdx = append(outIdx, inIdx[i])
			continue
		}
		switch msg := m.(type) {
		case agentcore.ToolResultMessage:
			// Keep role fields and the pairing anchor; only the visible
			// content swaps. IsError is preserved: an error result that the
			// model replaced still reads as an error.
			if e.Mode == agentcore.ModeHide {
				msg.Content = agentcore.ContentList{agentcore.NewTextContent(hidePlaceholder(e))}
			} else {
				msg.Content = agentcore.ContentList{agentcore.NewTextContent(replacePlaceholder(e))}
			}
			out = append(out, msg)
			outIdx = append(outIdx, inIdx[i])
		case agentcore.UserMessage:
			if e.Mode == agentcore.ModeHide {
				continue // no pairing constraint: drop outright
			}
			msg.Content = agentcore.ContentList{agentcore.NewTextContent(e.Digest)}
			out = append(out, msg)
			outIdx = append(outIdx, inIdx[i])
		case agentcore.AssistantMessage:
			if e.Mode == agentcore.ModeHide {
				if len(msg.ToolCalls()) == 0 {
					continue // drop outright only when no tool_use would orphan
				}
				out = append(out, msg) // defensive: cannot drop a tool_use half
				outIdx = append(outIdx, inIdx[i])
				continue
			}
			// Replace swaps the text but keeps tool-call blocks so the
			// call/result pairing survives.
			kept := make(agentcore.ContentList, 0, len(msg.Content))
			for _, c := range msg.Content {
				if _, isCall := c.(agentcore.ToolCallContent); isCall {
					kept = append(kept, c)
				}
			}
			kept = append(kept, agentcore.NewTextContent(e.Digest))
			msg.Content = kept
			out = append(out, msg)
			outIdx = append(outIdx, inIdx[i])
		default:
			out = append(out, m)
			outIdx = append(outIdx, inIdx[i])
		}
	}
	return out, outIdx
}

// locateTarget resolves an edit's anchor against msgs: a tool-call id scans
// for its ToolResultMessage; a content digest picks the editable message whose
// rendered text matches, preferring the candidate nearest the recorded seq
// (identical texts — a bare "继续" — are legal history, so the hint breaks the
// tie). A call-id anchor skips digest verification on purpose: the id is
// already exact, and the digest would falsely reject a target whose view
// content a later microcompact pass replaced.
func locateTarget(msgs agentcore.MessageList, e agentcore.ContextEdit) (int, bool) {
	if e.TargetCallID != "" {
		for i, m := range msgs {
			if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID == e.TargetCallID {
				return i, true
			}
		}
		return 0, false
	}
	if e.ContentHash == "" {
		return 0, false
	}
	best, bestDist := 0, -1
	for i, m := range msgs {
		switch m.(type) {
		case agentcore.UserMessage, agentcore.AssistantMessage, agentcore.ToolResultMessage:
		default:
			continue
		}
		if agentcore.MessageTextDigest(m) != e.ContentHash {
			continue
		}
		dist := i - e.TargetSeq
		if dist < 0 {
			dist = -dist
		}
		if bestDist < 0 || dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best, bestDist >= 0
}
