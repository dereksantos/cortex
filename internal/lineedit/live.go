package lineedit

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/style"
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
	stats    StatusStats
	// nowFn is the elapsed-time source for the status row's TurnStart clock.
	// time.Now in production (set in Start); tests pin a fixed instant so the
	// rendered seconds are deterministic (issue #109: the exact "34s" goldens
	// and tests used to read the wall clock measured from package init).
	nowFn func() time.Time

	confirm *confirmState // in-flight y/N question, served by the key loop
	susp    *suspendState // in-flight inspector lease, served by the key loop

	stop chan struct{}
	done chan struct{} // closed when both the key loop and ticker have exited
}

// secondsTickRe matches a trailing elapsed-seconds tick on the caller's
// activity label (SetThinking's "thinking... 12s") so statusLine can drop
// the stats line's own turn-elapsed segment — one counter per row
// (issue #109 review).
var secondsTickRe = regexp.MustCompile(`\d+s$`)

// ansiDim/ansiReset are the Dim role's SGR and the reset; ansiReset also
// closes a clipped styled line so a cut mid-style can't bleed color into the
// rest of the frame.
const (
	ansiDim   = string(style.Dim)
	ansiReset = "\033[0m"
)

// dim paints s in the Dim role so the status row reads as transient metadata.
func dim(s string) string { return style.Paint(s, style.Dim) }

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
		nowFn:   time.Now,
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
	res chan ConfirmChoice
}

// ConfirmChoice is the outcome of a confirmation (issue #107): Confirm
// answers the question with more than a bool — y approves once, a approves
// the exact command for the rest of the session, p approves the command's
// prefix. The caller (cmd/cortex's confirmRisky wiring) decides what
// AlwaysExact/AlwaysPrefix mean; lineedit itself only reports the key.
type ConfirmChoice int

const (
	ConfirmNo ConfirmChoice = iota
	ConfirmYes
	ConfirmAlwaysExact
	ConfirmAlwaysPrefix
)

// Confirm pauses live editing to ask a yes/no/more question, reading the
// answer through the key loop that already owns the terminal (never a
// competing reader). The question's context lines are emitted into
// scrollback; only the final "run it? …" line sits on the status row above
// the prompt. y/Y answers ConfirmYes; n/N/Enter answer ConfirmNo; a/A
// answers ConfirmAlwaysExact; p/P answers ConfirmAlwaysPrefix; Ctrl-C
// answers ConfirmNo and cancels the turn. Safe to call from the turn
// goroutine while the key loop runs.
func (a *Anchor) Confirm(question string) ConfirmChoice {
	body, ask := splitConfirm(question)
	for _, line := range body {
		a.EmitLine(line)
	}
	res := make(chan ConfirmChoice, 1)
	a.mu.Lock()
	a.confirm = &confirmState{ask: ask, res: res}
	a.eraseLocked()
	a.drawLocked()
	a.mu.Unlock()
	select {
	case <-a.stop: // turn ended without an answer → treat as declined
		return ConfirmNo
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

// handleConfirmByte folds one key into an in-flight confirmation. Only the
// confirm answer keys are meaningful (see confirmKeyAction's table); any
// other key is ignored so a stray keystroke can't be misread as an answer.
func (a *Anchor) handleConfirmByte(b byte) {
	v, ok := confirmKeyAction[b]
	if !ok {
		return // not an answer — keep waiting
	}
	choice, cancel := v.choice, v.cancel
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
	c.res <- choice
}

// confirmKeyAction is the single source of truth for which key maps to which
// confirm answer (issue #107): y/Y once, n/N/Enter decline, a/A always this
// session for the exact command, p/P always this session for the command's
// prefix, Ctrl-C declines and cancels the turn. The table is what the
// table-driven tests assert against; anything not in the table is not an
// answer.
var confirmKeyAction = map[byte]struct {
	choice ConfirmChoice
	cancel bool
}{
	'y':  {choice: ConfirmYes},
	'Y':  {choice: ConfirmYes},
	'n':  {choice: ConfirmNo},
	'N':  {choice: ConfirmNo},
	'\r': {choice: ConfirmNo},
	'\n': {choice: ConfirmNo},
	'a':  {choice: ConfirmAlwaysExact},
	'A':  {choice: ConfirmAlwaysExact},
	'p':  {choice: ConfirmAlwaysPrefix},
	'P':  {choice: ConfirmAlwaysPrefix},
	0x03: {choice: ConfirmNo, cancel: true}, // Ctrl-C: decline this command and cancel the turn
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
	a.resetLocked()
	return a.buf.string()
}

// resetLocked clears the status row's label and stats (and its rendered row)
// so a fresh anchor never inherits them from a previous turn. Stop calls it as
// part of the shutdown; tests exercise it directly. Must hold a.mu.
func (a *Anchor) resetLocked() {
	a.status, a.activity, a.stats = "", "", StatusStats{}
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

// StatusStats carries the live figures the status row shows next to the
// activity label (issue #109): model, context-window fill, this turn's
// tokens, the session's cumulative tokens and cost, and the turn's start
// time (for the elapsed seconds). The caller (cmd/cortex) owns populating it
// from session state — lineedit stays session-free; this is the wire shape.
//
// CostUSD is the session CUMULATIVE cost, reported by the backend when it
// reports cost (OpenRouter's usage accounting); zero means the backend never
// reported a figure, and the row must then omit cost entirely — never
// estimate it. Ctx is the context-window fill ratio (0..1+); <= 0 hides the
// segment (no request yet, or window unknown).
//
// Elapsed is the turn's elapsed duration at the instant the caller computed
// it. The anchor re-derives it from its own clock (nowFn; time.Now in
// production, pinned in tests) on every render so the tick loop's repaint
// keeps it fresh — the caller need not re-push stats between renders. A
// zero Elapsed with a non-zero TurnStart means "not yet computed" (the
// anchor derives it); a zero TurnStart with a non-zero Elapsed means "caller
// owns the figure" (StatusLine uses it directly, so the pure method is
// deterministic in tests — issue #109 review).
//
// All fields are read under the anchor's mutex (SetStatus), and the render
// (statusLine) happens under it too, so no sync is needed here.
type StatusStats struct {
	Model      string  // the coding model's name, e.g. "anthropic/claude-..."
	Ctx        float64 // context-window fill, 0..1+ (LastPromptTokens/window)
	InTokens   int     // this turn's input tokens (the last request's prompt)
	OutTokens  int     // this turn's output tokens (the last response's completion)
	CostUSD    float64 // session cumulative cost, backend-reported; 0 = not reported
	SessionIn  int     // session cumulative input tokens
	SessionOut int     // session cumulative output tokens
	TurnStart  time.Time
	Elapsed    time.Duration
}

// StatusLine renders the stats segments for the status row at width w:
// "<model> · ctx 42% · 18.2k in / 1.1k out · $0.013 · 34s" joined with
// " · ", each segment omitted when its data is absent (no model, ctx <= 0,
// zero turn tokens, no reported cost, zero turn start). The elapsed seconds
// are whole seconds since TurnStart (0s before any has elapsed). The
// turn-elapsed segment is the row's ONE counter: when the caller's activity
// label already carries its own seconds tick (SetThinking's "thinking...
// 12s"), statusLine (the anchor-side renderer) drops the segment so the row
// shows a single, consistent number — the label and the stats line's
// counters would disagree because the label resets per thinking phase
// (issue #109 review).
//
// When the full line exceeds w columns it is trimmed segment by segment,
// right to left, until it fits — the issue's priority order: the cost drops
// first (least decision-relevant mid-turn), then the token counts, then the
// context fill; the model and (the caller's) label are kept last.
// w <= 0 means "no width known": the full line is returned untruncated (the
// terminal-side truncation then applies).
func (s StatusStats) StatusLine(width int) string {
	var segs []string
	if s.Model != "" {
		segs = append(segs, s.Model)
	}
	if s.Ctx > 0 {
		segs = append(segs, "ctx "+pct(s.Ctx))
	}
	if s.InTokens > 0 || s.OutTokens > 0 {
		segs = append(segs, humanK(s.InTokens)+" in / "+humanK(s.OutTokens)+" out")
	}
	if s.CostUSD > 0 {
		segs = append(segs, "$"+humanCost(s.CostUSD))
	}
	if !s.TurnStart.IsZero() {
		d := s.Elapsed
		if d <= 0 {
			d = time.Since(s.TurnStart)
		}
		if d > 0 {
			segs = append(segs, fmt.Sprintf("%ds", int(d.Seconds())))
		} else {
			segs = append(segs, "0s")
		}
	}
	if len(segs) == 0 {
		return ""
	}
	full := strings.Join(segs, " · ")
	if width <= 0 || displayWidth(full) <= width {
		return full
	}
	// The row is the dim label + " · " + full; the label is the caller's and
	// must always fit, so trim the stats side to width minus the label's own
	// cost — which this method can't see. The caller passes the width REMAINING
	// after its label (see refreshStatusLocked), which keeps the math here
	// label-free.
	for len(segs) > 1 && displayWidth(strings.Join(segs, " · ")) > width {
		segs = segs[:len(segs)-1]
	}
	line := strings.Join(segs, " · ")
	if displayWidth(line) > width {
		line = truncate(line, width)
	}
	return line
}

// pct renders a 0..1+ fill ratio as a whole percent, e.g. "42%" (values above
// 100% are shown as-is — a window can be over the nominal size on resume).
func pct(r float64) string {
	return fmt.Sprintf("%d%%", int(r*100+0.5))
}

// humanK renders a token count compactly: 8200 -> "8.2k", 999 -> "999".
// lineedit keeps its own copy rather than importing internal/loopui (loopui
// already imports internal/tools; the copy is a few lines, the import is not).
func humanK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n >= 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000), ".0") + "k"
}

// humanCost renders a dollar cost like the REPL's "0.013" — three decimals
// under a dollar, two above — with no "$" of its own (the caller prefixes it),
// so "$0.013" reads as the issue's example does. 0 renders "0.000" and is
// filtered out by StatusLine before reaching here (no reported cost = no
// segment, never an estimate).
func humanCost(c float64) string {
	if c >= 1 {
		return fmt.Sprintf("%.2f", c)
	}
	if c >= 0.01 {
		return fmt.Sprintf("%.3f", c)
	}
	return fmt.Sprintf("%.4f", c)
}

// SetStatus publishes the live stats the status row appends to the activity
// label, and repaints immediately. Pass a zero StatusStats to clear the row
// back to label-only. Safe from any goroutine (the anchor's mutex guards the
// write, as with every other setter in this file).
func (a *Anchor) SetStatus(s StatusStats) {
	a.mu.Lock()
	a.stats = s
	a.refreshStatusLocked()
	a.mu.Unlock()
}

// statusLine renders the status row's full text: the caller's activity label
// (e.g. "thinking... 3s" or a running tool) plus the stats line, " · "-joined
// when both are present — the dim wrapper the terminal receives. The width
// passed to StatusStats is the budget LEFT after the label (label width + the
// 3-column " · " joiner), so label + stats never exceed the row width before
// drawLocked's own truncate (which is a safety net for over-wide labels the
// caller produced, not the trim policy — that lives in StatusLine).
func (a *Anchor) statusLine() string {
	if a.activity == "" {
		return ""
	}
	// The elapsed-seconds segment is the only piece of the stats line that
	// moves on its own (the label's own tick is the caller's); re-derive it
	// from the anchor's clock (nowFn; time.Now in production, pinned in
	// tests) on every render so the tick loop's repaint stays fresh without
	// the caller re-pushing stats.
	s := a.stats
	if !s.TurnStart.IsZero() {
		elapsed := time.Since(s.TurnStart)
		if a.nowFn != nil {
			elapsed = a.nowFn().Sub(s.TurnStart)
		}
		s.Elapsed = elapsed
	}
	// One counter per row (issue #109 review): when the label already
	// carries its own seconds tick (SetThinking's "thinking... 12s"), the
	// stats line's trailing turn-elapsed "34s" would show two counters that
	// disagree (the label resets per thinking phase). Drop the turn-elapsed
	// segment in that case; the label is the row's visible motion.
	if secondsTickRe.MatchString(a.activity) {
		s.TurnStart = time.Time{}
	}
	if s.StatusLine(0) == "" {
		return a.activity
	}
	w := a.widthFn()
	if w <= 0 {
		return a.activity + " · " + s.StatusLine(0)
	}
	budget := w - displayWidth(a.activity) - displayWidth(" · ")
	return a.activity + " · " + s.StatusLine(budget)
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
// No glyph to cycle — the caller's own label text (e.g. "thinking... 3s") and
// the stats line's elapsed-seconds counter are the only things that change
// between ticks.
func (a *Anchor) refreshStatusLocked() {
	if a.activity != "" {
		a.status = dim(a.statusLine())
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
