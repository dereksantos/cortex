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
// Layout is at most two rows: an optional status row ("thinking... 3s") directly
// above the input row. Both the input (single-row, horizontally scrolled by
// renderLine) and the status are one terminal row each, so erasing the block is
// a fixed, wrap-free cursor move.
type Anchor struct {
	out     io.Writer
	src     *readerSource
	widthFn func() int
	term    *Terminal // owner, so Stop can clear itself from Terminal.leaseInput

	mu     sync.Mutex
	prompt string
	buf    *buffer
	status string // rendered status row; "" hides it
	rows   int    // rows the pinned block currently occupies on screen (0,1,2)

	cancel context.CancelFunc // cancels the turn ctx on ESC / Ctrl-C

	activity string // status-row label; "" hides the row

	confirm *confirmState // in-flight y/N question, served by the key loop
	susp    *suspendState // in-flight inspector lease, served by the key loop

	stop chan struct{}
	done chan struct{} // closed when both the key loop and ticker have exited
}

// dim wraps s in the bright-black SGR so the status row reads as transient
// metadata. lineedit keeps its own copy rather than importing the cmd/cortex
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
		term:    t,
		prompt:  prompt,
		buf:     &buffer{},
		cancel:  cancel,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if seed != "" {
		setBuffer(a.buf, seed)
	}
	t.setAnchor(a)
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

// confirmState is an in-flight yes/no question. The anchor's key loop already
// owns the terminal during a turn, so a confirmation must be served from THAT
// loop — opening a second reader (Terminal.ReadLine) makes the two fight for
// each keystroke and the answer never lands. ask sits on the status row; the
// resolved answer is delivered on res.
type confirmState struct {
	ask string
	res chan bool
}

// Confirm pauses live editing to ask a yes/no question, reading the answer
// through the key loop that already owns the terminal (never a competing
// reader). The question's context lines are emitted into scrollback; only the
// final "run it? [y/N]" line sits on the status row above the prompt. Returns
// true only on an explicit y/Y; n/N/Enter decline, Ctrl-C declines and cancels
// the turn. Safe to call from the turn goroutine while the key loop runs.
func (a *Anchor) Confirm(question string) bool {
	body, ask := splitConfirm(question)
	for _, line := range body {
		a.EmitLine(line)
	}
	res := make(chan bool, 1)
	a.mu.Lock()
	a.confirm = &confirmState{ask: ask, res: res}
	a.eraseLocked()
	a.drawLocked()
	a.mu.Unlock()
	select {
	case <-a.stop: // turn ended without an answer → treat as declined
		return false
	case v := <-res:
		return v
	}
}

// splitConfirm separates a multi-line confirm prompt into the context lines
// (shown in scrollback) and the final ask (shown on the status row). The input
// is the gateShell question: a blank lead, a "risky: ..." line, the command,
// then "run it? [y/N]".
func splitConfirm(q string) (body []string, ask string) {
	lines := strings.Split(strings.Trim(q, "\n"), "\n")
	ask = strings.TrimSpace(lines[len(lines)-1])
	for _, l := range lines[:len(lines)-1] {
		if strings.TrimSpace(l) != "" {
			body = append(body, strings.TrimRight(l, " "))
		}
	}
	return body, ask
}

// handleConfirmByte folds one key into an in-flight confirmation. Only y/N,
// Enter, and Ctrl-C are meaningful; any other key is ignored so a stray
// keystroke can't be misread as an answer.
func (a *Anchor) handleConfirmByte(b byte) {
	var answer, cancel bool
	switch b {
	case 'y', 'Y':
		answer = true
	case 'n', 'N', '\r', '\n':
		answer = false
	case 0x03: // Ctrl-C: decline this command and cancel the turn
		answer, cancel = false, true
	default:
		return // not an answer — keep waiting
	}
	a.mu.Lock()
	c := a.confirm
	a.confirm = nil
	if c != nil {
		a.eraseLocked()
		a.drawLocked()
	}
	a.mu.Unlock()
	if c == nil {
		return
	}
	if cancel && a.cancel != nil {
		a.cancel()
	}
	c.res <- answer
}

// suspendState is an in-flight inspector lease. The anchor's key loop already
// owns the terminal during a turn, so an inspector must be fed from THAT loop
// for the same reason a confirmation must (see confirmState): a second reader
// on the fd makes the two fight for each keystroke. Unlike a confirmation,
// though, an inspector also takes the *screen* — so while a lease is out the
// anchor stops drawing entirely and parks its output in pending, which resume
// flushes into scrollback once the alternate screen is gone.
type suspendState struct {
	keys    chan byte
	pending []string
}

// Suspend parks the anchor for the duration of an inspector and returns the key
// source the inspector should read plus the func that restores the prompt. The
// pinned block is erased immediately (so the primary screen is clean before the
// alternate screen goes up and clean again when it comes down), drawing is
// suppressed, and the key loop forwards raw bytes to the returned source.
//
// Raw bytes, not decoded events: the inspector does its own escape decoding
// (including the bare-ESC timeout), so bytes must pass through in order and
// uninterpreted. The key loop only ever hands handleByte the *first* byte of a
// keystroke, but since a suspended handleByte returns immediately, the loop
// comes straight back for the continuation bytes and those flow through too.
func (a *Anchor) Suspend() (pollSource, func()) {
	a.mu.Lock()
	if a.susp != nil { // already leased — hand back an inert source
		a.mu.Unlock()
		return &suspendedKeys{}, func() {}
	}
	s := &suspendState{keys: make(chan byte, 256)}
	a.eraseLocked() // must run before susp is set; drawing is suppressed after
	a.susp = s
	a.mu.Unlock()
	return &suspendedKeys{s: s, stop: a.stop}, a.resume
}

// resume ends an inspector lease: the output held back while the alternate
// screen was up is flushed into scrollback in arrival order, then the pinned
// prompt is redrawn exactly as it was.
func (a *Anchor) resume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.susp
	a.susp = nil
	if s == nil {
		return
	}
	for _, line := range s.pending {
		io.WriteString(a.out, line+"\r\n")
	}
	a.drawLocked()
}

// suspendedKeys is the pollSource an inspector reads while the anchor is
// parked: bytes arrive from the anchor's key loop over a channel, and the
// timeout that would come from cbreak's VTIME is supplied here instead so the
// inspector's idle repaint and bare-ESC detection work identically.
type suspendedKeys struct {
	s    *suspendState
	stop chan struct{}
}

func (k *suspendedKeys) next() (byte, error) {
	for {
		b, timedOut, err := k.firstByte()
		if err != nil {
			return 0, err
		}
		if !timedOut {
			return b, nil
		}
	}
}

func (k *suspendedKeys) firstByte() (byte, bool, error) {
	if k.s == nil {
		return 0, false, io.EOF
	}
	select {
	case b := <-k.s.keys:
		return b, false, nil
	case <-k.stop: // the turn ended under us — close the inspector
		return 0, false, io.EOF
	case <-time.After(inspectPoll):
		return 0, true, nil
	}
}

// EmitLine prints one line of turn output above the pinned prompt, then redraws
// the prompt beneath it. s should not contain a trailing newline (the pipe
// reader splits on newlines); embedded ANSI is fine.
func (a *Anchor) EmitLine(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.susp != nil {
		// An inspector holds the alternate screen. Writing here would paint over
		// it and the line would never reach scrollback; hold it for resume.
		a.susp.pending = append(a.susp.pending, s)
		return
	}
	a.eraseLocked()
	io.WriteString(a.out, s+"\r\n")
	a.drawLocked()
}

// SetThinking drives the status row while the model generates. on=true shows
// "thinking..." with the latest reasoning tail; on=false clears it.
func (a *Anchor) SetThinking(on bool, tail string) {
	label := ""
	if on {
		label = "thinking..."
		if tail != "" {
			label += " " + tail
		}
	}
	a.SetActivity(label)
}

// SetActivity shows a status row labeled label (e.g. a running tool like
// "study(main.go)"); "" hides the row. The tick loop repaints the row on a
// fixed cadence while a label is set — there's no glyph to animate, so a
// caller's own elapsed-seconds text (SetThinking's tail) is what moves.
// Safe from any goroutine.
func (a *Anchor) SetActivity(label string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activity = label
	a.refreshStatusLocked()
}

// SetPrompt updates the prompt text and redraws the anchor. This allows the
// prompt to reflect live changes, such as an updated context gauge.
// Safe from any goroutine.
func (a *Anchor) SetPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prompt = prompt
	// Every other redraw path in this file pairs erase+draw; this one didn't,
	// which was invisible while its only callers ran with the status row
	// already hidden (a redraw-without-erase onto a 1-row block just
	// overwrites itself). It stops being invisible the moment a caller
	// invokes SetPrompt while the status row is live (2-row block): drawing
	// from the cursor's current position — parked on the input row, the
	// bottom of the block — rather than the block's top pushes a fresh line
	// onto the terminal instead of overwriting it.
	a.eraseLocked()
	a.drawLocked()
}

// Stop halts the editor goroutines, erases the pinned block, and returns the
// current line so the caller can seed the next prompt with it.
func (a *Anchor) Stop() string {
	a.term.setAnchor(nil)
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
	a.mu.Lock()
	susp := a.susp
	confirming := a.confirm != nil
	a.mu.Unlock()
	if susp != nil {
		// An inspector is up: this loop stays the fd's only reader and forwards
		// the byte verbatim (see Suspend). A full buffer means the inspector has
		// stopped draining, so the byte is dropped rather than blocking the loop.
		select {
		case susp.keys <- b:
		default:
		}
		return false
	}
	if confirming {
		a.handleConfirmByte(b)
		return false
	}
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
		return // Enter, Up/Down, PgUp/PgDn, Ctrl-R, unknown — no live change
	}
	a.refreshInputLocked()
}

// tickLoop repaints the status row on a fixed cadence while an activity is
// set, so an external caller's own elapsed-seconds label update (SetThinking)
// shows up promptly even between explicit SetActivity calls.
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
				a.refreshStatusLocked()
			}
			a.mu.Unlock()
		}
	}
}

// refreshStatusLocked recomputes the status row text and redraws the block.
// No glyph to cycle — the caller's own label text (e.g. "thinking... 3s") is
// the only thing that changes between ticks.
func (a *Anchor) refreshStatusLocked() {
	if a.activity != "" {
		a.status = dim(a.activity)
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
// block now occupies. Inert while an inspector holds the screen (Suspend):
// every redraw path funnels through here, so one guard covers the tick loop,
// status updates, and buffer edits alike.
func (a *Anchor) drawLocked() {
	if a.susp != nil {
		return
	}
	width := a.widthFn()
	var b strings.Builder
	rows := 1
	// A pending confirmation takes the status row, rendered bright (not dimmed)
	// so the ask stands out from a transient activity label.
	status := a.status
	if a.confirm != nil {
		status = a.confirm.ask
	}
	if status != "" {
		b.WriteString("\r\033[K")
		b.WriteString(truncate(status, width))
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
