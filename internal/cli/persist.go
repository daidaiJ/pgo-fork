// This file holds PersistTurn, the session-tree persistence step shared by the
// REPL loop and the /goal autonomous loop. It reads and advances a session's
// mutable cursor state through the Host contract, so a command need not import
// the concrete replDeps aggregate to persist the tail of a turn.
package cli

import (
	"fmt"
	"io"
	"time"
)

// PersistTurn appends the messages produced since the last persist as a new
// branch descending from the host's current leaf, advancing the leaf and the
// persisted-message cursor. Growing the tree with AppendBranch (rather than a
// full linear Save) is what lets a later /tree leaf-switch fork the on-disk
// history instead of clobbering it. If nothing new was produced it is a no-op.
//
// T3.3 marker-entry model: compaction no longer rewrites the context — the
// loop inserts a marker into the live list (always after the persisted cursor,
// by topology), so the tail append below carries the marker into the tree and
// the tree grows append-only. The old "context shrank below the cursor →
// re-save flattened" branch is gone: it was the defect that flattened the v3
// tree and physically destroyed abandoned branches after every auto-compaction.
func PersistTurn(out io.Writer, h Host) {
	agentCtx := h.AgentCtx()
	tail := agentCtx.Messages[h.Persisted():]
	if len(tail) == 0 {
		return
	}
	header := h.Header()
	header.UpdatedAt = time.Now().UTC()
	leaf, err := h.Store().AppendBranch(header, h.CurLeaf(), tail)
	if err != nil {
		fmt.Fprintf(out, "pigo: session save failed: %v\n", err)
		return
	}
	h.SetCurLeaf(leaf)
	h.SetPersisted(len(agentCtx.Messages))
}
