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
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, fmt.Errorf("fslock: open %s: %w", path, err)
	}
	if err := flock(int(f.Fd()), true); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fslock: lock %s: %w", path, err)
	}
	return f, nil
}

// Lock takes a blocking exclusive lock on an already-open file descriptor.
// It is used where the file is opened separately (the journal opens a segment
// before locking it). The lock is released when that descriptor is closed.
func Lock(fd int) error {
	if err := flock(fd, false); err != nil {
		return fmt.Errorf("fslock: lock fd %d: %w", fd, err)
	}
	return nil
}
