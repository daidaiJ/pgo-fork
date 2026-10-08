// This file holds RunManualCompact, the manual /compact core shared by the
// REPL's executor hook and the TUI's async compactCmd (T7.7 slice 1: one
// implementation, two projections — the front-ends own only the indicator and
// the persist step). It runs the summarization stream on the request view and
// inserts a compaction marker into the live list — the T3.3 marker-entry
// model — then reports the before/after token counts. A failure is reported in
// the message and leaves the context unchanged (US-004).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
)

// RunManualCompact compacts msgs in place on an explicit /compact request: it
// summarizes the request view against the live provider/model, chains from an
// existing leading marker so successive calls never drop the previous summary
// (defect-① fix), and inserts the new marker after the persisted cursor so the
// next tail-append persist carries it into the tree. persisted is the caller's
// persisted-message cursor. The returned string is the transcript feedback.
func RunManualCompact(msgs *agentcore.MessageList, persisted int, live *LiveConfig, creds *provider.CredentialStore) string {
	p := persisted
	if p > len(*msgs) {
		p = len(*msgs)
	}
	// The view→raw map (not the marker-anchor formula) converts the cut back:
	// microcompact markers and context edits also drop entries from the view.
	view, rawOf := compaction.ProjectViewMapped(*msgs)
	settings := compaction.DefaultCompactionSettings
	before := compaction.EstimateContextTokens(view).Tokens

	stream := provider.StreamFnFromProvider(live.Provider)
	model := provider.Model{Provider: live.ProviderName, ID: live.Model, ContextWindow: live.ContextWindow}

	// Resolve the API key like a normal turn so summarization authenticates
	// against auth-requiring providers (otherwise Compact fails with
	// "missing API key" and /compact would always report a non-fatal failure).
	scfg := provider.StreamConfig{}
	if creds != nil {
		scfg.APIKey = creds.GetAPIKey(context.Background(), live.ProviderName)
	}
	// Iterative chain: the view's leading marker (if any) is the previous
	// compaction — summarize only what came after it.
	prevIdx := -1
	var prevSummary string
	var prevDetails *compaction.CompactionDetails
	if len(view) > 0 {
		if c, ok := view[0].(agentcore.CompactionMessage); ok {
			prevIdx = 0
			prevSummary = c.Summary
			var d compaction.CompactionDetails
			if err := json.Unmarshal(c.Details, &d); err == nil && (len(d.ReadFiles) > 0 || len(d.ModifiedFiles) > 0) {
				prevDetails = &d
			}
		}
	}
	res, err := compaction.Compact(context.Background(), stream, model, view, settings, prevIdx, prevDetails, prevSummary, scfg)
	if err != nil {
		return fmt.Sprintf("compaction failed: %v (context left unchanged)", err)
	}
	if res == nil {
		return fmt.Sprintf("nothing to compact (%d tokens, %d messages)", before, len(view))
	}
	// Map the view cut back to raw-list coordinates and insert the marker at the
	// T3.3 topology position (after the persisted cursor so the next PersistTurn
	// carries it into the tree).
	fullCut := compaction.ViewRawOf(rawOf, res.FirstKeptIndex)
	insertAt := fullCut
	if insertAt < p {
		insertAt = p
	}
	marker := res.Message(time.Now().UnixMilli())
	marker.FirstKeptIndex = fullCut
	marker.KeptBefore = insertAt - fullCut
	newList := make(agentcore.MessageList, 0, len(*msgs)+1)
	newList = append(newList, (*msgs)[:insertAt]...)
	newList = append(newList, marker)
	newList = append(newList, (*msgs)[insertAt:]...)
	marker.TokensAfter = compaction.EstimateContextTokens(compaction.ProjectView(newList)).Tokens
	newList[insertAt] = marker
	*msgs = newList
	after := compaction.EstimateContextTokens(compaction.ProjectView(newList)).Tokens
	summarized := res.FirstKeptIndex - 1
	if summarized < 0 {
		summarized = 0
	}
	return fmt.Sprintf("compacted: %d → %d tokens, summarized %d messages, kept %d",
		before, after, summarized, len(view)-res.FirstKeptIndex)
}
