//go:build !windows

package session

// This file is the POSIX half of the session write lease: an exclusive advisory
// flock on a lock file that is never deleted.
//
// Why flock instead of the create/remove (O_EXCL) dance: a lock held by the
// kernel is released by the kernel when the holder dies — no TTL to guess, no
// heartbeat to refresh, no stale-takeover race. It also removes the create and
// delete from every session write, which on Windows was the source of an
// intermittent failure (a just-created file is not always deletable right away,
// which left a directory undeletable and failed unrelated tests' cleanup).

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive takes an exclusive advisory lock on f, without blocking. It
// returns an error for which lockBusy reports true when another holder has it.
func lockFileExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockFile drops the lock held on f.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// lockBusy reports whether err means "someone else holds this lock" (as opposed
// to a real I/O failure, such as flock being unsupported on the filesystem,
// which must be surfaced rather than retried).
func lockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES)
}
