package agentcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/testenv"
)

// seedLedgerFile writes a file and returns its resolved path plus stat, the
// shape every ledger record call needs.
func seedLedgerFile(t *testing.T, dir, name, content string) (string, string, time.Time, int64) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return p, name, info.ModTime(), info.Size()
}

func TestReadFileStateRecordAndResidency(t *testing.T) {
	s := NewReadFileState()
	resolved, arg, mod, size := seedLedgerFile(t, testenv.Dir(t), "f.txt", "hello\n")

	if s.Known(resolved) || s.Residency(resolved) {
		t.Fatal("empty ledger must know nothing")
	}
	s.RecordRead(ReadRecord{CallID: "call-1", ArgPath: arg, ResolvedPath: resolved, Content: "hello\n", ModTime: mod, Size: size})
	if !s.Known(resolved) || !s.Residency(resolved) {
		t.Fatal("read must create a resident entry")
	}
	if !s.FingerprintMismatch(resolved, mod.Add(time.Hour), size) {
		t.Error("different mtime must report a mismatch")
	}
	if s.FingerprintMismatch(resolved, mod, size) {
		t.Error("unchanged file must not report a mismatch")
	}

	// Revoking the only read call withdraws residency but keeps the entry
	// (fingerprint/snapshot survive — only the in-context copy is gone).
	s.RevokeCall("call-1")
	if !s.Known(resolved) {
		t.Fatal("revoke must not drop the entry")
	}
	if s.Residency(resolved) {
		t.Fatal("revoke must withdraw residency")
	}
}

func TestReadFileStateMutationProvesResidency(t *testing.T) {
	s := NewReadFileState()
	resolved, arg, mod, size := seedLedgerFile(t, testenv.Dir(t), "f.txt", "hello\n")
	s.RecordRead(ReadRecord{CallID: "call-1", ArgPath: arg, ResolvedPath: resolved, Content: "hello\n", ModTime: mod, Size: size})
	s.RevokeCall("call-1")

	// A write (or edit) after eviction re-proves residency from the mutation
	// itself — no read needed (qwen write-proves-residency).
	newMod := mod.Add(2 * time.Minute)
	s.RecordMutation(resolved, newMod, size)
	if !s.Residency(resolved) {
		t.Fatal("mutation must prove residency")
	}
	if s.FingerprintMismatch(resolved, newMod, size) {
		t.Error("RecordMutation must refresh the fingerprint")
	}

	// A write to a never-read file creates the entry on the spot.
	other := filepath.Join(filepath.Dir(resolved), "new.txt")
	s.RecordMutation(other, newMod, 3)
	if !s.Known(other) || !s.Residency(other) {
		t.Error("write to unknown file must create a resident entry")
	}
}

func TestReadFileStateTwoReadCallsPartialRevoke(t *testing.T) {
	// The microcompaction-reversal case: two reads of the same file, one
	// evicted — the newer read keeps the file resident.
	s := NewReadFileState()
	resolved, arg, mod, size := seedLedgerFile(t, testenv.Dir(t), "f.txt", "hello\n")
	s.RecordRead(ReadRecord{CallID: "call-1", ArgPath: arg, ResolvedPath: resolved, Content: "a", ModTime: mod, Size: size})
	s.RecordRead(ReadRecord{CallID: "call-2", ArgPath: arg, ResolvedPath: resolved, Content: "b", ModTime: mod, Size: size})
	s.RevokeCall("call-1")
	if !s.Residency(resolved) {
		t.Fatal("newer read must keep residency after an older one is revoked")
	}
	s.RevokeCall("call-2")
	if s.Residency(resolved) {
		t.Fatal("revoking every read must withdraw residency")
	}
	// Revoked call ids are pruned from the reverse index (no unbounded growth).
	s.RevokeCall("call-1") // already gone — must be a silent no-op
}

func TestReadFileStateRevokeAllResidency(t *testing.T) {
	// #4239 "cannot reverse-map → revoke everything": every entry loses
	// residency, fingerprints and snapshots survive.
	s := NewReadFileState()
	dir := testenv.Dir(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		resolved, arg, mod, size := seedLedgerFile(t, dir, name, "x\n")
		s.RecordRead(ReadRecord{CallID: "call-" + name, ArgPath: arg, ResolvedPath: resolved, Content: "x\n", ModTime: mod, Size: size})
	}
	s.RevokeAllResidency()
	for _, e := range []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")} {
		if s.Residency(e) {
			t.Errorf("%s still resident after RevokeAllResidency", e)
		}
		if !s.Known(e) {
			t.Errorf("%s entry dropped — fingerprint/snapshot must survive", e)
		}
	}
}

func TestReadFileStateSnapshotCapAndArgPathLookup(t *testing.T) {
	s := NewReadFileState()
	resolved, arg, mod, size := seedLedgerFile(t, testenv.Dir(t), "big.txt", strings.Repeat("x", 20*1024))
	s.RecordRead(ReadRecord{CallID: "call-1", ArgPath: arg, ResolvedPath: resolved, Content: strings.Repeat("x", 20*1024), ModTime: mod, Size: size})

	got, ok := s.ContentByArgPath(arg)
	if !ok {
		t.Fatal("snapshot must be retrievable by the raw argument path")
	}
	if len(got) > 8*1024+len(snapshotTruncationMark) || !strings.HasSuffix(got, snapshotTruncationMark) {
		t.Errorf("snapshot must be capped at %d chars with a truncation mark, got %d chars", 8*1024, len(got))
	}
	if _, ok := s.ContentByArgPath("never-read.txt"); ok {
		t.Error("unknown arg path must not resolve")
	}
}

func TestReadFileStateRevokeEvictedIdentified(t *testing.T) {
	// Two files; the evicted read calls are identifiable via the reverse
	// index → per-file revocation, the other file keeps residency.
	s := NewReadFileState()
	dir := testenv.Dir(t)
	aPath, aArg, aMod, aSize := seedLedgerFile(t, dir, "a.go", "a\n")
	bPath, bArg, bMod, bSize := seedLedgerFile(t, dir, "b.go", "b\n")
	s.RecordRead(ReadRecord{CallID: "call-a1", ArgPath: aArg, ResolvedPath: aPath, Content: "a\n", ModTime: aMod, Size: aSize})
	s.RecordRead(ReadRecord{CallID: "call-b1", ArgPath: bArg, ResolvedPath: bPath, Content: "b\n", ModTime: bMod, Size: bSize})

	s.RevokeEvicted([]string{"call-a1"})
	if s.Residency(aPath) {
		t.Error("evicted read must lose residency")
	}
	if !s.Residency(bPath) {
		t.Error("unrelated file must keep residency")
	}
}

func TestReadFileStateRevokeEvictedUnmappableRevokeAll(t *testing.T) {
	// #4239: an evicted read call the reverse index cannot identify (recorded
	// before the ledger existed) revokes EVERY read-armed residency — but
	// write/edit proven residency survives (those results are whitelist-exempt
	// from eviction, so the evidence is still in the history).
	s := NewReadFileState()
	dir := testenv.Dir(t)
	rPath, rArg, rMod, rSize := seedLedgerFile(t, dir, "r.go", "r\n")
	wPath, _, wMod, wSize := seedLedgerFile(t, dir, "w.go", "w\n")
	s.RecordRead(ReadRecord{CallID: "old-call", ArgPath: rArg, ResolvedPath: rPath, Content: "r\n", ModTime: rMod, Size: rSize})
	s.RecordMutation(wPath, wMod, wSize)

	s.RevokeEvicted([]string{"unrecorded-call"})
	if s.Residency(rPath) {
		t.Error("read-armed residency must be revoked by the unmappable branch")
	}
	if !s.Residency(wPath) {
		t.Error("write-proven residency must survive the unmappable branch")
	}
}
