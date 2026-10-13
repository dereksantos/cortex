//go:build windows

package fslock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh places the locked byte at offset 2^62, far past the end of
// any real file. Windows byte-range locks are mandatory: locking the file's
// own bytes would make other processes' reads of them fail. A lock past EOF
// still excludes a second locker, which is all the flock-style guard needs.
const lockOffsetHigh = 1 << 30

// flock places (or, when nonblock is true, attempts a non-blocking) exclusive
// lock on the handle. A non-blocking attempt that would have to wait returns
// fslock.ErrBusy. Windows releases the lock when the handle is closed.
func flock(fd int, nonblock bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblock {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	if err := windows.LockFileEx(windows.Handle(fd), flags, 0, 1, 0, ol); err != nil {
		if nonblock && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrBusy
		}
		return err
	}
	return nil
}
