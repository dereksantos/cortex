package lineedit

import "testing"

// feedTypeAhead replays a byte stream through appendTypeAhead the way the
// Interruptible watcher does, returning the captured text and whether an
// interrupt was requested (stopping at the first ESC/Ctrl-C).
func feedTypeAhead(in []byte) (string, bool) {
	var buf []byte
	for _, c := range in {
		var interrupt bool
		if buf, interrupt = appendTypeAhead(buf, c); interrupt {
			return string(buf), true
		}
	}
	return string(buf), false
}

func TestAppendTypeAhead(t *testing.T) {
	tests := []struct {
		name      string
		in        []byte
		want      string
		interrupt bool
	}{
		{"plain text", []byte("hello"), "hello", false},
		{"enter accumulates", []byte("a\nb"), "a\nb", false},
		{"carriage return kept", []byte("a\rb"), "a\rb", false},
		{"backspace erases", []byte("abc\x7f"), "ab", false},
		{"backspace on empty is noop", []byte("\x7fx"), "x", false},
		{"esc interrupts and stops", []byte("ab\x1bcd"), "ab", true},
		{"ctrl-c interrupts", []byte("x\x03y"), "x", true},
		{"stray control bytes dropped", []byte("a\x01\x02b"), "ab", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, interrupt := feedTypeAhead(tt.in)
			if got != tt.want {
				t.Errorf("captured = %q, want %q", got, tt.want)
			}
			if interrupt != tt.interrupt {
				t.Errorf("interrupt = %v, want %v", interrupt, tt.interrupt)
			}
		})
	}
}
