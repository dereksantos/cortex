package lineedit

import (
	"io"
	"strings"
	"testing"
)

// sliceSource feeds bytes to the decoder from an in-memory string.
type sliceSource struct {
	data []byte
	i    int
}

func (s *sliceSource) next() (byte, error) {
	if s.i >= len(s.data) {
		return 0, io.EOF
	}
	b := s.data[s.i]
	s.i++
	return b, nil
}

// decodeAll drives decodeKey until EOF, returning every event.
func decodeAll(t *testing.T, in string) []keyEvent {
	t.Helper()
	src := &sliceSource{data: []byte(in)}
	var out []keyEvent
	for {
		ev, err := decodeKey(src)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, ev)
	}
}

func TestBufferEditing(t *testing.T) {
	b := &buffer{}
	b.insert([]rune("hello world")...)
	if b.string() != "hello world" || b.pos != 11 {
		t.Fatalf("after insert: %q pos=%d", b.string(), b.pos)
	}
	b.home()
	if b.pos != 0 {
		t.Errorf("home: pos=%d", b.pos)
	}
	b.wordRight() // to end of "hello"
	if b.pos != 5 {
		t.Errorf("wordRight: pos=%d, want 5", b.pos)
	}
	b.end()
	b.killWord() // remove "world"
	if b.string() != "hello " {
		t.Errorf("killWord: %q", b.string())
	}
	b.insert([]rune("there")...)
	b.home()
	b.killToEnd()
	if b.string() != "" {
		t.Errorf("killToEnd from home: %q", b.string())
	}
}

func TestBufferBackspaceAndCursor(t *testing.T) {
	b := &buffer{}
	b.insert([]rune("abc")...)
	b.left()
	b.left() // between a and b
	b.backspace()
	if b.string() != "bc" || b.pos != 0 {
		t.Errorf("backspace: %q pos=%d", b.string(), b.pos)
	}
	b.end()
	b.insert('d')
	if b.string() != "bcd" {
		t.Errorf("insert at end: %q", b.string())
	}
	b.home()
	b.deleteForward()
	if b.string() != "cd" {
		t.Errorf("deleteForward: %q", b.string())
	}
}

func TestDecodeArrowsAndEditingKeys(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want keyKind
	}{
		{"left arrow", "\x1b[D", keyLeft},
		{"right arrow", "\x1b[C", keyRight},
		{"up arrow", "\x1b[A", keyUp},
		{"down arrow", "\x1b[B", keyDown},
		{"home csi", "\x1b[H", keyHome},
		{"end csi", "\x1b[F", keyEnd},
		{"home ~", "\x1b[1~", keyHome},
		{"delete", "\x1b[3~", keyDelete},
		{"ctrl-left (word)", "\x1b[1;5D", keyWordLeft},
		{"ctrl-right (word)", "\x1b[1;5C", keyWordRight},
		{"alt-b", "\x1bb", keyWordLeft},
		{"alt-f", "\x1bf", keyWordRight},
		{"ss3 home", "\x1bOH", keyHome},
		{"ctrl-a", "\x01", keyHome},
		{"ctrl-e", "\x05", keyEnd},
		{"ctrl-w", "\x17", keyKillWord},
		{"ctrl-u", "\x15", keyKillToStart},
		{"ctrl-k", "\x0b", keyKillToEnd},
		{"backspace del", "\x7f", keyBackspace},
		{"ctrl-c", "\x03", keyInterrupt},
		{"ctrl-d", "\x04", keyEOF},
		{"enter cr", "\r", keyEnter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := decodeAll(t, tt.in)
			if len(evs) != 1 || evs[0].kind != tt.want {
				t.Errorf("decode %q = %+v, want kind %v", tt.in, evs, tt.want)
			}
		})
	}
}

func TestDecodeRunesUTF8(t *testing.T) {
	evs := decodeAll(t, "aé世")
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	want := []rune{'a', 'é', '世'}
	for i, r := range want {
		if evs[i].kind != keyRune || evs[i].r != r {
			t.Errorf("event %d = %+v, want rune %q", i, evs[i], r)
		}
	}
}

func TestDecodeBracketedPaste(t *testing.T) {
	// A multi-line paste arrives between the 200~ / 201~ markers as one event.
	in := "\x1b[200~line one\nline two\nline three\x1b[201~"
	evs := decodeAll(t, in)
	if len(evs) != 1 || evs[0].kind != keyPaste {
		t.Fatalf("got %+v, want a single paste event", evs)
	}
	if evs[0].paste != "line one\nline two\nline three" {
		t.Errorf("paste body = %q", evs[0].paste)
	}
}

func TestRenderScrollKeepsCursorVisible(t *testing.T) {
	b := &buffer{}
	b.insert([]rune("0123456789")...) // pos at end (10)
	out := renderLine("> ", b, 6)     // prompt width 2, avail 4
	// One row only: must start with CR+clear and contain the prompt.
	if !strings.HasPrefix(out, "\r\033[K> ") {
		t.Errorf("render prefix wrong: %q", out)
	}
	// The far-left chars scroll off; the tail near the cursor stays visible.
	if !strings.Contains(out, "9") {
		t.Errorf("cursor tail not visible: %q", out)
	}
	if strings.Contains(stripANSI(out), "0123") {
		t.Errorf("head should have scrolled off: %q", stripANSI(out))
	}
}

func TestRenderSummaryForPaste(t *testing.T) {
	b := &buffer{}
	b.insert([]rune("first\nsecond\nthird")...)
	out := renderLine("> ", b, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "first") || !strings.Contains(plain, "+2 lines") {
		t.Errorf("summary = %q, want first line + line count", plain)
	}
}

func TestStripANSIWidth(t *testing.T) {
	// Colored prompt occupies only its visible glyphs.
	colored := "\x1b[36m> \x1b[0m"
	if w := displayWidth(colored); w != 2 {
		t.Errorf("displayWidth(colored) = %d, want 2", w)
	}
}
