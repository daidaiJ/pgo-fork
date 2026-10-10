package session

// TestStoreDeleteRemovesSubagentSidecar pins the T7.1 contract that deleting a
// session also removes its sub-agent sidecar directory (<id>.subagents), so a
// later session reusing the id cannot inherit another conversation's resumable
// child transcripts. The literal is mirrored from runtime.SubagentsSidecarSuffix
// (the two packages do not import each other).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

func TestStoreDeleteRemovesSubagentSidecar(t *testing.T) {
	root := filepath.Join(testenv.Dir(t), "sessions")
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const id = "sess-1"
	if err := store.Save(SessionHeader{ID: id}, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sidecar := filepath.Join(root, id+".subagents", "ag-1")
	if err := os.MkdirAll(sidecar, 0o755); err != nil {
		t.Fatalf("MkdirAll sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sidecar, "transcript.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	if err := store.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, id+".subagents")); !os.IsNotExist(err) {
		t.Errorf("sidecar survived Delete: stat err = %v", err)
	}
}
