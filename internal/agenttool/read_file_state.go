// This file bridges the readFileState ledger (T3.5, agentcore) into the tool
// layer: the context accessor the file tools share, and the edit guard that
// turns ledger state into model-facing rejections. The ledger itself lives on
// the AgentContext the running loop injects; tools invoked outside a loop get
// nil here and skip the guard entirely (same defensive shape as Snap).
package agenttool

import (
	"context"
	"os"

	"github.com/smallnest/pigo/internal/agentcore"
)

// ReadFileStateFromContext returns the readFileState ledger carried by the
// loop-injected AgentContext, or nil when there is none (standalone tool use).
func ReadFileStateFromContext(ctx context.Context) *agentcore.ReadFileState {
	agentCtx := agentcore.AgentContextFromContext(ctx)
	if agentCtx == nil {
		return nil
	}
	return agentCtx.ReadFiles
}

// guardEditAgainstStaleKnowledge is the edit tool's freshness gate (T3.5): it
// refuses an edit the model cannot vouch for. Three rejection states, all soft
// error results the model recovers from by reading the file:
//
//   - not read this session  → "read it first" (also the post-restart state:
//     the ledger is in-process, so every file starts unknown);
//   - residency revoked      → its read result was evicted (microcompaction)
//     or withdrawn, so the in-context copy cannot be trusted;
//   - fingerprint mismatch   → the file changed on disk since the model saw it.
//
// A stat failure (usually "file does not exist") is left to the edit's own read
// step to report. A nil ledger disables the guard.
func guardEditAgainstStaleKnowledge(st *agentcore.ReadFileState, resolvedPath string) (string, bool) {
	if st == nil {
		return "", true
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", true
	}
	if !st.Known(resolvedPath) {
		return "edit: file has not been read yet; read it first so the edit works from current content", false
	}
	if !st.Residency(resolvedPath) {
		return "edit: the earlier read result for this file is no longer in context (evicted to reduce context); re-read the file before editing", false
	}
	if st.FingerprintMismatch(resolvedPath, info.ModTime(), info.Size()) {
		return "edit: file has been modified since it was read; re-read it before editing", false
	}
	return "", true
}

// proveResidencyAfterMutation records the post-write/post-edit state as the new
// baseline: the fingerprint is refreshed and residency is proven (the model saw
// the full text or the diff). A nil ledger is a no-op.
func proveResidencyAfterMutation(st *agentcore.ReadFileState, resolvedPath string) {
	if st == nil {
		return
	}
	if info, err := os.Stat(resolvedPath); err == nil {
		st.RecordMutation(resolvedPath, info.ModTime(), info.Size())
	}
}
