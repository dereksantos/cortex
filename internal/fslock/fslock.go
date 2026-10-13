// Package fslock provides exclusive advisory file locking that is portable
// across the platforms Cortex supports. It is the single home for the
// flock-style guard that serializes the journal's segment appends and now
// also guards the REPL's session transcript against a second process (e.g.
// `cortex discord`) opening the same file.
//
// The lock is advisory and released automatically when the underlying file
// descriptor is closed, so callers must keep the *os.File open and close it
// (via (*os.File).Close) to release the lock.
package fslock

import (
	"errors"
	"fmt"
	"os"
)

// ErrBusy is returned (wrapped) when an exclusive lock cannot be acquired
// because another process already holds it. Callers can test with
// errors.Is(err, fslock.ErrBusy).
var ErrBusy = errors.New("file is locked by another process")

// OpenExclusive opens (creating if needed) the file at path with the given
// flag and perm, then takes a non-blocking exclusive lock on its descriptor.
// If another process already holds the lock, it returns an error wrapping
// fslock.ErrBusy instead of blocking.
//
// The lock is released when the returned *os.File is closed.
func OpenExclusive(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, lockableFlag(flag), perm)
	if err != nil {
		return nil, fmt.Errorf("fslock: open %s: %w", path, err)
	}
	if err := flock(int(f.Fd()), true); err != nil {
		// Best-effort close on the failed-lock path: the lock error is
		// already returned below, and a Close failure here doesn't change
		// that outcome — the fd is reclaimed at process exit regardless.
		_ = f.Close()
		return nil, fmt.Errorf("fslock: lock %s: %w", path, err)
	}
	return f, nil
}

// Lock takes a blocking exclusive lock on an already-open file descriptor.
// It is used where the file is opened separately (the journal opens a segment
// before locking it). The lock is released when that descriptor is closed.
//
// Open the file O_RDWR, not O_WRONLY: with O_APPEND, Windows opens an
// append-only handle, and LockFileEx refuses to lock one ("Access is
// denied"). Unix doesn't care, so the same flags work everywhere.
func Lock(fd int) error {
	if err := flock(fd, false); err != nil {
		return fmt.Errorf("fslock: lock fd %d: %w", fd, err)
	}
	return nil
}

// lockableFlag turns a write-only open into read-write so the handle can be
// locked on Windows (see Lock). Read access changes nothing else: callers
// still only write.
func lockableFlag(flag int) int {
	if flag&(os.O_WRONLY|os.O_RDWR) == os.O_WRONLY {
		return flag&^os.O_WRONLY | os.O_RDWR
	}
	return flag
}
