//go:build windows

package fslock

// flock is a no-op on Windows for v1. Cortex's capture hooks and session
// surface are primarily exercised on macOS/Linux; a Windows implementation
// would use LockFileEx via golang.org/x/sys/windows. Worst case without
// locking: two concurrent writers interleave a line which the reader skips.
func flock(fd int, nonblock bool) error {
	return nil
}
