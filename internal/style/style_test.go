package style

import (
	"strings"
	"testing"
)

func TestPaint(t *testing.T) {
	tests := []struct {
		name  string
		color bool
		in    string
		want  string
	}{
		{"colored", true, "hi", "\033[90mhi\033[0m"},
		{"no color", false, "hi", "hi"},
		{"empty stays empty", true, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer ForceColor(tt.color)()
			if got := Paint(tt.in, Dim); got != tt.want {
				t.Errorf("Paint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestForceColorRestores(t *testing.T) {
	before := ColorEnabled()
	restore := ForceColor(!before)
	if ColorEnabled() == before {
		t.Fatal("ForceColor did not change the setting")
	}
	restore()
	if ColorEnabled() != before {
		t.Errorf("restore left ColorEnabled = %v, want %v", ColorEnabled(), before)
	}
}

func TestStripAndWidth(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantStrip string
		wantWidth int
	}{
		{"plain", "abc", "abc", 3},
		{"colored", "\033[36mabc\033[0m", "abc", 3},
		{"wide runes", "日本", "日本", 4},
		{"mixed", "\033[90m14:02\033[0m  ok", "14:02  ok", 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip(tt.in); got != tt.wantStrip {
				t.Errorf("Strip(%q) = %q, want %q", tt.in, got, tt.wantStrip)
			}
			if got := Width(tt.in); got != tt.wantWidth {
				t.Errorf("Width(%q) = %d, want %d", tt.in, got, tt.wantWidth)
			}
		})
	}
}

func TestClip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"fits", "abc", 3, "abc"},
		{"cut", "abcdef", 4, "abc…"},
		{"one cell", "abcdef", 1, "…"},
		{"no room", "abc", 0, "…"},
		{"wide rune not split", "日本語", 4, "日…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clip(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("Clip(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			if tt.max > 0 && Width(got) > tt.max {
				t.Errorf("Clip(%q, %d) is %d cells wide", tt.in, tt.max, Width(got))
			}
		})
	}
}

func TestJustify(t *testing.T) {
	tests := []struct {
		name        string
		left, right string
		width       int
		want        string
	}{
		{"flush right", ". >", "24k|131k", 16, ". >     24k|131k"},
		{"too narrow drops right", ". > long input", "24k|131k", 16, ". > long input"},
		{"no terminal joins", ". >", "24k", 0, ". >  24k"},
		{"no right", ". >", "", 40, ". >"},
		{"ansi not counted", "\033[36m>\033[0m", "x", 4, "\033[36m>\033[0m  x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Justify(tt.left, tt.right, tt.width, 2); got != tt.want {
				t.Errorf("Justify = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"fits", "a short line", 20, []string{"a short line"}},
		{"wraps at spaces", "the report struct already carried json tags", 20,
			[]string{"the report struct", "already carried json", "tags"}},
		{"keeps newlines", "one\ntwo", 20, []string{"one", "two"}},
		{"long word alone", "a supercalifragilistic b", 8, []string{"a", "supercalifragilistic", "b"}},
		{"keeps indent", "  - item one two three", 12, []string{"  - item one", "  two three"}},
		{"no width", "a b c", 0, []string{"a b c"}},
		{"blank line kept", "a\n\nb", 10, []string{"a", "", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Wrap(tt.in, tt.width)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("Wrap(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

func TestContentWidth(t *testing.T) {
	tests := []struct{ term, want int }{
		{0, 0}, {40, 40}, {100, 100}, {220, MaxContentWidth},
	}
	for _, tt := range tests {
		prev := TermWidth
		TermWidth = func() int { return tt.term }
		if got := ContentWidth(); got != tt.want {
			t.Errorf("ContentWidth() at %d cols = %d, want %d", tt.term, got, tt.want)
		}
		TermWidth = prev
	}
}
