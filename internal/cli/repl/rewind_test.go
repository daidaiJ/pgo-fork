// Tests for the /rewind command wiring: listing restore points and restoring
// files + conversation. Restore points are derived from the session tree
// (cli.DeriveRewindPoints), so the tests persist a turn via AppendBranch the
// way the REPL loop does; the file-snapshot journal itself is tested in
// agenttool.
package repl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
)

// seedTurn persists one user turn as the session's first branch entry and
// returns the replDeps ready for runRewind.
func seedTurn(t *testing.T, deps *replDeps, prompt string) {
	t.Helper()
	msg := agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}}
	leaf, err := deps.store.AppendBranch(deps.header, "", agentcore.MessageList{msg})
	if err != nil {
		t.Fatal(err)
	}
	deps.agentCtx.Messages = agentcore.MessageList{msg}
	deps.persisted = 1
	deps.curLeaf = leaf
}

// /rewind with no argument lists the tree-derived restore points.
func TestREPLRewindListsPoints(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	deps.snap = agenttool.NewFileSnapshotRecorder()
	seedTurn(t, &deps, "add feature X")

	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.snap.Record(f)
	deps.snap.Commit("", "add feature X")

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind")
	got := out.String()
	if !strings.Contains(got, "restore points") || !strings.Contains(got, "add feature X") {
		t.Errorf("listing missing expected content:\n%s", got)
	}
	if !strings.Contains(got, "1 file") {
		t.Errorf("listing missing file count:\n%s", got)
	}
}

// /rewind N restores the file to its baseline and resets the conversation when
// the point's leaf is empty (it was the session's first turn).
func TestREPLRewindRestoresFiles(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	deps.snap = agenttool.NewFileSnapshotRecorder()
	seedTurn(t, &deps, "edit a.txt")

	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.snap.Record(f) // baseline "original"
	if err := os.WriteFile(f, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.snap.Commit("", "edit a.txt")

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind 1")

	if data, _ := os.ReadFile(f); string(data) != "original" {
		t.Errorf("file not restored: got %q, want original", string(data))
	}
	if len(deps.agentCtx.Messages) != 0 {
		t.Errorf("conversation not reset: %d messages remain", len(deps.agentCtx.Messages))
	}
	if len(deps.snap.Points()) != 0 {
		t.Errorf("journal not truncated after rewind")
	}
	if !strings.Contains(out.String(), "rewound to before point 1") {
		t.Errorf("missing confirmation:\n%s", out.String())
	}
}

// /rewind works without a snapshot recorder (file tools disabled): points come
// from the tree, the selection switches the leaf, and no files are restored.
func TestREPLRewindWithoutSnapshots(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	deps.snap = nil
	seedTurn(t, &deps, "hello there")

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind 1")
	got := out.String()
	if !strings.Contains(got, "rewound to before point 1") {
		t.Errorf("missing confirmation:\n%s", got)
	}
	if !strings.Contains(got, "no files to restore") {
		t.Errorf("missing file-restore note:\n%s", got)
	}
	if len(deps.agentCtx.Messages) != 0 {
		t.Errorf("conversation not reset: %d messages remain", len(deps.agentCtx.Messages))
	}
}

// /rewind lists points on abandoned branches with the ↩ marker and selecting
// one switches the conversation back to before that turn (G1).
func TestREPLRewindAbandonedBranch(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	seedTurn(t, &deps, "branch one") // first branch, later abandoned

	// A second turn re-rooted from the session start: a sibling branch that
	// becomes the active one.
	msg := agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("branch two")}}
	leafB, err := deps.store.AppendBranch(deps.header, "", agentcore.MessageList{msg})
	if err != nil {
		t.Fatal(err)
	}
	deps.agentCtx.Messages = agentcore.MessageList{msg}
	deps.persisted = 1
	deps.curLeaf = leafB

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind")
	got := out.String()
	if !strings.Contains(got, "↩ abandoned branch") || !strings.Contains(got, "branch one") {
		t.Errorf("abandoned branch not listed:\n%s", got)
	}
	if strings.Contains(got, "branch two  \n") || !strings.Contains(got, "branch two") {
		t.Errorf("active branch point missing:\n%s", got)
	}

	out.Reset()
	runRewind(&out, &deps, "/rewind 1")
	if !strings.Contains(out.String(), "rewound to before point 1") {
		t.Errorf("missing confirmation:\n%s", out.String())
	}
	if len(deps.agentCtx.Messages) != 0 || deps.curLeaf != "" {
		t.Errorf("did not switch back to before the abandoned turn: %d messages, leaf %q",
			len(deps.agentCtx.Messages), deps.curLeaf)
	}
	// Both branches stay complete on disk (AppendBranch keeps every entry).
	_, entries, err := deps.store.LoadEntries(deps.header.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("session tree should keep both branches: %d entries on disk", len(entries))
	}
}

// A successful rewind hands the turn's prompt back to the line editor so the
// next input line is prefilled with it (G2).
func TestREPLRewindRefillsPrompt(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	seedTurn(t, &deps, "retry this prompt")
	deps.editor = newREPLLineEditor(strings.NewReader(""), deps.in, &bytes.Buffer{}, deps.slash, nil)

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind 1")
	if deps.editor.prefill != "retry this prompt" {
		t.Errorf("input not refilled: prefill = %q", deps.editor.prefill)
	}
}
