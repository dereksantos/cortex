//go:build !windows

package journal

import "syscall"

// acquireExclusiveLock places an advisory exclusive lock on the file
// descriptor. The lock is released automatically when the fd is closed
// (so Writer.Close() and Writer.openSegment()'s old-handle close release
// it without an explicit unlock call).
//
// This protects against cross-process append interleaving — capture is
// invoked per-hook as separate processes, and without this lock two
// concurrent invocations could produce a corrupted segment.
func acquireExclusiveLock(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_EX)
}
