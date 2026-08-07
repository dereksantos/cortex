package lineedit

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// scriptSource replays a fixed keystroke script — plus explicit idle ticks, so
// the resize/repaint path is deterministic — as a pollSource. Same in-memory
// approach as sliceSource (lineedit_test.go) and newTestAnchor (live_test.go):
// no TTY, no timing.
type scriptSource struct {
	events []scriptEvent
	i      int
}

// scriptEvent is either one input byte or one idle poll tick.
type scriptEvent struct {
	b    byte
	idle bool
}

// script builds a byte-per-rune script from s; "\x00" is not usable as a key,
// so tests needing idle ticks compose events directly or use scriptIdle.
func script(s string) *scriptSource {
	ev := make([]scriptEvent, 0, len(s))
	for i := 0; i < len(s); i++ {
		ev = append(ev, scriptEvent{b: s[i]})
	}
	return &scriptSource{events: ev}
}

// idle appends n idle ticks to the script.
func (s *scriptSource) idle(n int) *scriptSource {
	for i := 0; i < n; i++ {
		s.events = append(s.events, scriptEvent{idle: true})
	}
	return s
}

// keys appends the bytes of s to the script.
func (s *scriptSource) keys(str string) *scriptSource {
	for i := 0; i < len(str); i++ {
		s.events = append(s.events, scriptEvent{b: str[i]})
	}
	return s
}

func (s *scriptSource) firstByte() (byte, bool, error) {
	if s.i >= len(s.events) {
		return 0, false, io.EOF
	}
	e := s.events[s.i]
	s.i++
	return e.b, e.idle, nil
}

func (s *scriptSource) next() (byte, error) {
	for {
		b, idle, err := s.firstByte()
		if err != nil {
			return 0, err
		}
		if !idle {
			return b, nil
		}
	}
}

// staticView is a fixed-body view; numbered lines make scroll position
// readable straight off the rendered frame.
type staticView struct {
	title string
	body  []string
}

func (v staticView) Title() string            { return v.title }
func (v staticView) Lines(width int) []string { return v.body }
func numberedBody(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("line-%02d", i+1)
	}
	return out
}

// fixedSize is a sizeFn returning a constant geometry.
func fixedSize(cols, rows int) func() (int, int) {
	return func() (int, int) { return cols, rows }
}

// frames splits a captured output stream into its individual frames. Every
// frame begins with the home+clear prefix, so that is the separator.
func frames(out string) []string {
	parts := strings.Split(out, "\x1b[H\x1b[2J")
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	return parts
}

// bodyRows returns the visible body rows of a frame (title and footer removed).
func bodyRows(frame string) []string {
	rows := strings.Split(stripANSI(frame), "\r\n")
	if len(rows) < 3 {
		return nil
	}
	return rows[1 : len(rows)-1]
}

func TestInspectQuitKeys(t *testing.T) {
	tests := []struct {
		name string
		src  *scriptSource
	}{
		{"lowercase q", script("q")},
		{"uppercase Q", script("Q")},
		{"ctrl-c", script("\x03")},
		{"ctrl-d", script("\x04")},
		// A bare ESC is an ESC followed by no burst: the idle tick is what makes
		// it distinguishable from an arrow key.
		{"bare esc", script("\x1b").idle(1)},
		{"input stream ends", script("")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			if err := runInspect(out, tc.src, fixedSize(40, 10), staticView{title: "t", body: numberedBody(3)}); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			if got := len(frames(out.String())); got == 0 {
				t.Errorf("no frame painted before quit")
			}
		})
	}
}

func TestInspectScrollAndPaging(t *testing.T) {
	// 10 rows => 8 body rows over a 30-line body; a page is 7 (one overlap).
	const rows, cols = 10, 40
	body := numberedBody(30)

	tests := []struct {
		name  string
		keys  string
		first string // expected first visible body line
	}{
		{"opens at the top", "q", "line-01"},
		{"j scrolls down one", "jq", "line-02"},
		{"k after j returns", "jkq", "line-01"},
		{"k at the top is clamped", "kkkq", "line-01"},
		{"down arrow scrolls", "\x1b[B\x1b[Bq", "line-03"},
		{"up arrow scrolls back", "\x1b[B\x1b[B\x1b[Aq", "line-02"},
		{"space pages down", " q", "line-08"},
		{"b pages back", " bq", "line-01"},
		{"pgdn pages down", "\x1b[6~q", "line-08"},
		{"pgup pages back", "\x1b[6~\x1b[5~q", "line-01"},
		{"G jumps to the last page", "Gq", "line-23"},
		{"g returns to the top", "Ggq", "line-01"},
		{"home is top", "G\x1b[Hq", "line-01"},
		{"end is bottom", "\x1b[Fq", "line-23"},
		{"scrolling past the end clamps", strings.Repeat("j", 60) + "q", "line-23"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			if err := runInspect(out, script(tc.keys), fixedSize(cols, rows), staticView{title: "t", body: body}); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			fs := frames(out.String())
			if len(fs) == 0 {
				t.Fatalf("no frames painted")
			}
			got := bodyRows(fs[len(fs)-1])
			if len(got) == 0 || got[0] != tc.first {
				t.Errorf("first visible line = %q, want %q", firstOr(got), tc.first)
			}
		})
	}
}

func firstOr(rows []string) string {
	if len(rows) == 0 {
		return "<none>"
	}
	return rows[0]
}

func TestInspectRepaintsOnResize(t *testing.T) {
	// The size changes between the opening paint and the idle tick; the tick
	// must notice and repaint without any signal handling.
	calls := 0
	sizeFn := func() (int, int) {
		calls++
		if calls <= 1 {
			return 40, 10
		}
		return 40, 6
	}
	out := &strings.Builder{}
	if err := runInspect(out, script("").idle(1).keys("q"), sizeFn, staticView{title: "t", body: numberedBody(30)}); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	if len(fs) < 2 {
		t.Fatalf("frames after resize = %d, want at least 2", len(fs))
	}
	if got, want := len(bodyRows(fs[0])), 8; got != want {
		t.Errorf("body rows before resize = %d, want %d", got, want)
	}
	if got, want := len(bodyRows(fs[1])), 4; got != want {
		t.Errorf("body rows after resize = %d, want %d", got, want)
	}
}

func TestInspectResizeReclampsScroll(t *testing.T) {
	// Parked at the bottom of a tall screen, then the screen grows: the offset
	// must be pulled back so the view never shows past the end of the body.
	calls := 0
	sizeFn := func() (int, int) {
		calls++
		if calls <= 2 {
			return 40, 6 // 4 body rows
		}
		return 40, 22 // 20 body rows — top must fall back to 0 for a 20-line body
	}
	out := &strings.Builder{}
	if err := runInspect(out, script("G").idle(1).keys("q"), sizeFn, staticView{title: "t", body: numberedBody(20)}); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	got := bodyRows(fs[len(fs)-1])
	if len(got) == 0 || got[0] != "line-01" {
		t.Errorf("after growing the screen the first line = %q, want %q", firstOr(got), "line-01")
	}
}

func TestInspectIdleDoesNotRepaintUnchangedFrame(t *testing.T) {
	out := &strings.Builder{}
	if err := runInspect(out, script("").idle(5).keys("q"), fixedSize(40, 10), staticView{title: "t", body: numberedBody(3)}); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	if got := len(frames(out.String())); got != 1 {
		t.Errorf("frames = %d, want 1 (an unchanged frame must cost no bytes)", got)
	}
}

// liveView changes its body on every pull, standing in for a view backed by
// live session state.
type liveView struct{ pulls int }

func (v *liveView) Title() string { return "live" }
func (v *liveView) Lines(width int) []string {
	v.pulls++
	return []string{fmt.Sprintf("pull-%d", v.pulls)}
}

func TestInspectRepullsViewWhileIdle(t *testing.T) {
	out := &strings.Builder{}
	v := &liveView{}
	if err := runInspect(out, script("").idle(2).keys("q"), fixedSize(40, 10), v); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	if len(fs) < 3 {
		t.Fatalf("frames = %d, want at least 3 (one per idle re-pull)", len(fs))
	}
	if !strings.Contains(stripANSI(fs[len(fs)-1]), "pull-3") {
		t.Errorf("last frame did not show the freshest pull: %q", stripANSI(fs[len(fs)-1]))
	}
}

func TestInspectFrameNeverWrapsOrOverflows(t *testing.T) {
	const cols, rows = 20, 8
	long := strings.Repeat("x", 200)
	colored := "\x1b[34m" + strings.Repeat("y", 200) + "\x1b[0m"
	out := &strings.Builder{}
	view := staticView{title: strings.Repeat("T", 200), body: []string{long, colored, "short"}}
	if err := runInspect(out, script("q"), fixedSize(cols, rows), view); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	got := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n")
	if len(got) != rows {
		t.Fatalf("frame rows = %d, want %d", len(got), rows)
	}
	for i, row := range got {
		if len([]rune(row)) > cols {
			t.Errorf("row %d is %d columns wide, want at most %d: %q", i, len([]rune(row)), cols, row)
		}
	}
	// No trailing newline: a frame that exactly fills the screen must not
	// scroll the alternate buffer by a row.
	if strings.HasSuffix(fs[len(fs)-1], "\r\n") {
		t.Errorf("frame ends with a newline, which would scroll the alternate screen")
	}
}

// panicView blows up on render — the harness must not take the REPL with it.
type panicView struct{}

func (panicView) Title() string      { return "boom" }
func (panicView) Lines(int) []string { panic("view exploded") }

func TestInspectRecoversViewPanic(t *testing.T) {
	out := &strings.Builder{}
	err := runInspect(out, script("q"), fixedSize(40, 10), panicView{})
	if err == nil {
		t.Fatal("runInspect = nil, want an error carrying the panic")
	}
	if !strings.Contains(err.Error(), "view exploded") {
		t.Errorf("error = %v, want it to name the panic value", err)
	}
}

func TestTerminalInspectEntersAndRestoresAltScreen(t *testing.T) {
	tests := []struct {
		name string
		view View
	}{
		{"normal exit", staticView{title: "t", body: numberedBody(5)}},
		{"view panics", panicView{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			term := &Terminal{out: out, fd: -1}
			_ = term.inspectWith(tc.view, script("q"))

			got := out.String()
			if !strings.HasPrefix(got, altScreenOn+cursorHide) {
				t.Errorf("output must open with alt-screen + cursor-hide; got %q", head(got))
			}
			if !strings.HasSuffix(got, cursorShow+altScreenOff) {
				t.Errorf("output must close with cursor-show + alt-screen-off; got %q", tail(got))
			}
			if term.inAlt {
				t.Error("terminal still marked as holding the alternate screen after Inspect")
			}
			// Byte-for-byte scrollback restoration rests on nothing being written
			// to the primary buffer: exactly one enter and one leave, in order.
			if n := strings.Count(got, altScreenOn); n != 1 {
				t.Errorf("alt-screen enter count = %d, want 1", n)
			}
			if n := strings.Count(got, altScreenOff); n != 1 {
				t.Errorf("alt-screen leave count = %d, want 1", n)
			}
		})
	}
}

func TestTerminalCloseLeavesAltScreen(t *testing.T) {
	t.Run("inspector still open", func(t *testing.T) {
		out := &strings.Builder{}
		term := &Terminal{out: out, fd: -1}
		term.enterAlt() // simulates a fatal signal arriving mid-inspector
		out.Reset()
		_ = term.Close()
		if got := out.String(); !strings.HasPrefix(got, cursorShow+altScreenOff) {
			t.Errorf("Close did not restore the primary screen first; got %q", head(got))
		}
	})
	t.Run("no inspector ever opened", func(t *testing.T) {
		out := &strings.Builder{}
		term := &Terminal{out: out, fd: -1}
		_ = term.Close()
		if strings.Contains(out.String(), altScreenOff) {
			t.Error("Close emitted an alt-screen leave on a terminal that never entered one")
		}
	})
}

func TestClampCols(t *testing.T) {
	tests := []struct {
		name string
		in   string
		w    int
		want string
	}{
		{"fits", "abc", 10, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"cut", "abcdef", 3, "abc"},
		{"zero width", "abc", 0, ""},
		{"ansi does not consume columns", "\x1b[34mabcde\x1b[0m", 5, "\x1b[34mabcde\x1b[0m"},
		{"ansi kept and closed on cut", "\x1b[34mabcdef", 3, "\x1b[34mabc" + ansiReset},
		{"wide runes counted by column", "日本語", 4, "日本"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampCols(tc.in, tc.w); got != tc.want {
				t.Errorf("clampCols(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
			}
		})
	}
}

// TestInspectFooter covers the position readout and the narrow-width
// degradation: the hint shortens in steps rather than being cut mid-word, and
// the position plus the way out survive at any width.
func TestInspectFooter(t *testing.T) {
	tests := []struct {
		name     string
		cols     int
		contains []string
	}{
		{"wide keeps the full legend", 120, []string{"lines 1-8 of 30", "g/G top/bottom", "q quit"}},
		{"medium drops the verbose scroll hint", 60, []string{"lines 1-8 of 30", "PgUp/PgDn page", "q quit"}},
		{"narrow keeps only position and exit", 30, []string{"lines 1-8 of 30", "q quit"}},
		{"very narrow still clamps to width", 12, []string{"lines"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			if err := runInspect(out, script("q"), fixedSize(tc.cols, 10), staticView{title: "t", body: numberedBody(30)}); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			fs := frames(out.String())
			rows := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n")
			footer := rows[len(rows)-1]
			if got := len([]rune(footer)); got > tc.cols {
				t.Errorf("footer is %d columns, want at most %d: %q", got, tc.cols, footer)
			}
			for _, want := range tc.contains {
				if !strings.Contains(footer, want) {
					t.Errorf("footer = %q, want it to contain %q", footer, want)
				}
			}
		})
	}
}

func TestInspectFooterOnEmptyView(t *testing.T) {
	out := &strings.Builder{}
	if err := runInspect(out, script("jkGq"), fixedSize(60, 10), staticView{title: "t"}); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	rows := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n")
	if footer := rows[len(rows)-1]; !strings.Contains(footer, "empty") || !strings.Contains(footer, "q quit") {
		t.Errorf("footer on an empty view = %q, want it to say empty and how to quit", footer)
	}
}

func head(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

func tail(s string) string {
	if len(s) > 40 {
		return s[len(s)-40:]
	}
	return s
}

// --- the anchor lease -------------------------------------------------------
//
// These cover the contention resolution described on Terminal.leaseInput: while
// an inspector is open the anchor's key loop stays the fd's only reader and
// forwards bytes to it, and the anchor stops drawing so it can't paint the
// pinned prompt over the alternate screen.

func TestAnchorSuspendErasesAndSuppressesDrawing(t *testing.T) {
	a, out := newTestAnchor("> ", "draft", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()
	out.Reset()

	_, resume := a.Suspend()
	if a.rows != 0 {
		t.Errorf("rows after Suspend = %d, want 0 (block erased before the alt screen)", a.rows)
	}
	if !strings.Contains(out.String(), "\033[J") {
		t.Errorf("Suspend did not erase the pinned block; got %q", out.String())
	}

	// Every redraw path funnels through drawLocked, so all of these must be inert.
	out.Reset()
	a.SetActivity("running a tool")
	a.SetPrompt("$ ")
	a.applyEvent(keyEvent{kind: keyRune, r: 'x'})
	if got := out.String(); got != "" {
		t.Errorf("anchor drew while suspended: %q", got)
	}

	resume()
	if !strings.Contains(stripANSI(out.String()), "$ draftx") {
		t.Errorf("resume did not redraw the prompt with its accumulated edits; got %q", stripANSI(out.String()))
	}
	if a.rows == 0 {
		t.Error("rows after resume = 0, want the block redrawn")
	}
}

func TestAnchorSuspendForwardsRawKeysToTheLease(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 80)
	src, resume := a.Suspend()
	defer resume()

	// A cursor-key escape sequence must arrive byte-for-byte and in order: the
	// inspector, not the anchor, is what decodes it.
	for _, b := range []byte("\x1b[6~") {
		if interrupt := a.handleByte(b); interrupt {
			t.Fatalf("handleByte(%q) requested interrupt while suspended", b)
		}
	}
	var got []byte
	for i := 0; i < 4; i++ {
		b, timedOut, err := src.firstByte()
		if err != nil || timedOut {
			t.Fatalf("firstByte %d = (%q, %v, %v), want a byte", i, b, timedOut, err)
		}
		got = append(got, b)
	}
	if string(got) != "\x1b[6~" {
		t.Errorf("forwarded bytes = %q, want %q", got, "\x1b[6~")
	}
	// The buffer must be untouched — a suspended anchor edits nothing.
	if a.buf.string() != "" {
		t.Errorf("buffer = %q, want empty (keys belong to the inspector)", a.buf.string())
	}
}

func TestAnchorSuspendHoldsOutputForScrollback(t *testing.T) {
	a, out := newTestAnchor("> ", "", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()

	_, resume := a.Suspend()
	out.Reset()
	a.EmitLine("first")
	a.EmitLine("second")
	if got := out.String(); got != "" {
		t.Errorf("turn output leaked onto the alternate screen: %q", got)
	}

	resume()
	vis := stripANSI(out.String())
	i, j := strings.Index(vis, "first"), strings.Index(vis, "second")
	if i < 0 || j < 0 {
		t.Fatalf("held output was lost instead of flushed to scrollback; got %q", vis)
	}
	if i > j {
		t.Errorf("held output flushed out of order; got %q", vis)
	}
}

func TestAnchorSuspendIsIdempotent(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 80)
	_, resume := a.Suspend()
	defer resume()
	src2, resume2 := a.Suspend()
	resume2() // must not end the first lease
	if a.susp == nil {
		t.Error("a nested Suspend/resume pair ended the outer lease")
	}
	if _, _, err := src2.firstByte(); err != io.EOF {
		t.Errorf("nested lease firstByte err = %v, want io.EOF (inert source)", err)
	}
}

func TestTerminalLeaseInputParksALiveAnchor(t *testing.T) {
	out := &strings.Builder{}
	term := &Terminal{out: out, fd: -1}
	a, _ := newTestAnchor("> ", "", 80)
	a.term = term
	term.setAnchor(a)

	_, release := term.leaseInput()
	if a.susp == nil {
		t.Fatal("leaseInput did not suspend the live anchor — a second reader would race it")
	}
	release()
	if a.susp != nil {
		t.Error("releasing the lease did not resume the anchor")
	}

	term.setAnchor(nil)
	src, release := term.leaseInput()
	if _, ok := src.(*readerSource); !ok {
		t.Errorf("with no anchor live, lease source = %T, want *readerSource", src)
	}
	release()
}
