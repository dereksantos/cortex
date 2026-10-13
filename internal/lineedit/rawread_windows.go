//go:build windows

package lineedit

import "syscall"

// rawRead reads from the handle via the raw syscall. On Windows Open fails
// (termios_stub.go) and callers read line-at-a-time instead, so this exists
// for the build; it has no VTIME timeout to poll through.
func rawRead(fd int, b []byte) (int, error) {
	return syscall.Read(syscall.Handle(fd), b)
}
