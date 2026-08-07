// inspect.go — the alternate-screen "inspector": an on-demand full-screen,
// scrollable view for the few surfaces that genuinely want a whole screen
// (/context today; /sessions and a memory browser later).
//
// The REPL is scrollback-native by decision: output goes to the normal
// terminal buffer so copy-paste, search, and piping all keep working. An
// inspector is the deliberate escape hatch from that — it borrows the
// terminal for as long as the user looks at it, then hands the scrollback
// back byte-for-byte. That restoration is the whole contract, and it is why
// this lives in lineedit rather than in a package of its own: entering the
// alternate screen, owning the keystroke stream, and putting the terminal
// back are the same three responsibilities Terminal already has (termios,
// bracketed paste, signal-restore), and splitting them across packages would
// mean two owners of one fd.
//
// Restoration rests on the standard xterm private mode 1049 pair: 1049h saves
// the cursor and switches to a scratch buffer, 1049l switches back and
// restores the cursor. The primary buffer is never written to in between (the
// cursor is hidden and every frame is addressed inside the alternate screen),
// so the user's history is not merely redrawn — it is untouched. Terminal.Close
// leaves the alternate screen first, so the existing fatal-signal handler
// (installSignalRestore) also unwinds a live inspector rather than stranding
// the user on the scratch buffer.
//
// Plain text only, per the 2026-07-19 no-glyph decision: the chrome here is a
// dim title row and a dim footer row. Color, spacing, and alignment carry the
// structure. A View's own body is passed through verbatim — the /context grid's
// cells are information-bearing (a map of the window), not decoration, and the
// harness must not second-guess them.
package lineedit

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// View is one inspector page: a title for the top row and a body the harness
// scrolls. Implementations are pull-based and must be cheap enough to call on
// every repaint — the harness re-pulls Lines each frame so the view stays live
// (a /context map reflects the session as it is now, not as it was when the
// user opened it).
//
// width is the current terminal column count, offered so a view can reflow.
// Views with a fixed frame (the /context grid is always 8x16 by design) are
// free to ignore it; the harness clamps any over-long line to the terminal
// width rather than letting it wrap and shear the layout.
//
// Adopting the harness is exactly this: implement Title and Lines, then call
// Terminal.Inspect(view). Scrolling, paging, resize, key handling, and the
// enter/restore dance are the harness's job, not the view's.
type View interface {
	Title() string
	Lines(width int) []string
}

// The terminal control strings the harness owns. 1049h/1049l is the
// alternate-screen pair (see the package comment); 25l/25h hide and show the
// cursor so a parked cursor never blinks over a cell of the view.
const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
)

// inspectPoll bounds how long an idle inspector waits for a keystroke before
// looping. It matches the cbreak VTIME tick (0.1s) the rest of the package
// polls on, and it is what makes resize handling signal-free: every tick the
// loop re-reads the terminal size and repaints if the frame changed. A SIGWINCH
// handler would buy ~50ms of latency at the cost of a second owner of the
// terminal's state, which this package deliberately avoids.
const inspectPoll = 100 * time.Millisecond

// inspectChrome is the number of rows the harness reserves for its own title
// and footer; the rest of the screen is the view's body.
const inspectChrome = 2

// pollSource is a byteSource that can also report "nothing arrived this tick".
// The inspector needs the distinction twice: to repaint while idle (resize),
// and to tell a bare ESC (quit) from the ESC that opens an arrow-key sequence —
// the same disambiguation Anchor.handleByte makes, by the same means.
type pollSource interface {
	byteSource
	firstByte() (b byte, timedOut bool, err error)
}

// inspectAction is the harness's key vocabulary — deliberately tiny. A view
// does not get to bind keys; every inspector scrolls the same way, so muscle
// memory carries from /context to whatever adopts this next.
type inspectAction int

const (
	inspectNone inspectAction = iota
	inspectQuit
	inspectUp
	inspectDown
	inspectPageUp
	inspectPageDown
	inspectTop
	inspectBottom
)

// inspectRun is one open inspector. top is the body index shown on the first
// body row; viewH and bodyLen are carried from the last render so a paging key
// can be applied before the next one recomputes them.
type inspectRun struct {
	out    io.Writer
	src    pollSource
	sizeFn func() (cols, rows int)
	view   View

	top     int
	viewH   int
	bodyLen int
	last    string // last frame written, so an unchanged frame costs no bytes
}

// runInspect drives one inspector to completion, returning when the user quits
// (q / ESC / Ctrl-C / Ctrl-D) or the input stream ends.
//
// A panic in the view is recovered and returned as an error rather than
// unwound: the caller's deferred restore would put the terminal back either
// way, but a REPL should survive a broken inspector page instead of dying with
// it. The panic value is preserved in the error so the bug is still visible.
func runInspect(out io.Writer, src pollSource, sizeFn func() (int, int), v View) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("failed to run inspector view %T: panic: %v", v, r)
		}
	}()
	r := &inspectRun{out: out, src: src, sizeFn: sizeFn, view: v}
	return r.loop()
}

// loop paints the first frame, then folds keystrokes until the user leaves. An
// idle tick repaints (picking up a resize or fresher view content); the frame
// cache in render makes that free when nothing actually moved.
func (r *inspectRun) loop() error {
	r.render()
	for {
		b, timedOut, err := r.src.firstByte()
		if err != nil {
			if err == io.EOF {
				return nil // input stream ended — treat as a quit, not a failure
			}
			return fmt.Errorf("failed to read inspector input: %w", err)
		}
		if timedOut {
			r.render()
			continue
		}
		if r.decode(b) == inspectQuit {
			return nil
		}
		r.render()
	}
}

// decode maps one first-byte to an action and applies it. Returns inspectQuit
// when the user asked to leave. Escape sequences are decoded through the
// package's shared decoder (keys.go), so arrows/PgUp/PgDn/Home/End behave
// identically here and at the prompt.
func (r *inspectRun) decode(b byte) inspectAction {
	act := inspectNone
	switch b {
	case 'q', 'Q', 0x03, 0x04: // q, Ctrl-C, Ctrl-D
		return inspectQuit
	case 'j':
		act = inspectDown
	case 'k':
		act = inspectUp
	case ' ':
		act = inspectPageDown
	case 'b':
		act = inspectPageUp
	case 'g':
		act = inspectTop
	case 'G':
		act = inspectBottom
	case 0x1b:
		// A bare ESC quits; an ESC that starts a sequence does not. The bytes of
		// a real sequence arrive in the same burst, so a timeout here means the
		// user pressed ESC on its own (Anchor.handleByte draws the same line).
		nb, timedOut, err := r.src.firstByte()
		if err != nil || timedOut {
			return inspectQuit
		}
		ev, err := decodeEscape(&pushback{b: nb, src: r.src})
		if err != nil {
			return inspectNone
		}
		act = actionForKey(ev.kind)
	}
	r.apply(act)
	return act
}

// actionForKey maps a decoded cursor key to a scroll action; anything else is
// inert (an inspector is read-only, so unbound keys must do nothing rather
// than guess).
func actionForKey(k keyKind) inspectAction {
	switch k {
	case keyUp:
		return inspectUp
	case keyDown:
		return inspectDown
	case keyPageUp:
		return inspectPageUp
	case keyPageDown:
		return inspectPageDown
	case keyHome:
		return inspectTop
	case keyEnd:
		return inspectBottom
	}
	return inspectNone
}

// apply moves the scroll offset. Clamping is left to render, which is the only
// place that knows the current body length and viewport height — so a resize
// between two keys can never leave top stranded past the end.
func (r *inspectRun) apply(a inspectAction) {
	page := r.viewH - 1 // keep one line of overlap so context carries across a page
	if page < 1 {
		page = 1
	}
	switch a {
	case inspectUp:
		r.top--
	case inspectDown:
		r.top++
	case inspectPageUp:
		r.top -= page
	case inspectPageDown:
		r.top += page
	case inspectTop:
		r.top = 0
	case inspectBottom:
		r.top = r.bodyLen // clamped down to the last full page by render
	}
}

// render pulls the view, composes a full frame, and writes it only if it
// differs from the one already on screen. The compare is what makes the 10Hz
// idle tick cheap: a static view costs zero bytes per tick, while a resize or
// a changed figure repaints within one tick.
func (r *inspectRun) render() {
	cols, rows := r.sizeFn()
	if cols < 1 {
		cols = 80
	}
	if rows < inspectChrome+1 {
		rows = inspectChrome + 1 // always leave one body row
	}
	body := r.view.Lines(cols)

	r.viewH = rows - inspectChrome
	r.bodyLen = len(body)
	if max := len(body) - r.viewH; r.top > max {
		r.top = max
	}
	if r.top < 0 {
		r.top = 0
	}

	frame := r.frame(cols, body)
	if frame == r.last {
		return
	}
	io.WriteString(r.out, frame)
	r.last = frame
}

// frame composes the whole screen: home + clear, then exactly rows lines
// joined by CRLF with no trailing newline. Omitting that last newline is what
// keeps the alternate screen from scrolling by one row when the frame fills it
// exactly. Every line is clamped to cols so a long line truncates instead of
// wrapping and shearing the rows below it.
func (r *inspectRun) frame(cols int, body []string) string {
	lines := make([]string, 0, r.viewH+inspectChrome)
	lines = append(lines, clampCols(dim(r.view.Title()), cols))
	for i := 0; i < r.viewH; i++ {
		if idx := r.top + i; idx < len(body) {
			lines = append(lines, clampCols(body[idx], cols))
		} else {
			lines = append(lines, "")
		}
	}
	lines = append(lines, clampCols(dim(r.footer(cols)), cols))
	return "\x1b[H\x1b[2J" + strings.Join(lines, "\r\n")
}

// footer is a position readout plus the key legend. Plain text, no glyphs —
// the middot is the same separator the rest of the REPL's metadata lines use.
//
// Rather than let a narrow terminal hard-cut the legend mid-word, the hint
// degrades in steps and the widest form that fits wins. The last step keeps
// only the position and "q quit": on any width, the user can still see where
// they are and how to get out.
func (r *inspectRun) footer(cols int) string {
	pos := "empty"
	if r.bodyLen > 0 {
		last := r.top + r.viewH
		if last > r.bodyLen {
			last = r.bodyLen
		}
		pos = fmt.Sprintf("lines %d-%d of %d", r.top+1, last, r.bodyLen)
	}
	for _, hint := range []string{
		"up/down or j/k scroll · PgUp/PgDn page · g/G top/bottom · q quit",
		"j/k scroll · PgUp/PgDn page · q quit",
		"q quit",
	} {
		s := pos + " · " + hint
		if displayWidth(s) <= cols {
			return s
		}
	}
	return pos + " · q quit"
}

// clampCols cuts s to at most w visible columns. ANSI escape sequences are
// copied through without being counted (they occupy no cells) and a reset is
// appended when the cut leaves styling open, so a truncated colored line can't
// bleed its color into the rest of the frame. displayWidth/stripANSI (render.go)
// already do the measuring half of this; the copy half has to live here because
// truncate's rune loop miscounts escape bytes.
func clampCols(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if displayWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used, styled := 0, false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
			}
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume the final byte
			}
			b.WriteString(s[i:j])
			styled = true
			i = j
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		cw := runewidth.RuneWidth(c)
		if used+cw > w {
			break
		}
		b.WriteRune(c)
		used += cw
		i += size
	}
	out := b.String()
	if styled && !strings.HasSuffix(out, ansiReset) {
		out += ansiReset
	}
	return out
}
