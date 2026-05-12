//go:build windows

package journal

// acquireExclusiveLock is a no-op on Windows for v1. Cortex's capture hooks
// are primarily tested on macOS/Linux; a Windows implementation would use
// LockFileEx via golang.org/x/sys/windows. Worst case without locking: two
// concurrent appends interleave one corrupted line which the reader skips.
func acquireExclusiveLock(fd int) error {
	return nil
}
