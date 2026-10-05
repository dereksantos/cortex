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

// pickerList is a selectable view over a fixed body, with an optional filter
// that narrows rows by case-insensitive substring — the shape a session picker
// has. It records every filter the harness hands it and the cursor it is told
// about, so a test can tell "the harness sent me this" apart from "the view
// invented it".
type pickerList struct {
	body     []string
	filter   string
	filterEd []string // every SetFilter argument, in order
	cursorEd []int    // every SetCursor argument, in order
	accepted int      // how many times the harness reported Enter
	cursor   int      // what Selected reports (the harness's, mirrored back)
}

func (p *pickerList) Title() string { return "pick" }

func (p *pickerList) Lines(width int) []string {
	if p.filter == "" {
		return p.body
	}
	var out []string
	for _, line := range p.body {
		if strings.Contains(strings.ToLower(line), strings.ToLower(p.filter)) {
			out = append(out, line)
		}
	}
	return out
}

func (p *pickerList) Selected() int { return p.cursor }

func (p *pickerList) SetCursor(i int) {
	p.cursorEd = append(p.cursorEd, i)
	p.cursor = i
}

func (p *pickerList) Filter() string { return p.filter }

func (p *pickerList) SetFilter(s string) {
	p.filterEd = append(p.filterEd, s)
	p.filter = s
}

func (p *pickerList) Accept() { p.accepted++ }

// rowList is the shape a picker must present for its indices to agree: a body
// containing only selectable rows, with any non-row text (a filter readout) in
// the title instead. Used by the test below to pin the contract from the
// harness side, so a view cannot put chrome in the body and desync the cursor.
type rowList struct {
	rows     []string
	cursor   int
	accepted int
	cursorEd []int
}

func (l *rowList) Title() string      { return "rows" }
func (l *rowList) Lines(int) []string { return l.rows }
func (l *rowList) Selected() int      { return l.cursor }
func (l *rowList) SetCursor(i int)    { l.cursorEd = append(l.cursorEd, i); l.cursor = i }
func (l *rowList) Filter() string     { return "" }
func (l *rowList) SetFilter(string)   {}
func (l *rowList) Accept()            { l.accepted++ }

// TestSelectableBodyRowsAreAllPickable pins the harness/view row contract from
// the harness side: with a body of only selectable rows, G lands the cursor on
// the last row, the footer counts exactly those rows, and Enter then accepts
// that row and closes the run — no keystroke is needed after Enter, and the
// cursor can never come to rest past the last real row.
func TestSelectableBodyRowsAreAllPickable(t *testing.T) {
	out := &strings.Builder{}
	v := &rowList{rows: []string{"row-a", "row-b", "row-c"}, cursor: -1}
	// The Enter must end the run: the trailing Home is there to be eaten by
	// a run that keeps going, which would move the cursor onto row 0 — the very
	// bug this pins (Enter only stopped scrolling, so the run ran on to EOF and
	// the keystrokes after the pick kept editing the view). Home is used rather
	// than a down-arrow because a down-arrow from the last row clamps back to it
	// and so would be a silent no-op either way.
	if err := runInspect(out, script("G\r\x1b[H"), fixedSize(60, 10), v); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	if v.accepted != 1 {
		t.Errorf("Accept() calls = %d, want 1 — Enter must accept once and end the run", v.accepted)
	}
	if got := v.Selected(); got != 2 {
		t.Errorf("Selected() = %d, want the last of 3 rows — a keystroke after the Enter was consumed, so the run did not end at the pick", got)
	}
	// SetCursor is where the cursor lives: the highlight must be parked on the
	// last row and the keystroke after the Enter must never have reached it.
	if n := len(v.cursorEd); n == 0 || v.cursorEd[n-1] != 2 {
		t.Errorf("last SetCursor = %v, want it to end on row 2 — Enter must accept once and end the run", v.cursorEd)
	}
	fs := frames(out.String())
	last := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n")
	if footer := last[len(last)-1]; !strings.Contains(footer, "row 3 of 3") {
		t.Errorf("footer = %q, want it to count only the selectable rows (row 3 of 3)", footer)
	}
	if body := bodyRows(fs[len(fs)-1]); len(body) < 3 || body[0] != "row-a" || body[2] != "row-c" {
		// A list shorter than the viewport keeps its window at the top, so the
		// highlighted last row is the third line rather than the bottom one.
		t.Errorf("visible body = %q, want the three rows with the last one drawn", body)
	}
}

// TestSelectableViewMirrorsHarnessCursor drives the cursor through the harness
// and reads it back through Selecter, so the mapping from key to highlighted
// row is pinned without assuming anything about how a view renders it.
func TestSelectableViewMirrorsHarnessCursor(t *testing.T) {
	tests := []struct {
		name string
		keys string
		want int // 0-based row the cursor ends on
	}{
		{"opens on the first row", "\x1b", 0},
		{"down once", "\x1b[B\x1b", 1},
		{"up at the top stays", "\x1b[A\x1b[A\x1b", 0},
		{"down past the end clamps", strings.Repeat("\x1b[B", 40) + "\x1b", 29},
		{"end is the last row", "\x1b[F\x1b", 29},
		{"home is the first", "\x1b[F\x1b[H\x1b", 0},
		{"G is the last row", "G\x1b", 29},
		// The scroll letters are text on a picker, so "back to the top" is Home
		// rather than g — and typing then deleting is the other way back.
		{"home after a jump is the first row", "G\x1b[H\x1b", 0},
		{"pgdn moves a page minus the overlap", "\x1b[6~\x1b", 7},
		{"pgup returns", "\x1b[6~\x1b[5~\x1b", 0},
		// On a selectable view the letters j/k/b/space are typing keys, so they
		// cannot move the cursor out from under someone who is picking: the two
		// letters narrow the list to nothing and backspace restores it, leaving the
		// highlight re-parked on the first row throughout.
		{"j is typing, not scrolling", "jj\x7f\x7f\x1b", 0},
		// The trailing ESC is the keystroke that leaves, so a script that means
		// "jump to the bottom then step up twice" must count it: G, up, up, esc
		// lands on row 27 — the last row two steps below the bottom.
		{"G then two steps up", "G\x1b[A\x1b[A\x1b", 27},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			v := &pickerList{body: numberedBody(30)}
			if err := runInspect(out, script(tc.keys), fixedSize(40, 10), v); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			if got := v.Selected(); got != tc.want {
				t.Errorf("Selected() = %d, want row %d", got, tc.want)
			}
		})
	}
}

// TestSelectableViewFooterReportsRow pins the footer's selectable form: the
// highlighted row, not a visible range — the number that matters when the body
// is a list of things to choose from.
func TestSelectableViewFooterReportsRow(t *testing.T) {
	out := &strings.Builder{}
	v := &pickerList{body: numberedBody(30)}
	if err := runInspect(out, script("\x1b[B\x1b[B\x1b[B\x1b"), fixedSize(90, 10), v); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	rows := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n")
	footer := rows[len(rows)-1]
	if !strings.Contains(footer, "row 4 of 30") {
		t.Errorf("footer = %q, want it to report the highlighted row (row 4 of 30)", footer)
	}
	if strings.Contains(footer, "lines ") {
		t.Errorf("footer = %q, want no visible-range readout on a selectable view", footer)
	}
}

// TestInspectFilterTakesThePrintableKeys covers the picker's filter box: typed
// runes land in the view, backspace edits, the scroll letters are inert, and q
// is filter text once there is something on screen to cancel back to — while
// ESC still quits, so leaving never requires a key the filter swallowed.
func TestInspectFilterTakesThePrintableKeys(t *testing.T) {
	tests := []struct {
		name    string
		keys    string
		wantFil string   // the filter the view ends with
		wantEds []string // every SetFilter call, in order
	}{
		{"letters type into the filter", "ab\x1b", "ab", []string{"a", "ab"}},
		// ESC is the cancel key a filter box is built around, which is what frees
		// every letter — q included — to be text: the box can spell "quit".
		{"q is filter text, ESC is the way out", "aq\x1b", "aq", []string{"a", "aq"}},
		{"backspace deletes the last char", "ab\x7f\x1b", "a", []string{"a", "ab", "a"}},
		{"backspace on an empty filter is a no-op", "\x7f\x1b", "", nil},
		// The letters the plain list binds as scroll keys are text here, so they
		// cannot scroll a picker out from under the reader. G is the one kept: the
		// shift is deliberate intent, and a last row past the fold needs a way to
		// be reached that is not typing its name.
		{"scroll letters are filter text, G is not", "jkb\x1b" + "G\x1b", "jkb", []string{"j", "jk", "jkb"}},
		{"a space is filter text, not a page", " x\x1b", " x", []string{" ", " x"}},
		// Multi-byte input arrives whole: one rune typed is one SetFilter call
		// carrying that character, and its continuation bytes are consumed rather
		// than leaking in as extra text. Backspace deletes the character, not half
		// of it.
		{"a multi-byte rune arrives whole", "é" + "\x7f" + "\x1b", "", []string{"é", ""}},
		// An arrow key must not leak a continuation byte into the filter: the
		// escape sequence is consumed as one keystroke.
		{"a cursor key adds nothing to the filter", "a" + "\x1b[B" + "b\x1b", "ab", []string{"a", "ab"}},
		// Ctrl-U and friends are not text and not commands: inert.
		{"other control bytes are inert", "ab\x15\x1b", "ab", []string{"a", "ab"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			v := &pickerList{body: numberedBody(5)}
			if err := runInspect(out, script(tc.keys), fixedSize(40, 10), v); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			if v.filter != tc.wantFil {
				t.Errorf("filter = %q, want %q", v.filter, tc.wantFil)
			}
			if got, want := strings.Join(v.filterEd, "|"), strings.Join(tc.wantEds, "|"); got != want {
				t.Errorf("SetFilter calls = %q, want %q", got, want)
			}
		})
	}
}

// TestInspectFilterKeepsQuitKeys pins the keys a user needs in order to get out
// of a filter box: ESC, Ctrl-C and Ctrl-D quit rather than typing, and other
// control bytes stay inert rather than feeding the filter garbage.
func TestInspectFilterKeepsQuitKeys(t *testing.T) {
	tests := []struct {
		name    string
		keys    string
		wantFil string
	}{
		{"esc quits without typing", "ab\x1b", "ab"},
		{"ctrl-c quits without typing", "ab\x03", "ab"},
		{"ctrl-d quits without typing", "ab\x04", "ab"},
		{"ctrl-u is inert", "ab\x15\x1b", "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			v := &pickerList{body: numberedBody(5)}
			if err := runInspect(out, script(tc.keys), fixedSize(40, 10), v); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			if v.filter != tc.wantFil {
				t.Errorf("filter = %q, want %q", v.filter, tc.wantFil)
			}
		})
	}
}

// TestInspectEnterReachesAccepter pins the acceptance key: Enter is delivered to
// an Accepter exactly once and ends the run there — proven by keystrokes fed
// after the Enter that a live run would consume and an ended one never sees —
// while a view with no Selecter is left alone (Enter stays inert and the run
// continues).
func TestInspectEnterReachesAccepter(t *testing.T) {
	out := &strings.Builder{}
	v := &pickerList{body: numberedBody(5)}
	// One Enter is the whole pick: the trailing down-arrow belongs to a run that
	// kept going, so if it is consumed the highlight moves to row 1 — the old
	// behavior, where Enter did nothing visible and ESC was needed next.
	if err := runInspect(out, script("\r\x1b[B"), fixedSize(40, 10), v); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	if v.accepted != 1 {
		t.Errorf("Accept() calls = %d, want 1 — Enter on an Accepter must end the run", v.accepted)
	}
	if got := v.Selected(); got != 0 {
		t.Errorf("Selected() = %d, want row 0 — a keystroke after the Enter was consumed, so the run did not end at the pick", got)
	}
	// Enter lands on the row the cursor was moved to, and again the keystrokes
	// after the pick must go unseen: another Enter would accept a second time and
	// a down-arrow would move the highlight off the accepted row.
	moved := &pickerList{body: numberedBody(5)}
	if err := runInspect(&strings.Builder{}, script("\x1b[B\r\r\x1b[B"), fixedSize(40, 10), moved); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	if moved.accepted != 1 || moved.Selected() != 1 {
		t.Errorf("accepted %d times with cursor on row %d, want 1 accept on row 1",
			moved.accepted, moved.Selected())
	}
	// A plain view must survive Enter too, in both directions: it is never told
	// (a view whose rows are not choices has nothing to accept) and the screen
	// stays up — the down-arrows after the Enter scroll it, which only a run
	// that kept going can do.
	plain := &strings.Builder{}
	if err := runInspect(plain, script("\r\x1b[B\x1b[Bq"), fixedSize(40, 10), staticView{title: "t", body: numberedBody(30)}); err != nil {
		t.Fatalf("runInspect on a plain view = %v, want nil", err)
	}
	fs := frames(plain.String())
	if first := firstOr(bodyRows(fs[len(fs)-1])); first != "line-03" {
		t.Errorf("first visible line = %q, want %q — Enter on a plain view must be inert and leave the screen up", first, "line-03")
	}
}

// TestSelectableCursorKeepsWindowStable checks the window does not chase the
// cursor: a step that stays inside the viewport repaints the same window, and
// one step up from the top row moves the highlight without dragging the list
// down with it (the topmost visible row is what the eye anchors on).
func TestSelectableCursorKeepsWindowStable(t *testing.T) {
	// 10 rows => 8 body rows over a 30-line body.
	tests := []struct {
		name  string
		keys  string
		first string // expected first visible body line
		row   int    // expected 0-based cursor
	}{
		{"a step within the page keeps the window", "\x1b[B\x1b[B\x1b", "line-01", 2},
		{"a step past the bottom edge scrolls by one row", strings.Repeat("\x1b[B", 8) + "\x1b", "line-02", 8},
		{"a step up from the top row keeps the window", "\x1b[B\x1b[A\x1b", "line-01", 0},
		{"end puts the last row on the last line", "\x1b[F\x1b", "line-23", 29},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			v := &pickerList{body: numberedBody(30)}
			if err := runInspect(out, script(tc.keys), fixedSize(40, 10), v); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			fs := frames(out.String())
			got := bodyRows(fs[len(fs)-1])
			if first := firstOr(got); first != tc.first {
				t.Errorf("first visible line = %q, want %q", first, tc.first)
			}
			if got := v.Selected(); got != tc.row {
				t.Errorf("Selected() = %d, want row %d", got, tc.row)
			}
		})
	}
}

// TestSelectableViewCursorTracksANarrowingList covers a filter edit that leaves
// the cursor past the end of the shortened list. Typing re-parks the highlight
// on the first match — the newest row is the one the filter was typed to find,
// and Enter then needs no second keystroke — and the highlight can never float
// past the last row of the narrowed list.
func TestSelectableViewCursorTracksANarrowingList(t *testing.T) {
	tests := []struct {
		name    string
		keys    string
		want    int
		wantMsg string
	}{
		{
			// End of the list, then "line-0": nine matches (line-01..line-09) and
			// the cursor was at 29, so it must land on the first match.
			"typing re-parks on the first match", "\x1b[F" + "line-0" + "\x1b", 0,
			"the first of the nine matching rows",
		},
		// A deleted filter restores the list under the cursor the shrink left it
		// on (the last row that existed while the list was empty, which is row 0),
		// and typing must not fling the highlight somewhere the user never put it.
		{
			"a deleted filter restores the list without chasing the cursor", "\x1b[F" + "zz" + "\x7f\x7f" + "\x1b", 0,
			"row 0, where the empty list had left the highlight",
		},
		{
			"a filter matching nothing has no selection", "zzz" + "\x1b", -1,
			"no row at all",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			v := &pickerList{body: numberedBody(30)}
			if err := runInspect(out, script(tc.keys), fixedSize(40, 10), v); err != nil {
				t.Fatalf("runInspect = %v, want nil", err)
			}
			if got := v.Selected(); got != tc.want {
				t.Errorf("Selected() = %d, want %d (%s)", got, tc.want, tc.wantMsg)
			}
		})
	}
}

// TestSelectableViewEmptyListHasNoSelection pins the no-rows case: a filter
// that matches nothing means there is nothing to pick, and Selected must say
// so rather than pointing at row zero of an empty list.
func TestSelectableViewEmptyListHasNoSelection(t *testing.T) {
	out := &strings.Builder{}
	v := &pickerList{body: numberedBody(5)}
	if err := runInspect(out, script("zzz"+"\x1b[B"+"\x1b"), fixedSize(40, 10), v); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	if got := v.Selected(); got >= 0 {
		t.Errorf("Selected() = %d, want a negative index (no rows match the filter)", got)
	}
	fs := frames(out.String())
	if footer := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n"); !strings.Contains(footer[len(footer)-1], "empty") {
		t.Errorf("footer = %q, want it to report an empty list", footer[len(footer)-1])
	}
}

// TestInspectPlainViewIsUnchangedByTheInteractiveHalf is the regression guard
// for the whole step: a view that implements nothing but Title and Lines must
// be driven exactly as it was — letters scroll, Enter and Backspace are inert,
// and the footer keeps its visible-range readout.
func TestInspectPlainViewIsUnchangedByTheInteractiveHalf(t *testing.T) {
	const cols, rows = 40, 10
	body := numberedBody(30)
	out := &strings.Builder{}
	if err := runInspect(out, script("jj\r\x7f"+"q"), fixedSize(cols, rows), staticView{title: "t", body: body}); err != nil {
		t.Fatalf("runInspect = %v, want nil", err)
	}
	fs := frames(out.String())
	got := bodyRows(fs[len(fs)-1])
	if first := firstOr(got); first != "line-03" {
		t.Errorf("first visible line = %q, want %q (j/k must still scroll a plain view)", first, "line-03")
	}
	footer := strings.Split(stripANSI(fs[len(fs)-1]), "\r\n")
	if f := footer[len(footer)-1]; !strings.Contains(f, "lines 3-10 of 30") {
		t.Errorf("footer = %q, want the plain view's range readout", f)
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
