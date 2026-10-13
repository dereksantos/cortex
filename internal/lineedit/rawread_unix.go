//go:build !windows

package lineedit

import "syscall"

// rawRead reads from the fd via the raw syscall, which (unlike os.File.Read)
// returns (0, nil) on a VTIME timeout instead of treating a zero-byte read as
// EOF — letting callers poll without a spurious EOF every 0.1s.
func rawRead(fd int, b []byte) (int, error) {
	for {
		n, err := syscall.Read(fd, b)
		if err == syscall.EINTR {
			continue
		}
		return n, err
	}
}
