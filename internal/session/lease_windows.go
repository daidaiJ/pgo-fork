//go:build windows

package session

// This file is the Windows half of the session write lease: an exclusive
// LockFileEx on a lock file that is never deleted.
//
// Why LockFileEx instead of the create/remove (O_EXCL) dance: the kernel owns the
// lock, so it is released when the holder dies — no TTL to guess, no heartbeat to
// refresh, no stale-takeover race. Just as importantly it removes a file create
// and delete from every session write: on Windows a just-created file is not
// always deletable immediately, and a leftover lock file made a directory
// undeletable, which failed unrelated tests' TempDir cleanup.

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockBytes is how much of the file is locked. The lock is a mutex, not a
// byte-range guard over real data, so one byte at offset 0 is enough — locking a
// range beyond EOF is legal and is the usual way to use LockFileEx as a mutex.
const lockBytes = 1

// lockFileExclusive takes an exclusive lock on f without blocking. It returns an
// error for which lockBusy reports true when another holder has it.
func lockFileExclusive(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockBytes, 0, &ol)
}

// unlockFile drops the lock held on f.
func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockBytes, 0, &ol)
}

// lockBusy reports whether err means "someone else holds this lock". Windows
// reports that as ERROR_LOCK_VIOLATION (and ERROR_IO_PENDING would mean the
// request was queued, which cannot happen with LOCKFILE_FAIL_IMMEDIATELY).
func lockBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
