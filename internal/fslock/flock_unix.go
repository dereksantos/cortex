//go:build !windows

package fslock

import "syscall"

// flock places (or, when nonblock is true, attempts a non-blocking) exclusive
// advisory lock on the descriptor. A non-blocking attempt that would have to
// wait returns fslock.ErrBusy.
func flock(fd int, nonblock bool) error {
	op := syscall.LOCK_EX
	if nonblock {
		op |= syscall.LOCK_NB
	}
	if err := syscall.Flock(fd, op); err != nil {
		if nonblock && err == syscall.EWOULDBLOCK {
			return ErrBusy
		}
		return err
	}
	return nil
}
