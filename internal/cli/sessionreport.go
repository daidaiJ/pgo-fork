// This file holds WriteSessionSummary, the /session renderer shared by the
// REPL and the TUI (T7.7 slice 1: the replDeps closure and the TUI's
// renderSession were byte-identical duplicates — one renderer now).
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/session"
)

// WriteSessionSummary renders the /session summary (US-009, #125): session id,
// message count, estimated token usage, model/provider, creation time, and
// compaction-checkpoint count. Counts derive from the in-memory context (the
// source of truth for the live turn), so unsaved messages are counted too.
func WriteSessionSummary(out io.Writer, header session.SessionHeader, live *LiveConfig, msgs agentcore.MessageList) {
	tokens := compaction.EstimateContextTokens(msgs).Tokens
	compactions := 0
	for _, m := range msgs {
		if _, ok := m.(agentcore.CompactionMessage); ok {
			compactions++
		}
	}
	fmt.Fprintf(out, "session:      %s\n", header.ID)
	fmt.Fprintf(out, "messages:     %d\n", len(msgs))
	fmt.Fprintf(out, "tokens (est): %d\n", tokens)
	model := live.Model
	providerName := live.ProviderName
	if model == "" {
		model = header.Model
	}
	if providerName == "" {
		providerName = header.Provider
	}
	fmt.Fprintf(out, "model:        %s (provider: %s)\n", model, providerName)
	if !header.CreatedAt.IsZero() {
		fmt.Fprintf(out, "created:      %s\n", header.CreatedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "compactions:  %d\n", compactions)
}
