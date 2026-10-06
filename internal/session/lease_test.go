package session

// Tests for the session write lease and the truncation protocol (T6.1): the
// lease is what turns two concurrent writers into a serialized pair instead of a
// lost update, and the tolerant decoder is what keeps a session torn by a crash
// resumable instead of unreadable.

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestAcquireLeaseIsExclusive is the core guarantee: a second writer must not be
// able to enter the critical section while the first holds it. It probes with a
// zero wait because Store.AcquireLease deliberately *waits* for its turn — that
// contract is covered by TestAcquireLeaseWaitsForItsTurn.
func TestAcquireLeaseIsExclusive(t *testing.T) {
	s := newStore(t)
	first, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer first.Release()

	if _, err := acquireLease(s.leasePath("s1"), 0); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire = %v, want ErrLocked", err)
	}
}

// TestAcquireLeaseWaitsForItsTurn covers the waiting contract: a busy lease is
// not a failure, it is a queue. A writer that arrives while another holds the
// lease gets it once the holder releases, so no session write is dropped just
// because two writers overlapped.
func TestAcquireLeaseWaitsForItsTurn(t *testing.T) {
	s := newStore(t)
	holder, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		holder.Release()
	}()
	start := time.Now()
	next, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("waiting AcquireLease = %v, want it to get the lease after the holder released", err)
	}
	next.Release()
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("acquired in %v, want it to have waited for the holder", elapsed)
	}
}

// TestAcquireLeaseIsPerSession verifies the lease granularity: holding one
// session's lease must not block an unrelated session (a global lock would
// serialize every session in the store, which is not the contract).
func TestAcquireLeaseIsPerSession(t *testing.T) {
	s := newStore(t)
	a, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease s1: %v", err)
	}
	defer a.Release()
	b, err := s.AcquireLease("s2")
	if err != nil {
		t.Fatalf("AcquireLease s2 = %v, want success (leases are per session)", err)
	}
	b.Release()
}

func TestLeaseReleaseAllowsReacquire(t *testing.T) {
	s := newStore(t)
	l, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("reacquire after release = %v, want success", err)
	}
	again.Release()
}

func TestLeaseReleaseIsIdempotent(t *testing.T) {
	s := newStore(t)
	l, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release = %v, want nil (idempotent)", err)
	}
}

// TestLeaseFreedWhenHolderDisappears is the property a kernel lock buys and a
// TTL-based lock can only approximate: when the holder goes away without
// releasing, the lease is immediately free — no waiting for a staleness window,
// no takeover race. Closing the file stands in for the process dying.
func TestLeaseFreedWhenHolderDisappears(t *testing.T) {
	dir := testDir(t)
	path := dir + "/s1.jsonl.lock"
	l, err := acquireLease(path, time.Second)
	if err != nil {
		t.Fatalf("acquireLease: %v", err)
	}
	// Simulate the holder dying: no Release, just the file going away with it.
	if err := l.file.Close(); err != nil {
		t.Fatalf("close holder file: %v", err)
	}
	next, err := acquireLease(path, time.Second)
	if err != nil {
		t.Fatalf("acquire after holder disappeared = %v, want immediate success", err)
	}
	next.Release()
}

// TestLeaseLockFilePersists documents the on-disk convention: the lock file is
// created once and kept. Deleting it would race every process that has it open,
// and the lease does not depend on the file existing.
func TestLeaseLockFilePersists(t *testing.T) {
	s := newStore(t)
	l, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(s.leasePath("s1")); err != nil {
		t.Fatalf("lock file after release: %v, want it kept (never deleted)", err)
	}
}

// TestLeaseRenewAfterReleaseFails pins Renew's new meaning: with a kernel lock
// there is nothing to refresh, and asking a released lease to renew is an error
// rather than a silent no-op.
func TestLeaseRenewAfterReleaseFails(t *testing.T) {
	s := newStore(t)
	l, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := l.Renew(); err != nil {
		t.Fatalf("Renew while held = %v, want nil (nothing to refresh)", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := l.Renew(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Renew after release = %v, want ErrLocked", err)
	}
}

// TestLeaseFileIsNotASession guards the naming convention: the lock file lives
// next to the session under a ".jsonl.lock" name, which listing must never
// mistake for a conversation.
func TestLeaseFileIsNotASession(t *testing.T) {
	s := newStore(t)
	l, err := s.AcquireLease("s1")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer l.Release()

	headers, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(headers) != 0 {
		t.Fatalf("List returned %d sessions, want 0 (lease/digest files are not sessions)", len(headers))
	}
}

// TestConcurrentAppendsKeepEveryTurn is the acceptance criterion behind T6.1:
// concurrent appends to one session lose nothing. Each writer appends one turn;
// a lost update would show up as fewer messages than writers.
func TestConcurrentAppendsKeepEveryTurn(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "shared", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A separate Store per writer: two processes share only the directory.
			w, err := NewStore(s.Dir())
			if err != nil {
				errs <- err
				return
			}
			errs <- w.Append("shared", now, agentcore.MessageList{
				agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(string(rune('a' + i)))}},
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append: %v", err)
		}
	}

	_, msgs, err := s.Load("shared")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != writers {
		t.Fatalf("loaded %d messages, want %d (a lost update dropped a writer's turn)", len(msgs), writers)
	}
}

// TestLoadDropsTruncatedTail covers crash recovery on the read side: a final line
// cut off mid-write must not make the whole session unreadable.
func TestLoadDropsTruncatedTail(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "torn", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	appendRaw(t, s.path("torn"), `{"id":"half","parentId":"x","timestamp":"2026`)

	_, entries, err := s.LoadEntries("torn")
	if err != nil {
		t.Fatalf("LoadEntries on torn tail = %v, want the intact prefix", err)
	}
	if want := len(sampleMessages()); len(entries) != want {
		t.Fatalf("loaded %d entries, want %d (only the torn line may be dropped)", len(entries), want)
	}
}

// TestLoadRejectsCorruptionMidFile is the safety boundary of that recovery: a bad
// line with content after it is corruption, not a torn tail, and stays an error
// in both modes — silently dropping it would delete the rest of the session.
func TestLoadRejectsCorruptionMidFile(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "mid", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	msgs := sampleMessages()[:1]
	if err := s.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(s.path("mid"))
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("fixture has %d lines, want header + 1 entry", len(lines))
	}
	// header, broken line, then the intact entry — damage in the middle.
	corrupt := strings.Join([]string{lines[0], `{"id":"broken",`, lines[1]}, "\n") + "\n"
	if err := os.WriteFile(s.path("mid"), []byte(corrupt), 0o644); err != nil {
		t.Fatalf("write corrupt session: %v", err)
	}
	if _, _, err := s.LoadEntries("mid"); err == nil {
		t.Fatal("LoadEntries accepted a corrupt line in the middle, want an error")
	}
}

// TestRepairRewritesTruncatedTail covers the explicit repair path: the torn line
// is dropped on disk, so a later reader sees a clean file without paying for the
// tolerant decode.
func TestRepairRewritesTruncatedTail(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "fix", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	appendRaw(t, s.path("fix"), `{"id":"half","paren`)

	repaired, err := s.Repair("fix")
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if !repaired {
		t.Fatal("Repair reported no work on a torn file")
	}
	raw, err := os.ReadFile(s.path("fix"))
	if err != nil {
		t.Fatalf("read repaired session: %v", err)
	}
	if strings.Contains(string(raw), "half") {
		t.Fatal("repaired file still contains the torn line")
	}
	again, err := s.Repair("fix")
	if err != nil {
		t.Fatalf("second Repair: %v", err)
	}
	if again {
		t.Fatal("second Repair reported work on an already clean file")
	}
}

// TestAppendHealsTruncatedSession is the write-side counterpart: appending to a
// torn session persists the intact prefix and leaves nothing torn behind, so a
// session recovers the moment it is used again.
func TestAppendHealsTruncatedSession(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "heal", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	appendRaw(t, s.path("heal"), `{"id":"half","paren`)

	extra := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("next")}},
	}
	if err := s.Append("heal", now, extra); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_, msgs, err := s.Load("heal")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := len(sampleMessages()) + 1; len(msgs) != want {
		t.Fatalf("loaded %d messages, want %d", len(msgs), want)
	}
	if _, _, torn, err := s.loadTolerant("heal"); err != nil || torn {
		t.Fatalf("session still torn after append (torn=%v, err=%v)", torn, err)
	}
}

// TestSaveIsIdempotentForUnchangedContent covers the sentinel: rewriting identical
// content leaves the committed file untouched, so a driver that saves on every
// turn does not churn the file (and its mtime) when nothing changed. It drives
// SaveEntries because Save stamps a fresh timestamp on every entry, which is a
// genuine content change.
func TestSaveIsIdempotentForUnchangedContent(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "idem", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	entries := []Entry{{ID: "e1", Timestamp: now, Message: sampleMessages()[0]}}
	if err := s.SaveEntries(header, entries); err != nil {
		t.Fatalf("SaveEntries: %v", err)
	}
	before, err := os.Stat(s.path("idem"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // make any rewrite's mtime distinguishable
	if err := s.SaveEntries(header, entries); err != nil {
		t.Fatalf("second SaveEntries: %v", err)
	}
	after, err := os.Stat(s.path("idem"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("identical save rewrote the file (mtime %v → %v), want a no-op", before.ModTime(), after.ModTime())
	}
}

// TestSaveRewritesWhenContentChanges keeps the sentinel honest: a real change
// must still be committed.
func TestSaveRewritesWhenContentChanges(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: "chg", Version: SchemaVersion, CreatedAt: now, UpdatedAt: now}
	entries := []Entry{{ID: "e1", Timestamp: now, Message: sampleMessages()[0]}}
	if err := s.SaveEntries(header, entries); err != nil {
		t.Fatalf("SaveEntries: %v", err)
	}
	if err := s.SaveEntries(header, entries[:0]); err != nil {
		t.Fatalf("second SaveEntries: %v", err)
	}
	_, got, err := s.LoadEntries("chg")
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("loaded %d entries, want 0 (changed content must be committed)", len(got))
	}
}

// appendRaw appends raw bytes to a session file, standing in for a write that was
// cut off mid-line.
func appendRaw(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatalf("append raw: %v", err)
	}
}
