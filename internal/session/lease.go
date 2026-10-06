// This file implements the session write lease (T6.1): a cross-process mutual
// exclusion primitive that serializes writes to one session file.
//
// Why a lease and not just temp+rename: atomicWrite (see session.go) already
// guarantees a *reader* never sees a half-written file, but it says nothing
// about two *writers*. Two pigo processes appending to the same session (a REPL
// and a /btw side thread, or a resumed session still being written by an older
// process) would each load-modify-save and the last rename would silently drop
// the other's turn. A lease turns that lost update into a serialized write.
//
// The lock itself is a kernel lock on a lock file that is created once and never
// deleted (flock on POSIX, LockFileEx on Windows — see lease_unix.go and
// lease_windows.go). The kernel owns it, so:
//
//   - a crashed or killed holder releases it immediately: no TTL to guess, no
//     heartbeat to refresh, and no stale-takeover race to get wrong;
//   - a session write no longer creates and deletes a file, which on Windows was
//     reliably unreliable — a just-created file is not always deletable at once,
//     and a leftover lock file made a directory undeletable, failing unrelated
//     tests' cleanup.
//
// Contention is filtered by an in-process gate first, so two goroutines in one
// process serialize on a semaphore instead of on the file. The kernel lock still
// carries the cross-process case, which the gate cannot see.
package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrLocked is returned by Store.AcquireLease when a live lease is still held
// after the wait window. It is deliberately distinct from the I/O errors
// AcquireLease may also return: ErrLocked means "another writer is in its
// critical section", which a caller can retry or queue later, while a wrapped
// error means the write cannot proceed at all.
var ErrLocked = errors.New("session: another live writer holds the lease")

// DefaultLeaseWait is how long AcquireLease waits for a busy lease before giving
// up and returning ErrLocked. Waiting — rather than failing on first contact —
// is what makes concurrent writers *serialize* instead of losing one of them:
// each append waits its turn and every turn is persisted.
const DefaultLeaseWait = 10 * time.Second

// Lease is an acquired session write lease. The holder must Release it (usually
// via defer); Release is idempotent so a double call is safe.
type Lease struct {
	path string
	// file is the open lock file whose kernel lock IS the lease. Closing it
	// releases the lock, which is why Release never deletes anything.
	file *os.File
	// gate is the in-process semaphore taken alongside the kernel lock (see
	// AcquireLease); Release returns it.
	gate     chan struct{}
	released bool
}

// leasePath is the lock file for a session id. It sits next to the session file
// as <id>.jsonl.lock, a name Store.listHeaders ignores (it only enumerates names
// ending in ".jsonl"), so a lock is never mistaken for a session.
func (s *Store) leasePath(id string) string {
	return filepath.Join(s.dir, FileName(id)+".lock")
}

// AcquireLease takes the write lease for session id. When another live writer
// holds it, AcquireLease waits up to DefaultLeaseWait for its turn and returns
// ErrLocked if the lease is still busy then. The caller must Release the
// returned lease.
func (s *Store) AcquireLease(id string) (*Lease, error) {
	path := s.leasePath(id)
	// The gate is a semaphore, not a mutex: it must be time-bounded. A plain
	// sync.Mutex would turn a legitimate "someone else holds it" into a hang if
	// two goroutines in one process ever contended — including by accident,
	// through a nested write path.
	gate := processGate(path)
	if !waitGate(gate, DefaultLeaseWait) {
		return nil, ErrLocked
	}
	l, err := acquireLease(path, DefaultLeaseWait)
	if err != nil {
		releaseGate(gate)
		return nil, err
	}
	l.gate = gate
	return l, nil
}

// acquireLease is the testable core of AcquireLease, parameterized on path and
// wait so a test can use a short window instead of the default.
func acquireLease(path string, wait time.Duration) (*Lease, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("session: create lease dir: %w", err)
	}
	// The lock file is created once and kept: the kernel lock lives on the open
	// description, not on the file's existence.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open lease %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	backoff := 2 * time.Millisecond
	for {
		err := lockFileExclusive(f)
		if err == nil {
			return &Lease{path: path, file: f}, nil
		}
		if !lockBusy(err) {
			f.Close()
			return nil, fmt.Errorf("session: lock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, ErrLocked
		}
		time.Sleep(backoff)
		if backoff < 25*time.Millisecond {
			backoff *= 2
		}
	}
}

// Renew is a no-op kept for API compatibility with callers that hold a lease
// across a long write: with a kernel lock the lease cannot expire underneath the
// holder, so there is nothing to refresh.
func (l *Lease) Renew() error {
	if l == nil || l.released {
		return ErrLocked
	}
	return nil
}

// Release drops the lease: it unlocks and closes the lock file and returns the
// in-process gate. The file itself is never deleted — deleting it would race
// every other process that has it open, and the lock does not depend on the
// file's existence anyway.
//
// It is safe to call in a defer and safe to call twice: a second call is a no-op
// and never panics.
func (l *Lease) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	if l.gate != nil {
		defer releaseGate(l.gate)
	}
	// Closing the file releases the kernel lock even if the explicit unlock
	// fails, so neither error can leave the lease held.
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if closeErr != nil {
		return fmt.Errorf("session: release lease: %w", closeErr)
	}
	if unlockErr != nil {
		return fmt.Errorf("session: unlock lease: %w", unlockErr)
	}
	return nil
}

// Path exposes the lock file, for diagnostics and tests.
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// withLease runs fn while holding the write lease for id, releasing it on the
// way out whether fn succeeded or failed. It is the wrapper that turns Save and
// SaveEntries into mutually exclusive operations across processes.
func (s *Store) withLease(id string, fn func() error) error {
	l, err := s.AcquireLease(id)
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}

// gateByPath holds the in-process semaphore guarding each lease path. Gates are
// keyed by path rather than by Store so two Store instances pointing at the same
// directory (two "processes" in a test, or two components in one binary) still
// serialize.
var (
	gateMu     sync.Mutex
	gateByPath = map[string]chan struct{}{}
)

// processGate returns the capacity-1 semaphore for one lease path.
func processGate(path string) chan struct{} {
	gateMu.Lock()
	defer gateMu.Unlock()
	g, ok := gateByPath[path]
	if !ok {
		g = make(chan struct{}, 1)
		gateByPath[path] = g
	}
	return g
}

// waitGate tries to take the gate until wait elapses, with a short backoff. It
// reports whether the gate was taken; false means every in-process slot was busy
// for the whole window.
func waitGate(g chan struct{}, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	backoff := time.Millisecond
	for {
		select {
		case g <- struct{}{}:
			return true
		default:
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(backoff)
		if backoff < 10*time.Millisecond {
			backoff *= 2
		}
	}
}

// releaseGate returns the gate to the pool.
func releaseGate(g chan struct{}) {
	select {
	case <-g:
	default: // not held (defensive): never block and never panic
	}
}
