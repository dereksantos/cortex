package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitBlocks(t *testing.T) {
	tests := []struct {
		name       string
		pending    string
		wantBlocks []string
		wantRest   string
	}{
		{
			name:       "incomplete line held entirely",
			pending:    "Here's the plan",
			wantBlocks: nil,
			wantRest:   "Here's the plan",
		},
		{
			name:       "paragraph held until blank line",
			pending:    "one line\n",
			wantBlocks: nil,
			wantRest:   "one line\n",
		},
		{
			name:       "paragraph flushes at blank line",
			pending:    "para one\n\npara two",
			wantBlocks: []string{"para one"},
			wantRest:   "para two",
		},
		{
			name:       "multi-line list buffers as one block",
			pending:    "- a\n- b\n- c\n",
			wantBlocks: nil,
			wantRest:   "- a\n- b\n- c\n",
		},
		{
			name:       "open fence is never flushed",
			pending:    "```go\nfunc x() {}\n",
			wantBlocks: nil,
			wantRest:   "```go\nfunc x() {}\n",
		},
		{
			name:       "closed fence flushes as one block",
			pending:    "```go\nfunc x() {}\n```\n",
			wantBlocks: []string{"```go\nfunc x() {}\n```"},
			wantRest:   "",
		},
		{
			name:       "prose before a fence flushes separately",
			pending:    "Look:\n```go\nx := 1\n```\n",
			wantBlocks: []string{"Look:", "```go\nx := 1\n```"},
			wantRest:   "",
		},
		{
			name:       "blank line inside fence is not a boundary",
			pending:    "```\nline1\n\nline2\n```\n",
			wantBlocks: []string{"```\nline1\n\nline2\n```"},
			wantRest:   "",
		},
		{
			name:       "tilde fences recognized",
			pending:    "~~~\ncode\n~~~\n",
			wantBlocks: []string{"~~~\ncode\n~~~"},
			wantRest:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, rest := splitBlocks(tt.pending)
			if !reflect.DeepEqual(blocks, tt.wantBlocks) {
				t.Errorf("blocks = %#v, want %#v", blocks, tt.wantBlocks)
			}
			if rest != tt.wantRest {
				t.Errorf("rest = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

// TestStreamPrinterRenderHoldsOpenFence verifies the streaming guarantee: a
// half-written code fence is never shown until it closes, and once closed its
// code appears (glamour-rendered) in the output.
func TestStreamPrinterRenderHoldsOpenFence(t *testing.T) {
	md := newMarkdownRenderer(80)
	if md == nil {
		t.Fatal("renderer build failed")
	}
	var buf strings.Builder
	p := &streamPrinter{out: &buf, md: md} // nil spinner: no terminal control

	// Stream an opening fence and a code line, but not the close.
	p.onContent("```go\n")
	p.onContent("func answer() int { return 42 }\n")
	if strings.Contains(buf.String(), "answer") {
		t.Fatalf("open fence leaked code before close: %q", buf.String())
	}

	// Close the fence: now the block flushes through glamour.
	p.onContent("```\n")
	p.finish()
	out := stripANSI(buf.String())
	if !strings.Contains(out, "func answer()") {
		t.Errorf("closed fence not rendered; output = %q", out)
	}
}

// TestStreamPrinterRawPathUnchanged confirms md=nil still streams bytes
// verbatim (the path the existing stream_test.go relies on).
func TestStreamPrinterRawPathUnchanged(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf} // md nil → raw
	p.onContent("plain ")
	p.onContent("text")
	p.finish()
	if !strings.Contains(buf.String(), "plain text") {
		t.Errorf("raw path mangled output: %q", buf.String())
	}
}

func TestTrimBlockPadding(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"literal trailing spaces", "hello     ", "hello"},
		{"ansi-wrapped trailing spaces", "hi\x1b[38;5;252m \x1b[0m\x1b[38;5;252m \x1b[0m", "hi"},
		{"interior spaces kept", "a b  c   ", "a b  c"},
		{"per line", "one   \ntwo \x1b[0m\nthree", "one\ntwo\nthree"},
		{"styled text preserved", "\x1b[1mbold\x1b[0m   ", "\x1b[1mbold\x1b[0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimBlockPadding(tt.in); got != tt.want {
				t.Errorf("trimBlockPadding(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTrimLeadingIndent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain indent", "  Hello", "Hello"},
		{"leading newlines", "\n\n  Hello", "Hello"},
		{"empty sgr pairs then indent", "\x1b[38;5;252m\x1b[0m  \x1b[38;5;252mHello", "\x1b[38;5;252mHello"},
		{"keeps color of first glyph", "  \x1b[1mBold", "\x1b[1mBold"},
		{"nothing to trim", "Hello world", "Hello world"},
		{"interior indent preserved", "First\n  second", "First\n  second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimLeadingIndent(tt.in); got != tt.want {
				t.Errorf("trimLeadingIndent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// stripANSI removes CSI/OSC escape sequences so assertions can match the
// visible text glamour wraps in styling codes.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// Skip ESC and a following CSI ("[ … letter") or short sequence.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++ // the final byte
				}
				continue
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
