// Tests for the file-snapshot rewind journal: per-turn dedup of recorded paths,
// commit grouping (an untouched turn produces no restore point), and restore
// semantics — reverse replay across turns rolls a file to its oldest baseline,
// files that did not exist are deleted, and the journal is truncated to the tip.
package agenttool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// A turn's first Record for a path is the baseline; later Records that turn are
// ignored, and Commit groups the turn's files into one point.
func TestRecorderRecordDedupAndCommit(t *testing.T) {
	dir := testenv.Dir(t)
	f := filepath.Join(dir, "a.txt")
	writeFileT(t, f, "v0")

	r := NewFileSnapshotRecorder()
	r.Record(f)            // baseline "v0"
	writeFileT(t, f, "v1") // model's first edit
	r.Record(f)            // second edit same turn — must be ignored
	writeFileT(t, f, "v2")

	if !r.Commit("leaf0", "edit a") {
		t.Fatal("Commit reported no restore point despite a recorded file")
	}
	// An untouched turn creates nothing.
	if r.Commit("leaf1", "no edits") {
		t.Fatal("Commit created a restore point for a turn with no records")
	}
	points := r.Points()
	if len(points) != 1 || len(points[0].Snapshots) != 1 {
		t.Fatalf("want 1 point with 1 snapshot, got %+v", points)
	}
	if got := string(points[0].Snapshots[0].Content); got != "v0" {
		t.Errorf("baseline content = %q, want v0", got)
	}
}

// Restoring rolls files back and deletes ones that did not exist before, and
// returns the pre-turn leaf id.
func TestRecorderRestore(t *testing.T) {
	dir := testenv.Dir(t)
	existing := filepath.Join(dir, "keep.txt")
	created := filepath.Join(dir, "new.txt")
	writeFileT(t, existing, "orig")

	r := NewFileSnapshotRecorder()

	// Turn 1: edit an existing file.
	r.Record(existing)
	writeFileT(t, existing, "edited")
	r.Commit("leafA", "turn1")

	// Turn 2: create a brand-new file.
	r.Record(created)
	writeFileT(t, created, "brand new")
	r.Commit("leafB", "turn2")

	// Rewind to before turn 1 (index 0): both turns roll back.
	leaf, restored, warnings, err := r.Restore(0)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if leaf != "leafA" {
		t.Errorf("target leaf = %q, want leafA", leaf)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(restored) != 2 {
		t.Errorf("restored %d files, want 2", len(restored))
	}
	if got := readFileT(t, existing); got != "orig" {
		t.Errorf("existing file = %q, want orig", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("created file should have been deleted, stat err = %v", err)
	}
	if len(r.Points()) != 0 {
		t.Errorf("journal should be empty after restoring from index 0")
	}
}

// When a file is edited across several turns, restoring to before the earliest
// of them lands it at its oldest baseline (not an intermediate version).
func TestRecorderRestoreOldestBaselineWins(t *testing.T) {
	dir := testenv.Dir(t)
	f := filepath.Join(dir, "a.txt")
	writeFileT(t, f, "v0")

	r := NewFileSnapshotRecorder()
	r.Record(f) // baseline v0
	writeFileT(t, f, "v1")
	r.Commit("leaf0", "t1")

	r.Record(f) // baseline v1
	writeFileT(t, f, "v2")
	r.Commit("leaf1", "t2")

	// Rewind to before t2 only (index 1): file returns to v1.
	if _, _, _, err := r.Restore(1); err != nil {
		t.Fatalf("Restore(1): %v", err)
	}
	if got := readFileT(t, f); got != "v1" {
		t.Errorf("after rewind to before t2, file = %q, want v1", got)
	}
	// One point remains (t1); rewind it too → v0.
	if _, _, _, err := r.Restore(0); err != nil {
		t.Fatalf("Restore(0): %v", err)
	}
	if got := readFileT(t, f); got != "v0" {
		t.Errorf("after full rewind, file = %q, want v0", got)
	}
}

func TestRecorderRestoreOutOfRange(t *testing.T) {
	r := NewFileSnapshotRecorder()
	if _, _, _, err := r.Restore(0); err == nil {
		t.Error("Restore on empty journal should error")
	}
}

// A nil recorder is a safe no-op so tools can hold an optional handle.
func TestRecorderNilSafe(t *testing.T) {
	var r *FileSnapshotRecorder
	r.Record("/nonexistent")
	if r.Commit("x", "y") {
		t.Error("nil Commit should report no point")
	}
	if r.Points() != nil {
		t.Error("nil Points should be nil")
	}
}
