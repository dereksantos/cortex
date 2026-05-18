//go:build windows

package dag

// acquireExclusiveLock is a no-op on Windows for v1, matching the
// internal/journal pattern. Cross-process correctness on Windows
// would use LockFileEx via golang.org/x/sys/windows; cortex's primary
// platforms are macOS/Linux and the queue tolerates a corrupted line
// from interleaved writes (ReadAndConsume skips unparseable lines).
func acquireExclusiveLock(fd int) error {
	return nil
}
