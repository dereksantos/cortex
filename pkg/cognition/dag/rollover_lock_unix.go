//go:build !windows

package dag

import "syscall"

// acquireExclusiveLock places a POSIX advisory exclusive lock on the
// file descriptor. Released when the fd closes. Same pattern as
// internal/journal/lock_unix.go — protects against cross-process
// append interleaving on the deferred-spawn queue.
func acquireExclusiveLock(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_EX)
}
