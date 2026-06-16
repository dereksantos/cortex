package lineedit

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// ErrInterrupted is returned by ReadLine when the user presses Ctrl-C at the
// prompt: the line is abandoned, but the REPL keeps running.
var ErrInterrupted = errors.New("lineedit: interrupted")

// Terminal owns a TTY in cbreak mode for an interactive session. Construct with
// Open and always Close (it restores the prior terminal state).
type Terminal struct {
	in      *os.File
	out     io.Writer
	fd      int
	old     *termState
	history *History
}

// SetHistory wires the recall list used by ↑/↓ and Ctrl-R. Nil disables it.
func (t *Terminal) SetHistory(h *History) { t.history = h }

// AddHistory records a submitted line for recall. The caller chooses what to
// record (e.g. skipping session-ending meta commands).
func (t *Terminal) AddHistory(s string) { t.history.Add(s) }

// IsInteractive reports whether f is a terminal — the gate for using the line
// editor at all (piped/redirected input falls back to a plain reader).
func IsInteractive(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// Open puts in into cbreak mode and enables bracketed paste. Returns an error
// on non-terminals or unsupported platforms; callers fall back to line-at-a-
// time reading. Close restores the original state.
func Open(in *os.File, out io.Writer) (*Terminal, error) {
	fd := int(in.Fd())
	old, err := getTermios(fd)
	if err != nil {
		return nil, err
	}
	cb := makeCbreak(*old)
	if err := setTermios(fd, &cb); err != nil {
		return nil, err
	}
	t := &Terminal{in: in, out: out, fd: fd, old: old}
	io.WriteString(out, "\x1b[?2004h") // enable bracketed paste
	t.installSignalRestore()
	return t, nil
}

// Close disables bracketed paste and restores the saved terminal state.
func (t *Terminal) Close() error {
	io.WriteString(t.out, "\x1b[?2004l")
	return setTermios(t.fd, t.old)
}

// installSignalRestore guards against a kill leaving the terminal in cbreak:
// on a fatal signal, restore state and exit. Ctrl-C does not arrive here (ISIG
// is off, so it's delivered as the byte 0x03), but an external kill -INT/TERM/
// HUP would otherwise skip Close.
func (t *Terminal) installSignalRestore() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-ch
		t.Close()
		os.Exit(1)
	}()
}

func (t *Terminal) width() int {
	w, _, err := term.GetSize(t.fd)
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// ReadLine renders prompt and edits one line until Enter. Returns io.EOF on
// Ctrl-D at an empty line and ErrInterrupted on Ctrl-C.
func (t *Terminal) ReadLine(prompt string) (string, error) {
	src := newReaderSource(t.fd)
	buf := &buffer{}
	redraw := func() { io.WriteString(t.out, renderLine(prompt, buf, t.width())) }
	redraw()

	// History navigation: hpos indexes into history; at history.Len() means the
	// live draft (preserved so ↓ past the newest restores what was typed).
	hpos := t.history.Len()
	var draft []rune

	for {
		ev, err := decodeKey(src)
		if err != nil {
			if err == io.EOF {
				return "", io.EOF
			}
			return "", err
		}
		switch ev.kind {
		case keyEnter:
			line := buf.string()
			io.WriteString(t.out, "\r\n")
			return line, nil // caller decides what to record (AddHistory)
		case keyUp:
			if hpos == 0 {
				continue
			}
			if hpos == t.history.Len() {
				draft = append([]rune{}, buf.runes...)
			}
			hpos--
			setBuffer(buf, t.history.at(hpos))
		case keyDown:
			if hpos >= t.history.Len() {
				continue
			}
			hpos++
			if hpos == t.history.Len() {
				buf.runes, buf.pos = append([]rune{}, draft...), len(draft)
			} else {
				setBuffer(buf, t.history.at(hpos))
			}
		case keyReverseSearch:
			if t.history.Len() == 0 {
				continue
			}
			res, action := t.reverseSearch(src)
			switch action {
			case rsSubmit:
				io.WriteString(t.out, "\r\n")
				return res, nil
			case rsAccept:
				setBuffer(buf, res)
				hpos = t.history.Len()
			}
		case keyEOF:
			if len(buf.runes) == 0 {
				io.WriteString(t.out, "\r\n")
				return "", io.EOF
			}
			buf.deleteForward() // Ctrl-D mid-line deletes, like readline
		case keyInterrupt:
			io.WriteString(t.out, "^C\r\n")
			return "", ErrInterrupted
		case keyRune:
			buf.insert(ev.r)
		case keyPaste:
			buf.insert([]rune(ev.paste)...)
		case keyBackspace:
			buf.backspace()
		case keyDelete:
			buf.deleteForward()
		case keyLeft:
			buf.left()
		case keyRight:
			buf.right()
		case keyHome:
			buf.home()
		case keyEnd:
			buf.end()
		case keyWordLeft:
			buf.wordLeft()
		case keyWordRight:
			buf.wordRight()
		case keyKillToEnd:
			buf.killToEnd()
		case keyKillToStart:
			buf.killToStart()
		case keyKillWord:
			buf.killWord()
		case keyUnknown, keyAbort:
			continue // no state change → no redraw
		}
		redraw()
	}
}

// setBuffer replaces the line with s and parks the cursor at the end.
func setBuffer(b *buffer, s string) {
	b.runes = []rune(s)
	b.pos = len(b.runes)
}

// reverse-search outcomes.
const (
	rsCancel = iota // restore the pre-search line
	rsAccept        // put the match in the buffer for further editing
	rsSubmit        // accept and submit the match immediately
)

// reverseSearch runs an incremental Ctrl-R history search as a sub-mode reading
// from the same source. Typing refines the query; Ctrl-R steps to the next older
// match; Enter submits; a cursor key accepts the match for editing; Ctrl-G or
// Ctrl-C cancels. Returns the resulting line and the chosen outcome.
func (t *Terminal) reverseSearch(src *readerSource) (string, int) {
	var query []rune
	idx, match, _ := t.history.searchBackward("", t.history.Len())

	render := func() {
		io.WriteString(t.out, "\r\033[K(reverse-i-search)`"+string(query)+"': "+firstLine(match))
	}
	research := func(from int) {
		if i, m, ok := t.history.searchBackward(string(query), from); ok {
			idx, match = i, m
		} else {
			match = ""
		}
	}
	render()

	for {
		ev, err := decodeKey(src)
		if err != nil {
			return "", rsCancel
		}
		switch ev.kind {
		case keyEnter:
			if match == "" {
				return "", rsCancel
			}
			return match, rsSubmit
		case keyInterrupt, keyAbort:
			return "", rsCancel
		case keyReverseSearch:
			research(idx) // step to the next older match
		case keyRune:
			query = append(query, ev.r)
			research(t.history.Len())
		case keyBackspace:
			if len(query) > 0 {
				query = query[:len(query)-1]
			}
			research(t.history.Len())
		case keyLeft, keyRight, keyHome, keyEnd, keyWordLeft, keyWordRight:
			if match == "" {
				return "", rsCancel
			}
			return match, rsAccept
		}
		render()
	}
}

// firstLine returns s up to its first newline — multi-line history entries show
// as one line in the search prompt.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Interruptible returns a context cancelled when the user presses ESC or Ctrl-C
// while a turn runs, plus a stop func to call once the turn finishes. The
// watcher reads the fd directly; because cbreak uses VTIME, each read returns
// within ~0.1s, so stop() can signal the goroutine and it exits at the next
// read boundary — no concurrent reads with the next ReadLine.
func (t *Terminal) Interruptible(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 64)
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			n, err := rawRead(t.fd, b)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				return
			}
			for _, c := range b[:n] {
				if c == 0x1b || c == 0x03 { // ESC or Ctrl-C
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() {
		close(stopCh)
		<-done
		cancel()
	}
}

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

// readerSource yields bytes from the fd one at a time with a refill buffer, so
// paste bursts don't cost a syscall per byte. Zero-byte VTIME timeouts are
// retried, so next() blocks until a real byte arrives.
type readerSource struct {
	fd   int
	buf  [4096]byte
	n, i int
}

func newReaderSource(fd int) *readerSource { return &readerSource{fd: fd} }

func (s *readerSource) next() (byte, error) {
	for s.i >= s.n {
		n, err := rawRead(s.fd, s.buf[:])
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue // VTIME timeout, no input yet — keep waiting
		}
		s.n, s.i = n, 0
	}
	b := s.buf[s.i]
	s.i++
	return b, nil
}
