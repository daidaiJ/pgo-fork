package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/testenv"
)

// guardedContext returns a context carrying an AgentContext with a fresh
// readFileState ledger — the shape the loop injects (T3.5).
func guardedContext() context.Context {
	return agentcore.WithAgentContext(context.Background(),
		&agentcore.AgentContext{ReadFiles: agentcore.NewReadFileState()})
}

// ledgerRead drives the read tool against ctx so the file lands in THAT
// context's ledger exactly as the loop would record it.
func ledgerRead(t *testing.T, ctx context.Context, dir, path string) agentcore.AgentToolResult {
	t.Helper()
	tool := &ReadTool{Root: dir}
	raw, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, gerr := tool.Execute(ctx, "read-call-1", raw, nil)
	if gerr != nil {
		t.Fatalf("read go error: %v", gerr)
	}
	return res
}

func TestEditGuardRejectsUnreadFile(t *testing.T) {
	dir := testenv.Dir(t)
	seedFile(t, dir, "f.txt", "alpha\n")
	tool := &EditTool{Root: dir}
	res := runEditWithCtx(t, guardedContext(), tool, map[string]any{"path": "f.txt", "old_string": "alpha", "new_string": "beta"})
	if !strings.Contains(resultText(res), "has not been read yet") {
		t.Errorf("expected not-read rejection, got %q", resultText(res))
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "alpha\n" {
		t.Errorf("rejected edit must not mutate, got %q", got)
	}
}

func TestEditGuardPassesAfterRead(t *testing.T) {
	dir := testenv.Dir(t)
	ctx := guardedContext()
	ledgerRead(t, ctx, dir, "f.txt")
	tool := &EditTool{Root: dir}
	res := runEditWithCtx(t, ctx, tool, map[string]any{"path": "f.txt", "old_string": "alpha", "new_string": "beta"})
	if strings.Contains(resultText(res), "re-read") || strings.Contains(resultText(res), "not been read") {
		t.Fatalf("edit after read must pass, got %q", resultText(res))
	}
	// The edit itself is now the freshness baseline: a second edit passes too.
	res = runEditWithCtx(t, ctx, tool, map[string]any{"path": "f.txt", "old_string": "beta", "new_string": "gamma"})
	if strings.Contains(resultText(res), "modified since it was read") {
		t.Fatalf("post-edit state must be the new baseline, got %q", resultText(res))
	}
}

func TestEditGuardRejectsFingerprintMismatch(t *testing.T) {
	dir := testenv.Dir(t)
	ctx := guardedContext()
	seedFile(t, dir, "f.txt", "alpha\n")
	ledgerRead(t, ctx, dir, "f.txt")
	// External mutation between read and edit: size changes, so the fingerprint
	// check fires deterministically even on coarse-mtime filesystems.
	seedFile(t, dir, "f.txt", "alpha\nbeta\n")
	tool := &EditTool{Root: dir}
	res := runEditWithCtx(t, ctx, tool, map[string]any{"path": "f.txt", "old_string": "alpha", "new_string": "beta"})
	if !strings.Contains(resultText(res), "modified since it was read") {
		t.Errorf("expected stale-fingerprint rejection, got %q", resultText(res))
	}
}

func TestEditGuardWriteProvesResidency(t *testing.T) {
	dir := testenv.Dir(t)
	ctx := guardedContext()
	tool := &WriteTool{Root: dir}
	// Write without any read: legal (file creation path), and it proves
	// residency so a following edit passes without a read.
	raw, _ := json.Marshal(map[string]any{"path": "w.txt", "content": "one\ntwo\n"})
	if _, gerr := tool.Execute(ctx, "w-1", raw, nil); gerr != nil {
		t.Fatalf("write go error: %v", gerr)
	}
	etool := &EditTool{Root: dir}
	res := runEditWithCtx(t, ctx, etool, map[string]any{"path": "w.txt", "old_string": "one", "new_string": "ONE"})
	if strings.Contains(resultText(res), "not been read") || strings.Contains(resultText(res), "modified since") {
		t.Fatalf("edit after write must pass (write proves residency), got %q", resultText(res))
	}
}

func TestEditGuardTruncatedReadArmsResidency(t *testing.T) {
	// Spec D-2: qwen's "partial read does not re-arm" is not adopted — a
	// truncated read still carries real current content, so it arms residency.
	dir := testenv.Dir(t)
	content := strings.Repeat("line\n", 3000)
	seedFile(t, dir, "f.txt", content)
	ctx := guardedContext()
	ledgerRead(t, ctx, dir, "f.txt") // read caps at 2000 lines → truncated
	tool := &EditTool{Root: dir}
	res := runEditWithCtx(t, ctx, tool, map[string]any{"path": "f.txt", "old_string": "line", "new_string": "LINE"})
	if strings.Contains(resultText(res), "no longer in context") || strings.Contains(resultText(res), "not been read") {
		t.Fatalf("truncated read must arm residency, got %q", resultText(res))
	}
}

func TestEditGuardNilLedgerSkipsGuard(t *testing.T) {
	// Tools used outside a loop carry no ledger (AgentContext absent or
	// ReadFiles nil): behavior must be unchanged from pre-T3.5.
	dir := testenv.Dir(t)
	seedFile(t, dir, "f.txt", "alpha\n")
	tool := &EditTool{Root: dir}
	res := runEditWithCtx(t, context.Background(), tool, map[string]any{"path": "f.txt", "old_string": "alpha", "new_string": "beta"})
	if strings.Contains(resultText(res), "not been read") {
		t.Fatalf("nil ledger must skip the guard, got %q", resultText(res))
	}
}

// runEditWithCtx is runEdit with an explicit context.
func runEditWithCtx(t *testing.T, ctx context.Context, tool *EditTool, args map[string]any) agentcore.AgentToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, gerr := tool.Execute(ctx, "call-1", raw, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res
}
