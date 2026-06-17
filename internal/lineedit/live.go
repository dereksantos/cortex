package lineedit

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"
)

// Anchor pins a one-row editable prompt to the bottom of the terminal and keeps
// it there while output streams above it. The line editor runs in a background
// goroutine, so keystrokes echo live even while the caller's main thread is busy
// producing output. That output must be funneled through EmitLine (the REPL
// redirects os.Stdout into a pipe whose lines feed it) so it lands above the
// pinned row; input and status redraws write straight to the real terminal,
// which is why Anchor keeps its own out handle rather than touching os.Stdout.
//
// Layout is at most two rows: an optional status row ("⠹ thinking…") directly
// above the input row. Both the input (single-row, horizontally scrolled by
// renderLine) and the status are one terminal row each, so erasing the block is
// a fixed, wrap-free cursor move.
type Anchor struct {
	out     io.Writer
	src     *readerSource
	widthFn func() int

	mu     sync.Mutex
	prompt string
	buf    *buffer
	status string // rendered status row; "" hides it
	rows   int    // rows the pinned block currently occupies on screen (0,1,2)

	cancel context.CancelFunc // cancels the turn ctx on ESC / Ctrl-C

	activity string // label shown after the spinner glyph; "" hides the status row
	spinIdx  int

	stop chan struct{}
	done chan struct{} // closed when both the key loop and ticker have exited
}

// anchorSpinner is the status-row glyph cycle (matches the standalone Spinner).
var anchorSpinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// dim wraps s in the bright-black SGR so the status row reads as transient
// metadata. lineedit keeps its own copy rather than importing the cmd/loop
// palette (that would invert the dependency).
const (
	ansiDim   = "\033[90m"
	ansiReset = "\033[0m"
)

func dim(s string) string { return ansiDim + s + ansiReset }

// Anchor pins an editable prompt seeded with seed and returns it plus a context
// cancelled when the user hits ESC or Ctrl-C. Start the turn, route its output
// through EmitLine, then call Stop to retrieve the (possibly edited) line.
func (t *Terminal) Anchor(prompt, seed string) (*Anchor, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Anchor{
		out:     t.out,
		src:     newReaderSource(t.fd),
		widthFn: t.width,
		prompt:  prompt,
		buf:     &buffer{},
		cancel:  cancel,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if seed != "" {
		setBuffer(a.buf, seed)
	}
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.keyLoop() }()
	go func() { defer wg.Done(); a.tickLoop() }()
	go func() { wg.Wait(); close(a.done) }()
	return a, ctx
}

// Width is the anchor's current terminal column count — the source of truth for
// output word-wrap while os.Stdout is redirected away from the terminal.
func (a *Anchor) Width() int { return a.widthFn() }

// EmitLine prints one line of turn output above the pinned prompt, then redraws
// the prompt beneath it. s should not contain a trailing newline (the pipe
// reader splits on newlines); embedded ANSI is fine.
func (a *Anchor) EmitLine(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eraseLocked()
	io.WriteString(a.out, s+"\r\n")
	a.drawLocked()
}

// SetThinking drives the status row while the model generates. on=true shows a
// spinning "thinking…" with the latest reasoning tail; on=false clears it.
func (a *Anchor) SetThinking(on bool, tail string) {
	label := ""
	if on {
		label = "thinking…"
		if tail != "" {
			label += " " + tail
		}
	}
	a.SetActivity(label)
}

// SetActivity shows a spinning status row labeled label (e.g. a running tool
// like "study(main.go)"); "" hides the row. The tick loop animates the glyph
// while a label is set. Safe from any goroutine.
func (a *Anchor) SetActivity(label string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activity = label
	a.refreshStatusLocked()
}

// Stop halts the editor goroutines, erases the pinned block, and returns the
// current line so the caller can seed the next prompt with it.
func (a *Anchor) Stop() string {
	close(a.stop)
	<-a.done
	a.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eraseLocked()
	a.status, a.activity = "", ""
	return a.buf.string()
}

// keyLoop reads keystrokes and edits the pinned line live until Stop. The first
// byte of each key is read with a VTIME-bounded poll so the loop notices stop
// promptly; continuation bytes use the blocking source.
func (a *Anchor) keyLoop() {
	for {
		select {
		case <-a.stop:
			return
		default:
		}
		b, timedOut, err := a.src.firstByte()
		if err != nil {
			return
		}
		if timedOut {
			continue
		}
		if a.handleByte(b) {
			return // interrupt requested
		}
	}
}

// handleByte folds one first-byte into the buffer, redrawing on change. It
// returns true when the user asked to interrupt (ESC or Ctrl-C), which cancels
// the turn but leaves the editor running so they can keep typing. A lone ESC is
// distinguished from an arrow-key escape sequence by a follow-up poll: a real
// sequence's bytes arrive in the same burst, so a timeout means a bare ESC.
func (a *Anchor) handleByte(b byte) (interrupt bool) {
	if b == 0x1b {
		nb, timedOut, err := a.src.firstByte()
		if err != nil {
			return false
		}
		if timedOut {
			a.cancel() // bare ESC → interrupt
			return false
		}
		ev, err := decodeEscape(&pushback{b: nb, src: a.src})
		if err != nil {
			return false
		}
		a.applyEvent(ev)
		return false
	}
	if b == 0x03 { // Ctrl-C
		a.cancel()
		return false
	}
	ev, err := decodeKeyByte(b, a.src)
	if err != nil {
		return false
	}
	a.applyEvent(ev)
	return false
}

// applyEvent edits the buffer for one decoded key and redraws. Submission keys
// are intentionally inert here: Enter and history navigation belong to the
// foreground ReadLine that resumes once the turn ends. This loop only echoes
// the user's in-progress next message.
func (a *Anchor) applyEvent(ev keyEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch ev.kind {
	case keyRune:
		a.buf.insert(ev.r)
	case keyPaste:
		a.buf.insert([]rune(ev.paste)...)
	case keyBackspace:
		a.buf.backspace()
	case keyDelete:
		a.buf.deleteForward()
	case keyLeft:
		a.buf.left()
	case keyRight:
		a.buf.right()
	case keyHome:
		a.buf.home()
	case keyEnd:
		a.buf.end()
	case keyWordLeft:
		a.buf.wordLeft()
	case keyWordRight:
		a.buf.wordRight()
	case keyKillToEnd:
		a.buf.killToEnd()
	case keyKillToStart:
		a.buf.killToStart()
	case keyKillWord:
		a.buf.killWord()
	default:
		return // Enter, Up/Down, Ctrl-R, unknown — no live change
	}
	a.refreshInputLocked()
}

// tickLoop animates the status spinner while an activity is set.
func (a *Anchor) tickLoop() {
	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			a.mu.Lock()
			if a.activity != "" {
				a.spinIdx++
				a.refreshStatusLocked()
			}
			a.mu.Unlock()
		}
	}
}

// refreshStatusLocked recomputes the status row text and redraws the block.
func (a *Anchor) refreshStatusLocked() {
	if a.activity != "" {
		glyph := string(anchorSpinner[a.spinIdx%len(anchorSpinner)])
		a.status = dim(glyph + " " + a.activity)
	} else {
		a.status = ""
	}
	a.eraseLocked()
	a.drawLocked()
}

// refreshInputLocked redraws the block in place after a buffer edit.
func (a *Anchor) refreshInputLocked() {
	a.eraseLocked()
	a.drawLocked()
}

// eraseLocked clears the pinned block, leaving the cursor at its top-left. It
// assumes the cursor is currently parked on the input row (the post-draw
// invariant), so it steps up over the status row when one is shown.
func (a *Anchor) eraseLocked() {
	if a.rows == 0 {
		return
	}
	if a.rows == 2 {
		io.WriteString(a.out, "\033[1A")
	}
	io.WriteString(a.out, "\r\033[J")
	a.rows = 0
}

// drawLocked renders the status row (if any) and the input row, parking the
// cursor on the input row at the edit column, and records how many rows the
// block now occupies.
func (a *Anchor) drawLocked() {
	width := a.widthFn()
	var b strings.Builder
	rows := 1
	if a.status != "" {
		b.WriteString("\r\033[K")
		b.WriteString(truncate(a.status, width))
		b.WriteString("\r\n")
		rows = 2
	}
	b.WriteString(renderLine(a.prompt, a.buf, width))
	io.WriteString(a.out, b.String())
	a.rows = rows
}

// pushback is a byteSource that yields one already-read byte before delegating
// to src — used to feed decodeEscape the byte that followed an ESC.
type pushback struct {
	b    byte
	used bool
	src  byteSource
}

func (p *pushback) next() (byte, error) {
	if !p.used {
		p.used = true
		return p.b, nil
	}
	return p.src.next()
}
