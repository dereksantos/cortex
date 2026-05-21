package repltui

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cliout"
)

// TestTUISink_SatisfiesSinkInterface pins the contract: *TUISink
// must implement cliout.Sink. Compile-time check via assignment.
func TestTUISink_SatisfiesSinkInterface(t *testing.T) {
	var _ cliout.Sink = NewTUISink(false)
}

// TestTUISink_WriteMethodsDontPanicWithoutProgram covers the
// pre-program-set window: between sink construction and SetProgram
// being called, Info/Warn/Error/Event/Banner should silently drop
// rather than panic. Without this, a writer goroutine racing
// startup could crash the harness.
func TestTUISink_WriteMethodsDontPanicWithoutProgram(t *testing.T) {
	s := NewTUISink(false)
	s.Info("info")
	s.Warn("warn")
	s.Error(errors.New("err"))
	s.Error(nil) // nil error must be a no-op
	s.Event("coding.tool_call", map[string]any{"name": "x"})
	s.Banner("banner")
	// No assertion needed — the test passes if no panic occurred.
}

// TestTUISink_ReadLineDeliveryRoundTrip covers the channel bridge:
// a writer calls deliverInput; the reader's ReadLine returns the
// same payload. Without this round-trip the REPL goroutine can't
// see user input.
func TestTUISink_ReadLineDeliveryRoundTrip(t *testing.T) {
	s := NewTUISink(false)
	go func() {
		// Simulate the Update handler delivering input.
		time.Sleep(10 * time.Millisecond)
		s.deliverInput("hello world", nil)
	}()
	line, err := s.ReadLine("~ ")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "hello world" {
		t.Errorf("line: got %q, want %q", line, "hello world")
	}
}

// TestTUISink_ReadLineEOFOnClose pins the quit path: Close()
// unblocks any in-flight ReadLine with io.EOF so the REPL goroutine
// can exit cleanly when the user quits the program.
func TestTUISink_ReadLineEOFOnClose(t *testing.T) {
	s := NewTUISink(false)
	done := make(chan struct{})
	go func() {
		_, err := s.ReadLine("")
		if !errors.Is(err, io.EOF) {
			t.Errorf("ReadLine after Close: got %v, want io.EOF", err)
		}
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	s.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("ReadLine did not unblock on Close within 1s")
	}
}

// TestTUISink_DeliverInputDropsWhenChannelFull pins the
// non-blocking deliver guarantee: if no ReadLine is waiting, a
// rogue Update can't freeze the TUI by spamming submissions.
func TestTUISink_DeliverInputDropsWhenChannelFull(t *testing.T) {
	s := NewTUISink(false)
	s.deliverInput("first", nil)
	// Buffer is 1; this must not block.
	done := make(chan struct{})
	go func() {
		s.deliverInput("second", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("deliverInput blocked when channel was full")
	}
	// The first value should still be there; the second was dropped.
	line, err := s.ReadLine("")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "first" {
		t.Errorf("got %q, want first", line)
	}
}

// TestRenderEventLine_KnownKindsVisible covers the rendering gate:
// in non-verbose mode the tool-call line is always shown; the
// verbose-gated kinds are hidden (return "").
func TestRenderEventLine_KnownKindsVisible(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		payload   map[string]any
		verbose   bool
		wantEmpty bool
		wantSub   string
	}{
		{"tool_call non-verbose", "coding.tool_call",
			map[string]any{"name": "read_file"}, false, false, "⚙ read_file"},
		{"tool_result non-verbose hidden", "coding.tool_result",
			map[string]any{"output_chars": 100}, false, true, ""},
		{"tool_result verbose shown", "coding.tool_result",
			map[string]any{"output_chars": 100}, true, false, "result: 100"},
		{"turn non-verbose hidden", "coding.turn",
			map[string]any{"turn": 1, "tokens_in": 100, "tokens_out": 50, "tool_calls": 2},
			false, true, ""},
		{"final visible", "coding.final",
			map[string]any{"content": "all done"}, false, false, "all done"},
		{"no_progress visible", "coding.no_progress",
			map[string]any{"window": 5}, false, false, "no progress"},
		{"unknown verbose shown", "custom.unknown",
			map[string]any{}, true, false, "custom.unknown"},
		{"unknown non-verbose hidden", "custom.unknown",
			map[string]any{}, false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderEventLine(tc.kind, tc.payload, tc.verbose)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("wanted empty, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("wanted non-empty containing %q", tc.wantSub)
			}
			// Skip the substring check on the styled output if the
			// expected substring uses unicode (style codes may wrap
			// it); just confirm something was rendered.
			if tc.wantSub != "" && !containsAcrossANSI(got, tc.wantSub) {
				t.Errorf("rendered line missing %q: %q", tc.wantSub, got)
			}
		})
	}
}

// containsAcrossANSI is a lenient substring check that tolerates
// lipgloss-injected ANSI escape sequences sprinkled through the
// rendered text. We strip the ESC sequences (CSI form) before
// looking up the substring.
func containsAcrossANSI(haystack, needle string) bool {
	// CSI sequences: ESC '[' ... letter. Strip them inline.
	var b []byte
	for i := 0; i < len(haystack); i++ {
		if haystack[i] == 0x1b && i+1 < len(haystack) && haystack[i+1] == '[' {
			j := i + 2
			for j < len(haystack) {
				c := haystack[j]
				j++
				if c >= '@' && c <= '~' {
					break
				}
			}
			i = j - 1
			continue
		}
		b = append(b, haystack[i])
	}
	stripped := string(b)
	return contains(stripped, needle)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
