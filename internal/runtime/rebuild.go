// Context rebuild for the "infinite context" feature (#482). Where auto-
// compaction (loop.go) inserts a compaction marker when the context outgrows
// its window, a rebuild reconstructs the working context deterministically from
// a persisted checkpoint: everything before the checkpoint watermark collapses
// into the marker's summary, and everything at/after the watermark is kept
// verbatim. This is what the /rebuild command (REPL + TUI) invokes.
//
// T3.3 marker-entry model: like auto-compaction, rebuild no longer rewrites
// the context — it inserts a CompactionMessage marker into the live list at
// the topology position (max(cut, persisted cursor)) and lets the request-view
// projection (compaction.ProjectView) collapse the prefix. The caller's list
// grows by exactly one message; persistence stays a plain tail append.
//
// When no checkpoint exists yet there is nothing to reload, so a rebuild falls
// back to the ordinary compaction path (the same runCompaction flow
// maybeAutoCompact drives) so /rebuild still shrinks an overgrown context.
//
// This file is deliberately side-effect free with respect to the loop: it never
// mutates the caller's AgentContext. It returns the rebuilt MessageList (plus a
// RebuildResult describing what happened and an equivalent CompactionEvent) so
// the CLI handlers decide when and how to apply it.
package runtime

import (
	"context"
	"encoding/json"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
)

// unmarshalDetails decodes a compaction marker's opaque Details JSON into
// CompactionDetails (shared by the loop's iterative chain and the rebuild path).
func unmarshalDetails(raw json.RawMessage) (*compaction.CompactionDetails, error) {
	var d compaction.CompactionDetails
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// RebuildResult describes the outcome of a context rebuild. Messages is the
// rebuilt list ready to replace the live context; the remaining fields mirror
// CompactionEvent so a front-end can report the same before/after summary as a
// compaction.
type RebuildResult struct {
	// Messages is the rebuilt context: the original list with one compaction
	// marker inserted at the cut (T3.3 marker-entry model — nothing is dropped;
	// the request view collapses the prefix). When NoOp is true it is the
	// original list, unchanged.
	Messages agentcore.MessageList
	// FromCheckpoint is true when the boundary came from a persisted checkpoint;
	// false when the no-checkpoint fallback ran a summarizing compaction.
	FromCheckpoint bool
	// Watermark is the raw-list position the marker was inserted at.
	Watermark int
	// SummarizedCount is how many view messages were collapsed into the summary.
	SummarizedCount int
	// KeptCount is how many view messages were preserved verbatim.
	KeptCount int
	// TokensBefore / TokensAfter are the estimated context tokens of the view
	// before and after the rebuild (equal when NoOp).
	TokensBefore int
	TokensAfter  int
	// NoOp is true when nothing changed: no checkpoint existed and there was
	// nothing to compact (an empty summarization range).
	NoOp bool
}

// Event renders the rebuild as a CompactionEvent so consumers that already
// handle compaction reporting (the REPL/TUI event surfaces) can present a
// rebuild with the same shape. Reason is "rebuild".
func (r *RebuildResult) Event() agentcore.CompactionEvent {
	return agentcore.CompactionEvent{
		Reason:          "rebuild",
		TokensBefore:    r.TokensBefore,
		TokensAfter:     r.TokensAfter,
		SummarizedCount: r.SummarizedCount,
		KeptCount:       r.KeptCount,
	}
}

// RebuildFromCheckpoint reconstructs the working context for sessionID.
//
// If a checkpoint exists under memoryRoot, the compaction marker is inserted at
// the checkpoint watermark: messages before the watermark collapse to the
// checkpoint summary (in the request view), and messages at/after the watermark
// are preserved verbatim. The watermark is clamped to [0, len(view)] so a stale
// checkpoint recorded against a longer history (or one that has since been
// re-compacted) never slices out of range.
//
// If no checkpoint exists, it falls back to the summarizing compaction path —
// the same runCompaction flow the loop's auto-compaction uses — so /rebuild
// still shrinks the context. When there is nothing to compact the original list
// is returned with NoOp set.
//
// It performs no mutation of the caller's context and no checkpoint writes; the
// returned RebuildResult carries the rebuilt list for the caller to apply. When
// waitForCheckpoint is non-nil it is invoked before reading, so a caller that
// has an in-flight checkpoint write can block until it lands (kept as a callback
// so this stays decoupled from the loop's write path).
func RebuildFromCheckpoint(
	ctx context.Context,
	msgs agentcore.MessageList,
	sessionID, memoryRoot string,
	cfg *RunConfig,
	waitForCheckpoint func(),
) (*RebuildResult, error) {
	if waitForCheckpoint != nil {
		waitForCheckpoint()
	}
	now := nowMillis()
	persisted := 0
	if cfg != nil && cfg.PersistedCount != nil {
		persisted = cfg.PersistedCount()
	}
	if persisted > len(msgs) {
		persisted = len(msgs)
	}
	// Decisions run on the request view (T3.3): the raw list may hold superseded
	// markers and pre-compaction history the view collapses. The view→raw map
	// (not the marker-anchor formula) converts cuts/watermarks back: microcompact
	// markers and context edits also drop entries from the view.
	view, rawOf := compaction.ProjectViewMapped(msgs)
	tokensBefore := compaction.EstimateContextTokens(view).Tokens

	cp, ok, err := LoadCheckpoint(sessionID, memoryRoot)
	if err != nil {
		return nil, err
	}
	if ok {
		return rebuildFromLoadedCheckpoint(msgs, rawOf, persisted, cp, tokensBefore, now), nil
	}

	// No checkpoint: fall back to the summarizing compaction path.
	prevIdx := -1
	var prevSummary string
	var prevDetails *compaction.CompactionDetails
	if len(view) > 0 {
		if c, isMarker := view[0].(agentcore.CompactionMessage); isMarker {
			prevIdx = 0
			prevSummary = c.Summary
			if d, err := unmarshalDetails(c.Details); err == nil {
				prevDetails = d
			}
		}
	}
	res, err := runCompaction(ctx, view, cfg, prevIdx, prevSummary, prevDetails)
	if err != nil {
		return nil, err
	}
	if res == nil {
		// Nothing to summarize (no valid cut point / empty range): leave as-is.
		return &RebuildResult{
			Messages:     msgs,
			TokensBefore: tokensBefore,
			TokensAfter:  tokensBefore,
			KeptCount:    len(view),
			NoOp:         true,
		}, nil
	}
	cut := res.FirstKeptIndex
	fullCut := compaction.ViewRawOf(rawOf, cut)
	newList, marker, insertAt := insertCompactionMarker(msgs, res, fullCut, persisted)
	tokensAfter := compaction.EstimateContextTokens(compaction.ProjectView(newList)).Tokens
	marker.TokensAfter = tokensAfter
	newList[insertAt] = marker
	return &RebuildResult{
		Messages:        newList,
		FromCheckpoint:  false,
		Watermark:       insertAt,
		SummarizedCount: max(0, cut-1),
		KeptCount:       len(view) - cut,
		TokensBefore:    tokensBefore,
		TokensAfter:     tokensAfter,
	}, nil
}

// rebuildFromLoadedCheckpoint builds the rebuilt context from a loaded
// checkpoint: the pre-watermark prefix collapses into a compaction marker
// carrying cp.Summary (inserted at the T3.3 topology position), and the tail
// from the (clamped) watermark on is preserved verbatim in the request view.
// The watermark is a view coordinate (the checkpoint's cut was recorded against
// the view, T3.3 D-2) and converts back through the view→raw map.
func rebuildFromLoadedCheckpoint(msgs agentcore.MessageList, rawOf []int, persisted int, cp *Checkpoint, tokensBefore int, now int64) *RebuildResult {
	// A nil map is the identity: the view length is the raw length.
	viewLen := len(rawOf)
	if rawOf == nil {
		viewLen = len(msgs)
	}
	w := cp.Watermark
	if w < 0 {
		w = 0
	}
	if w > viewLen {
		w = viewLen
	}
	res := &compaction.CompactionResult{
		Summary:        cp.Summary,
		FirstKeptIndex: w,
		TokensBefore:   tokensBefore,
	}
	fullCut := compaction.ViewRawOf(rawOf, w)
	newList, marker, insertAt := insertCompactionMarker(msgs, res, fullCut, persisted)
	tokensAfter := compaction.EstimateContextTokens(compaction.ProjectView(newList)).Tokens
	marker.TokensAfter = tokensAfter
	newList[insertAt] = marker
	return &RebuildResult{
		Messages:        newList,
		FromCheckpoint:  true,
		Watermark:       insertAt,
		SummarizedCount: w,
		KeptCount:       viewLen - w,
		TokensBefore:    tokensBefore,
		TokensAfter:     tokensAfter,
	}
}
