package lineedit

import (
	"bytes"
	"strings"
	"testing"
)

func TestAltEnterInsertsNewline(t *testing.T) {
	line, err := readLineWithNoTTY(t, "first\x1b\rsecond\r", "", nil)
	if err != nil {
		t.Fatalf("readLineWith: %v", err)
	}
	if line != "first\nsecond" {
		t.Errorf("line = %q, want %q", line, "first\nsecond")
	}
}

func TestLineUpDownMoveWithinADraft(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		pos     int
		up      bool
		wantOK  bool
		wantPos int
	}{
		{"up keeps the column", "abcd\nxy\nlonger", 10, true, true, 7},       // "lo|nger" col 2 → "xy|"
		{"up clamps to a short line", "abcd\nxy\nlonger", 13, true, true, 7}, // col 5 → end of "xy"
		{"up from the first line falls through", "abcd\nxy", 2, true, false, 2},
		{"down keeps the column", "abcd\nxy\nlonger", 1, false, true, 6},
		{"down from the last line falls through", "abcd\nxy", 6, false, false, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &buffer{runes: []rune(tt.text), pos: tt.pos}
			var ok bool
			if tt.up {
				ok = b.lineUp()
			} else {
				ok = b.lineDown()
			}
			if ok != tt.wantOK || b.pos != tt.wantPos {
				t.Errorf("moved=%v pos=%d, want moved=%v pos=%d", ok, b.pos, tt.wantOK, tt.wantPos)
			}
		})
	}
}

func TestMultilineDraftShowsTheCursorLine(t *testing.T) {
	b := &buffer{runes: []rune("first line\nsecond line\nthird"), pos: 14} // in "second"
	out := renderLine("> ", b, 60, "")
	plain := stripANSI(out)
	if !strings.Contains(plain, "> second line") || !strings.Contains(plain, "[line 2/3]") {
		t.Errorf("want the cursor's line and its position: %q", plain)
	}
	if strings.Contains(plain, "first line") || strings.Contains(plain, "third") {
		t.Errorf("only the cursor's line belongs on the row: %q", plain)
	}
	if !strings.HasSuffix(out, "\r\033[5C") { // "> " + "sec" → column 5
		t.Errorf("cursor not parked in the line: %q", out)
	}
}

func TestCtrlOOpensDetailFromTheMainPromptOnly(t *testing.T) {
	opened := 0
	var out bytes.Buffer
	term := &Terminal{out: &out}
	term.SetDetail(func() { opened++ })
	echo := func(line string, _ int) string { return line }

	if _, err := term.readLineWith("> ", "", &sliceSource{data: []byte("\x0f\r")}, echo); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Errorf("Ctrl-O at the main prompt should open the detail view once, opened %d", opened)
	}
	if _, err := term.readLineWith("> ", "", &sliceSource{data: []byte("\x0f\r")}, nil); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Errorf("a plain read (a y/N answer) must not open it, opened %d", opened)
	}
}

func TestQuestionMarkShowsKeyHints(t *testing.T) {
	run := func(data string, echo bool) (string, string) {
		var out bytes.Buffer
		term := &Terminal{out: &out}
		term.SetKeyHints("tab complete · ctrl-o last turn")
		var accepted func(string, int) string
		if echo {
			accepted = func(l string, _ int) string { return l }
		}
		line, err := term.readLineWith("> ", "", &sliceSource{data: []byte(data)}, accepted)
		if err != nil {
			t.Fatal(err)
		}
		return line, out.String()
	}
	line, out := run("?\r", true)
	if line != "?" {
		t.Errorf("the ? is still typed: line = %q", line)
	}
	if !strings.Contains(out, "tab complete · ctrl-o last turn") {
		t.Errorf("hints not shown: %q", out)
	}
	if _, out := run("a?\r", true); strings.Contains(out, "tab complete") {
		t.Errorf("hints only for a leading ?: %q", out)
	}
	if _, out := run("?\r", false); strings.Contains(out, "tab complete") {
		t.Errorf("a plain read must not show hints: %q", out)
	}
}
