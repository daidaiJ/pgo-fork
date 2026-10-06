// This file implements the readFileState ledger (T3.5): an in-process record
// of the files the model has read, edited, or written, so the edit tool can
// refuse to act on stale knowledge and microcompaction can revoke that
// knowledge when it evicts the read results it was based on (qwen #4239
// semantics — see wiki/port/read-file-state.md).
//
// The ledger is per-AgentContext and never persisted: after a process restart
// every file is "not read yet" and the edit guard sends the model back through
// a read — the safe direction. Entries carry a cheap mtime+size fingerprint
// (no content hashing on the edit path — one stat, zero extra IO), a truncated
// content snapshot of the latest read (the post-compaction reminder's
// re-injection source), and the set of read tool-call ids whose results are
// still resident in the live history.
package agentcore

import (
	"path/filepath"
	"sync"
	"time"
)

// snapshotCapChars caps the stored content snapshot per file. A read tool
// output can reach 2000 lines × 2000 chars; without a cap a long session would
// pin megabytes of stale text. The reminder re-injection path caps at 5K per
// file anyway, so 8K of stored snapshot never binds it.
const snapshotCapChars = 8 * 1024

// snapshotTruncationMark is appended when a snapshot is cut at the cap.
const snapshotTruncationMark = "\n… (snapshot truncated)"

type readFileEntry struct {
	Path    string // resolved absolute path (ledger key)
	ArgPath string // raw argument path the model last used (reminder lookup)
	ModTime int64  // fingerprint: mtime as UnixNano
	Size    int64  // fingerprint: bytes
	ReadAt  int64  // wall-clock ms of the latest read (recency ordering)
	Content string // snapshot of what the read tool returned
	Calls   []string // read call ids whose results are still in history
	Proven  bool     // residency proven by a write/edit (strongest evidence)
}

// ReadFileState is the ledger itself. The zero value is not usable; NewReadFileState
// is the constructor. All methods are safe for concurrent use.
type ReadFileState struct {
	mu     sync.Mutex
	files  map[string]*readFileEntry
	byCall map[string]string // read call id → resolved path (#4239 reverse index)
}

// NewReadFileState returns an empty ledger.
func NewReadFileState() *ReadFileState {
	return &ReadFileState{
		files:  make(map[string]*readFileEntry),
		byCall: make(map[string]string),
	}
}

// ReadRecord is the observation a tool hands to RecordRead: the resolved path
// plus everything the ledger should remember about this read.
type ReadRecord struct {
	CallID string
	// ArgPath is the raw argument path the model used (pre-resolution), kept
	// for the reminder's arg-path lookup; resolvedPath is the ledger key.
	ArgPath      string
	ResolvedPath string
	Content      string
	ModTime      time.Time
	Size         int64
}

// RecordRead records a successful read: it refreshes the fingerprint, snapshot,
// and recency, and arms residency for this call. A record without a call id
// still refreshes state but does not add residency evidence (nothing to revoke
// later).
func (s *ReadFileState) RecordRead(r ReadRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.files[r.ResolvedPath]
	if e == nil {
		e = &readFileEntry{Path: r.ResolvedPath}
		s.files[r.ResolvedPath] = e
	}
	e.ArgPath = r.ArgPath
	e.ModTime = r.ModTime.UnixNano()
	e.Size = r.Size
	e.ReadAt = time.Now().UnixMilli()
	e.Content = truncateSnapshot(r.Content)
	if r.CallID != "" {
		e.Calls = append(e.Calls, r.CallID)
		s.byCall[r.CallID] = r.ResolvedPath
	}
}

// RecordMutation records that a write or edit just produced the file's current
// content on disk: the fingerprint is refreshed and residency is proven — the
// model has seen either the full text (write) or the diff (edit), so its
// knowledge is current without a read.
func (s *ReadFileState) RecordMutation(resolvedPath string, modTime time.Time, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.files[resolvedPath]
	if e == nil {
		// A write to a file that was never read: the write itself is the
		// residency evidence, so create the entry on the spot.
		e = &readFileEntry{Path: resolvedPath}
		s.files[resolvedPath] = e
	}
	e.ModTime = modTime.UnixNano()
	e.Size = size
	e.Proven = true
}

// RevokeCall withdraws the residency one read call contributed (its result was
// evicted from the history). The fingerprint and snapshot survive: they still
// describe the on-disk file, only the model's in-context copy is gone.
func (s *ReadFileState) RevokeCall(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, ok := s.byCall[callID]
	if !ok {
		return
	}
	delete(s.byCall, callID)
	if e := s.files[path]; e != nil {
		for i, c := range e.Calls {
			if c == callID {
				e.Calls = append(e.Calls[:i], e.Calls[i+1:]...)
				break
			}
		}
	}
}

// RevokeAllResidency is the #4239 "cannot reverse-map, revoke everything"
// defense: when an evicted read call predates the ledger (session restore) its
// file cannot be identified, so every file's residency is withdrawn at once.
// Fingerprints and snapshots survive.
func (s *ReadFileState) RevokeAllResidency() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.files {
		e.Calls = nil
		e.Proven = false
	}
	s.byCall = make(map[string]string)
}

// Residency reports whether the file's content is still evidenced in the live
// history: at least one un-evicted read call, or a write/edit proof.
func (s *ReadFileState) Residency(resolvedPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.files[resolvedPath]
	if e == nil {
		return false
	}
	return len(e.Calls) > 0 || e.Proven
}

// Known reports whether the ledger has any entry for the path at all — the
// "file has not been read yet" distinction the edit guard needs before it can
// ask the residency question.
func (s *ReadFileState) Known(resolvedPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[resolvedPath]
	return ok
}

// FingerprintMismatch reports whether the file's current stat disagrees with
// the ledger (the file changed on disk since the model last saw it).
func (s *ReadFileState) FingerprintMismatch(resolvedPath string, modTime time.Time, size int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.files[resolvedPath]
	if e == nil {
		return true
	}
	return e.ModTime != modTime.UnixNano() || e.Size != size
}

// ContentByArgPath returns the stored snapshot for the most recent read that
// used the given raw argument path, and whether one exists.
func (s *ReadFileState) ContentByArgPath(argPath string) (string, bool) {
	key := filepath.Clean(argPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.files {
		if filepath.Clean(e.ArgPath) == key && e.Content != "" {
			return e.Content, true
		}
	}
	return "", false
}

// truncateSnapshot caps content at snapshotCapChars, appending a visible
// truncation mark so injected snapshots never masquerade as complete files.
func truncateSnapshot(content string) string {
	if len(content) <= snapshotCapChars {
		return content
	}
	return content[:snapshotCapChars] + snapshotTruncationMark
}
