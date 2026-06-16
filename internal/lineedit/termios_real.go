//go:build darwin || linux

package lineedit

import "golang.org/x/sys/unix"

// termState is the saved terminal attributes to restore on Close.
type termState = unix.Termios

func getTermios(fd int) (*termState, error) { return unix.IoctlGetTermios(fd, ioctlGet) }

func setTermios(fd int, st *termState) error { return unix.IoctlSetTermios(fd, ioctlSet, st) }

// makeCbreak returns a copy of st in cbreak mode: byte-at-a-time, no echo, no
// signal generation (we read Ctrl-C as a byte), no input CR/flow mangling — but
// OPOST is deliberately left ON so the harness's "\n" output still maps to
// "\r\n". VMIN=0/VTIME=1 makes a read return after at most 0.1s even with no
// input, so the interrupt watcher can poll a stop flag and exit cleanly between
// reads (no cancelable-reader machinery needed).
func makeCbreak(st termState) termState {
	st.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	st.Iflag &^= unix.IXON | unix.ICRNL | unix.BRKINT | unix.INPCK | unix.ISTRIP
	st.Cc[unix.VMIN] = 0
	st.Cc[unix.VTIME] = 1
	return st
}
