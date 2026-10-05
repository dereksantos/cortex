// inspect.go — the alternate-screen "inspector": an on-demand full-screen,
// scrollable view for the few surfaces that genuinely want a whole screen
// /context today; the session picker and a memory browser later).
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
//
// A view can also be interactive rather than read-only, by implementing the
// optional Accepter, Filterer, and Selecter interfaces: the harness then routes
// Enter, Backspace, and typed runes to it (so a filter box can hold the
// keyboard) and tracks a row cursor that up/down/page/g/G move. Those are
// strictly additive — a view that implements nothing but Title and Lines is
// driven exactly as before, keys and frame included.
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
// enter/restore dance are the harness's job, not the view's. A view that wants
// to pick instead of merely display adds Accept, Filter, SetFilter, and
// Selected — see Accepter, Filterer, and Selecter.
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
//
// The last three entries — Enter, Backspace, and a plain printable rune — are
// the interactive set: they exist so a view can offer a filter box and a
// pick-with-Enter affordance instead of being a read-only page. Only views
// that opt in (Filterer, Selecter) are told about them; the plain list
// (/context) simply ignores them, exactly as it ignores the cursor.
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
	// inspectEnter is the acceptance key. The harness always stops scrolling on
	// it and hands the action to an Accepter; nothing else about it is
	// harness-level, because "what does accepting mean" is the view's business.
	inspectEnter
	// inspectBackspace edits the view's filter leftward (Filterer).
	inspectBackspace
	// inspectRune appends key.r to the view's filter. The rune travels in
	// inspectEvent, not in the action value, so the vocabulary stays a closed
	// set of constants rather than a rune masquerading as one.
	inspectRune
)

// inspectEvent is one decoded keystroke: which action it maps to, and for
// inspectRune the rune itself.
type inspectEvent struct {
	action inspectAction
	r      rune
}

// Accepter is an optional view interface for the acceptance key. A view that
// implements it is told when the user hit Enter, so it can treat the selected
// row as a choice rather than as text on a page — typically by leaving (a
// picker cancels the inspector the way Escape does) or by recording the pick
// for its builder to read afterwards. There is deliberately no return channel:
// a harness that reported a value would have to decide what acceptance means,
// which is the mistake this seam exists to avoid.
type Accepter interface {
	Accept()
}

// Filterer is an optional view interface for views that narrow their own rows
// by text. The harness owns no filter state: it appends the typed rune and
// deletes one on Backspace, and nothing else — the string it hands to
// SetFilter is what the view's Filter returns plus one keystroke, so the filter
// semantics (case, substring vs. prefix, which fields match, what an empty
// filter shows) stay in the view, where they belong.
type Filterer interface {
	Filter() string
	SetFilter(s string)
}

// Selecter is an optional view interface: which row the user's cursor is on,
// as an index into the rows the view is currently showing (its filtered list,
// not the full one). It is read-only and purely informational — the harness
// itself never consults it, because scrolling a plain list does not care which
// row is highlighted. It exists so the caller of Inspect can ask the view what
// was selected once the user has accepted, without the harness having to know
// that picking is what the view is for. A view with no rows to select returns a
// negative index.
type Selecter interface {
	Selected() int
}

// Cursorer is an optional view interface for a selectable view: the harness
// tells it which row the cursor is on, immediately before each repaint. The
// harness deliberately does not mark the row itself — a View's body is passed
// through verbatim, and whether the highlight is a bold row, a leading marker,
// or nothing at all is presentation the view owns. A view that does not
// implement it is never told, so its bytes stay exactly what Lines returned.
type Cursorer interface {
	SetCursor(i int)
}

// inspectRun is one open inspector. top is the body index shown on the first
// body row; viewH and bodyLen are carried from the last render so a paging key
// can be applied before the next one recomputes them.
//
// cursor is the interactive half: the highlighted row, -1 when the list has no
// rows. For a view that is not a Selecter it stays at -1 and every cursor key
// degrades to the plain scroll it always was, which is what keeps /context
// byte-identical. topMin records how far down the top of the window has been,
// the floor a back-up scroll may not rise above while the cursor is visible —
// see moveCursorTo.
type inspectRun struct {
	out    io.Writer
	src    pollSource
	sizeFn func() (cols, rows int)
	view   View

	selectable bool // view implements Selecter: the row cursor is live
	filterable bool // view implements Filterer: printable keys edit its filter

	top     int
	viewH   int
	bodyLen int
	last    string // last frame written, so an unchanged frame costs no bytes
	cursor  int    // highlighted row; -1 when the list has no rows (see render)
	// filterChanged is set by a keystroke that edited the view's filter and
	// cleared once render has re-parked the cursor on the first match. It is the
	// one signal that distinguishes "the list changed because the user typed"
	// from "the list changed because the world did" (a live view re-pulled on an
	// idle tick), which is what keeps a placed cursor where it was put.
	filterChanged bool
}

// runInspect drives one inspector to completion, returning when the user quits
// (q / ESC / Ctrl-C / Ctrl-D) or the input stream ends. Enter is not one of
// them: on Enter the harness tells an Accepter to accept and keeps the screen
// up, so a view that picks does its own leaving, and one that does not simply
// ignores the key.
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
	_, r.selectable = v.(Selecter)
	_, r.filterable = v.(Filterer)
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
		if r.decode(b).action == inspectQuit {
			return nil
		}
		r.render()
	}
}

// decode maps one first-byte to an event and applies it. Returns inspectQuit
// when the user asked to leave. Escape sequences are decoded through the
// package's shared decoder (keys.go), so arrows/PgUp/PgDn/Home/End behave
// identically here and at the prompt.
//
// The routing rule for a view that filters is the whole difference between a
// picker and a page: a letter belongs to the filter box, so it must not scroll.
// Only what the harness must keep is special-cased — ESC (cancel, one
// keystroke, so a sequence's bytes can never be read as the next character
// typed), Ctrl-C and Ctrl-D, and G for jump-to-bottom — and everything else
// printable goes to the view. A view that does not filter — every inspector that
// exists today — is driven exactly as before.
func (r *inspectRun) decode(b byte) inspectEvent {
	if r.filterable {
		switch b {
		case 0x1b:
			// ESC is consumed whole here: bare ESC cancels, Alt-<char> is text, and
			// a cursor/page sequence moves the cursor. No continuation byte of any
			// of them can reach the filter as a stray character.
			ev, err := r.readEsc()
			if err != nil || ev.kind == keyUnknown {
				return inspectEvent{action: inspectQuit}
			}
			if ev.kind == keyRune {
				return r.apply(inspectEvent{action: inspectRune, r: ev.r})
			}
			return r.apply(inspectEvent{action: actionForKey(ev.kind)})
		case 'G':
			return r.apply(inspectEvent{action: inspectBottom})
		case 0x03, 0x04: // Ctrl-C, Ctrl-D
			return inspectEvent{action: inspectQuit}
		default:
			if ev := r.decodeFiltered(b); ev.action != inspectNone {
				return r.apply(ev)
			}
			// Any other control byte (Ctrl-U, Ctrl-K, …) does nothing: a text box
			// has no use for it, and a plain list's letter bindings must not leak
			// into one that is collecting text.
			return inspectEvent{}
		}
	}
	act := inspectNone
	switch b {
	case 'q', 'Q', 0x03, 0x04: // q, Ctrl-C, Ctrl-D
		return inspectEvent{action: inspectQuit}
	case '\r', '\n':
		act = inspectEnter
	case 0x7f, 0x08:
		act = inspectBackspace
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
		ev, err := r.readEsc()
		if err != nil {
			return inspectEvent{action: inspectQuit}
		}
		act = actionForKey(ev.kind)
	}
	return r.apply(inspectEvent{action: act})
}

// readEsc reads the keystroke whose first byte was ESC: a bare ESC (nothing
// follows within one poll) yields keyUnknown, which every caller reads as
// cancel; otherwise the sequence is decoded whole through the shared decoder,
// so none of its bytes are left for the next keystroke to be misread from.
func (r *inspectRun) readEsc() (keyEvent, error) {
	nb, timedOut, err := r.src.firstByte()
	if err != nil {
		return keyEvent{}, err
	}
	if timedOut {
		return keyEvent{kind: keyUnknown}, nil
	}
	return decodeEscape(&pushback{b: nb, src: r.src})
}

// decodeFiltered decodes a printable keystroke for a view that collects its own
// text, and returns inspectNone for the handful of keys the caller above keeps.
// A filtering view is a text box first: a letter never scrolls, and q is q, so
// the box can hold the word "quit" — the way out is ESC, Ctrl-C, Ctrl-D, the
// pairing every interactive filter box uses.
//
// The byte is classified directly rather than pushed back through the shared
// decoder, because the inspector loop reads one byte per poll and hands that
// byte here; pushing it back into decodeKeyByte would block on continuation
// bytes a scripted source has no more of.
func (r *inspectRun) decodeFiltered(b byte) inspectEvent {
	switch b {
	case '\r', '\n':
		return inspectEvent{action: inspectEnter}
	case 0x7f, 0x08:
		return inspectEvent{action: inspectBackspace}
	}
	if b < 0x20 {
		return inspectEvent{} // control bytes are the caller's, not text
	}
	// A printable rune's first byte is not the whole character when it is
	// multi-byte: assemble it from its continuation bytes (the same utf8Len rule
	// keys.go's prompt path uses) so the filter receives one character and the
	// next poll sees the next keystroke rather than a stray 0x80-0xbf byte.
	buf := []byte{b}
	for i := 1; i < utf8Len(b); i++ {
		c, err := r.src.next()
		if err != nil {
			break // a truncated rune still contributes its first byte's text
		}
		buf = append(buf, c)
	}
	rn, _ := utf8.DecodeRune(buf)
	return inspectEvent{action: inspectRune, r: rn}
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

// apply moves the cursor and, through it, the scroll offset, returning the
// event it moved by so every keystroke has exactly one apply on its path.
// Clamping is left to render, which is the only place that knows the current
// body length and viewport height — so a resize, or a filter edit that just
// narrowed the list, can never leave the cursor or top stranded past the end.
func (r *inspectRun) apply(e inspectEvent) inspectEvent {
	switch e.action {
	case inspectEnter:
		// The view decides what acceptance means; all the harness does is stop
		// here instead of scrolling. A view with no Accept is a plain list, for
		// which Enter is simply inert.
		if a, ok := r.view.(Accepter); ok {
			a.Accept()
		}
		return e
	case inspectRune:
		if f, ok := r.view.(Filterer); ok {
			f.SetFilter(f.Filter() + string(e.r))
			r.filterChanged = true
		}
		return e
	case inspectBackspace:
		if f, ok := r.view.(Filterer); ok {
			if f.Filter() != "" {
				f.SetFilter(dropLastRune(f.Filter()))
				r.filterChanged = true
			}
		}
		return e
	}

	page := r.viewH - 1 // keep one line of overlap so context carries across a page
	if page < 1 {
		page = 1
	}
	switch e.action {
	case inspectUp:
		r.moveCursor(-1, r.bodyLen)
	case inspectDown:
		r.moveCursor(1, r.bodyLen)
	case inspectPageUp:
		r.moveCursor(-page, r.bodyLen)
	case inspectPageDown:
		r.moveCursor(page, r.bodyLen)
	case inspectTop:
		r.moveCursorTo(0, r.bodyLen)
	case inspectBottom:
		// A plain list has no cursor, so "bottom" is its last full page, exactly
		// as before; a selectable list highlights its last row and pulls the
		// window along to it.
		if r.selectable {
			r.moveCursorTo(r.bodyLen-1, r.bodyLen)
		} else {
			r.top = r.bodyLen // clamped down to the last full page by render
		}
	}
	return e
}

// moveCursor moves the highlighted row by delta rows. Keeping the cursor
// visible is deliberately not a "follow the cursor" clamp: the window moves
// only when the highlight is actually past an edge. That is what keeps the
// topmost row stable — one step down from the middle of a page repaints the
// same window with one highlight moved, and one step up from the top row does
// not drag the whole list down with it. For a view that is not selectable
// there is no cursor, so the keystroke is the plain scroll it always was.
func (r *inspectRun) moveCursor(delta, bodyLen int) {
	if !r.selectable {
		r.top += delta
		return
	}
	r.moveCursorTo(r.cursor+delta, bodyLen)
}

// moveCursorTo highlights row i (clamped into the current list) and brings the
// window along only as far as it must to keep that row visible. bodyLen is the
// caller's fresh row count, because render is the only place that has pulled
// the list since the last keystroke — the stale r.bodyLen belongs to the
// previous frame and would clamp a jump to bottom one row short.
func (r *inspectRun) moveCursorTo(i, bodyLen int) {
	if i < 0 {
		i = 0
	}
	if bodyLen > 0 && i > bodyLen-1 {
		i = bodyLen - 1
	}
	if bodyLen == 0 {
		i = -1 // nothing to highlight: an empty list has no row under the cursor
	}
	r.cursor = i
	if r.viewH < 1 || i < 0 {
		return
	}
	if i < r.top {
		r.top = i
	} else if i > r.top+r.viewH-1 {
		r.top = i - r.viewH + 1
	}
}

// dropLastRune removes one rune from the end of s, so Backspace on a filter
// holding a multi-byte character deletes the character and not half of it.
func dropLastRune(s string) string {
	if s == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
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
	// One pull per frame, shared by the cursor clamp and the frame itself: the
	// view is pull-based and live (it may have changed since the last tick), and
	// asking it twice could paint a frame that describes a different list than
	// the cursor was clamped against.
	//
	// The cursor is clamped before the view is told where it is, so a Cursorer
	// is never pointed at a row that does not exist. A filter edit that narrowed
	// the list, or a resize that shortened it, can leave the highlight past the
	// last row; pull it back so there is always a row under the cursor when there
	// is a row at all (an empty list has none, which is what Selected reports).
	viewH := rows - inspectChrome
	body := r.view.Lines(cols)
	if r.selectable {
		if r.cursor < 0 {
			r.cursor = 0 // the highlight opens on the first row, and re-opens after an empty filter
		}
		if len(body) == 0 {
			r.cursor = -1
			r.filterChanged = false
		} else {
			// A filtering view parks the highlight on the first match after the
			// user types: the rows are ordered newest-first, so the top row is the
			// one they most likely want and Enter needs no second keystroke. Only a
			// filter edit re-parks it — a resize or an idle repaint of a live view
			// must never yank a cursor the user placed.
			if r.filterChanged {
				r.cursor = 0
				r.top = 0
				r.filterChanged = false
			}
			// Never carry a highlight down into a list that has shrunk: a filter
			// edit pulls it back to the last row that exists, so it cannot float
			// over nothing (an empty list has no cursor at all, above).
			if r.cursor > len(body)-1 {
				r.cursor = len(body) - 1
			}
			if r.cursor < r.top {
				r.top = r.cursor
			}
			// Keep the highlighted row inside the window, moving the window the
			// least distance possible: the topmost row stays put whenever the
			// cursor is already visible, which is what an eye anchored on a list
			// expects.
			if r.cursor > r.top+viewH-1 {
				r.top = r.cursor - viewH + 1
			}
		}
	}
	if max := len(body) - viewH; r.top > max {
		r.top = max
	}
	if r.top < 0 {
		r.top = 0
	}

	// A selectable view marks its own highlighted row, so it is told where the
	// cursor landed after the clamp — the frame cache then sees a moved cursor,
	// because the highlight changes the bytes the view renders.
	if c, ok := r.view.(Cursorer); ok {
		c.SetCursor(r.cursor)
		body = r.view.Lines(cols)
	}

	r.viewH = viewH
	r.bodyLen = len(body)

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
// A selectable list reports the highlighted row instead of a visible range:
// where the cursor is is the one number that matters there, and a range says
// nothing about the choice. A filtering list names its edit keys in the widest
// form of the legend, so the affordance is on screen rather than in the docs.
//
// Rather than let a narrow terminal hard-cut the legend mid-word, the hint
// degrades in steps and the widest form that fits wins. The last step keeps
// only the position and "q quit": on any width, the user can still see where
// they are and how to get out.
func (r *inspectRun) footer(cols int) string {
	pos := "empty"
	if r.bodyLen > 0 {
		if r.selectable {
			pos = fmt.Sprintf("row %d of %d", r.cursor+1, r.bodyLen)
		} else {
			last := r.top + r.viewH
			if last > r.bodyLen {
				last = r.bodyLen
			}
			pos = fmt.Sprintf("lines %d-%d of %d", r.top+1, last, r.bodyLen)
		}
	}
	hints := []string{
		"up/down or j/k scroll · PgUp/PgDn page · g/G top/bottom · q quit",
		"j/k scroll · PgUp/PgDn page · q quit",
		"q quit",
	}
	if r.selectable && r.filterable {
		hints = []string{
			"up/down select · type to filter · backspace edits · enter picks · esc cancels",
			"up/down select · type filters · enter picks · esc cancels",
			"enter picks · esc cancels",
			"q quit",
		}
	} else if r.selectable {
		hints = []string{
			"up/down select · PgUp/PgDn page · g/G top/bottom · enter picks · esc cancels",
			"up/down select · enter picks · esc cancels",
			"enter picks · esc cancels",
			"q quit",
		}
	}
	for _, hint := range hints {
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
