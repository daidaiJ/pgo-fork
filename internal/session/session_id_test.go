package session

// Tests for the session-id uniqueness guard (a /btw-era fix, #T4.3): NewID
// resolves to the microsecond, so two ids minted on a fast path — or in the same
// Windows clock tick — can come out identical. Import/Fork derive a new session
// from an existing one, so a collision there silently overwrites the source
// file. These tests pin the collision away by forcing the worst case: the same
// timestamp the source id was minted from.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestImportNeverOverwritesSource verifies importing a session with the very
// timestamp its source id came from still yields a distinct session: the source
// file survives and both sessions are listed.
func TestImportNeverOverwritesSource(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	src := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now, Model: "faux"}
	if err := s.Save(src, agentcore.MessageList{userMsg("hello"), assistantMsg("hi")}); err != nil {
		t.Fatalf("Save source: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if _, err := s.Export(src.ID, path); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Worst case: the identical timestamp, which is what used to collide.
	got, entries, err := s.Import(path, now)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got.ID == src.ID {
		t.Fatalf("imported id %q collides with the source id — the source would be overwritten", got.ID)
	}
	if got.ParentSession != src.ID {
		t.Errorf("ParentSession = %q, want %q", got.ParentSession, src.ID)
	}
	if len(entries) != 2 {
		t.Errorf("imported entries = %d, want 2", len(entries))
	}
	// The source must still be intact and independently listed.
	if _, entries, err := s.LoadEntries(src.ID); err != nil || len(entries) != 2 {
		t.Fatalf("source session damaged: entries=%d err=%v", len(entries), err)
	}
	all, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List = %d sessions, want 2 (source + import)", len(all))
	}
}

// TestForkNeverOverwritesSource verifies the same guard for /fork and /clone:
// forking with the source's own timestamp yields a distinct session id.
func TestForkNeverOverwritesSource(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	src := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(src, agentcore.MessageList{userMsg("q"), assistantMsg("a")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, entries, err := s.LoadEntries(src.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	leaf := entries[len(entries)-1].ID

	got, _, err := s.Fork(src.ID, leaf, now)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if got.ID == src.ID {
		t.Fatalf("fork id %q collides with the source id", got.ID)
	}
	if _, err := os.Stat(s.path(src.ID)); err != nil {
		t.Fatalf("source session file missing after fork: %v", err)
	}
}

// TestOpenPeekAvoidsCollision verifies a second side thread opened in the same
// clock tick gets its own peek file instead of overwriting the first.
func TestOpenPeekAvoidsCollision(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	parent := saveParent(t, s, NewID(now))
	first, err := OpenPeek(s, parent, now)
	if err != nil {
		t.Fatalf("OpenPeek first: %v", err)
	}
	if err := first.Append(agentcore.MessageList{userMsg("q1")}); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	second, err := OpenPeek(s, parent, now)
	if err != nil {
		t.Fatalf("OpenPeek second: %v", err)
	}
	if second.ID() == first.ID() {
		t.Fatalf("second peek session reused id %q", first.ID())
	}
	msgs, err := first.Messages()
	if err != nil {
		t.Fatalf("first.Messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("first peek session was overwritten, got %d messages", len(msgs))
	}
}
