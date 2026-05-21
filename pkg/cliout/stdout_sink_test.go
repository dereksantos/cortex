package cliout

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func newTestSink(t *testing.T, input string, verbose bool) (*StdoutSink, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errW bytes.Buffer
	s := NewWith(&out, &errW, strings.NewReader(input), verbose)
	return s, &out, &errW
}

func TestStdoutSink_InfoToStdoutWithNewline(t *testing.T) {
	s, out, errW := newTestSink(t, "", false)
	s.Info("model → llama3.2:3b")
	if got := out.String(); got != "model → llama3.2:3b\n" {
		t.Errorf("Info stdout: got %q", got)
	}
	if errW.Len() != 0 {
		t.Errorf("Info should not write to stderr, got %q", errW.String())
	}
}

func TestStdoutSink_WarnToStderrWithPrefix(t *testing.T) {
	s, out, errW := newTestSink(t, "", false)
	s.Warn("ollama unreachable")
	if errW.String() != "warn: ollama unreachable\n" {
		t.Errorf("Warn stderr: got %q", errW.String())
	}
	if out.Len() != 0 {
		t.Errorf("Warn should not write to stdout, got %q", out.String())
	}
}

func TestStdoutSink_ErrorToStderr_NilIsNoop(t *testing.T) {
	s, out, errW := newTestSink(t, "", false)
	s.Error(nil)
	if out.Len() != 0 || errW.Len() != 0 {
		t.Errorf("nil error should be silent")
	}
	s.Error(errors.New("verify failed"))
	if errW.String() != "error: verify failed\n" {
		t.Errorf("Error stderr: got %q", errW.String())
	}
}

func TestStdoutSink_ReadLine_StripsNewlineEchoesPrompt(t *testing.T) {
	s, out, _ := newTestSink(t, "hello world\n", false)
	got, err := s.ReadLine("~ ")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if got != "hello world" {
		t.Errorf("line: got %q, want %q", got, "hello world")
	}
	if out.String() != "~ " {
		t.Errorf("prompt: got %q, want %q", out.String(), "~ ")
	}
}

func TestStdoutSink_ReadLine_EOFOnEmptyInput(t *testing.T) {
	s, _, _ := newTestSink(t, "", false)
	got, err := s.ReadLine("")
	if !errors.Is(err, io.EOF) {
		t.Errorf("err: got %v, want io.EOF", err)
	}
	if got != "" {
		t.Errorf("line: got %q, want \"\"", got)
	}
}

func TestStdoutSink_ReadLine_HandlesCRLF(t *testing.T) {
	s, _, _ := newTestSink(t, "windows-line\r\n", false)
	got, err := s.ReadLine("")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if got != "windows-line" {
		t.Errorf("CRLF strip: got %q", got)
	}
}

func TestStdoutSink_Event_ToolCall_AlwaysShown(t *testing.T) {
	s, out, _ := newTestSink(t, "", false /* not verbose */)
	s.Event("coding.tool_call", map[string]any{"name": "read_file", "args": `{"path":"a"}`})
	if !strings.Contains(out.String(), "⚙ read_file") {
		t.Errorf("tool_call should render in non-verbose; got %q", out.String())
	}
}

func TestStdoutSink_Event_VerboseGate(t *testing.T) {
	// Non-verbose hides coding.turn; verbose shows it.
	for _, verbose := range []bool{false, true} {
		s, out, _ := newTestSink(t, "", verbose)
		s.Event("coding.turn", map[string]any{
			"turn": 1, "tool_calls": 2,
			"cumulative_in": 1200, "cumulative_out": 400, "cumulative_usd": 0.003,
		})
		got := out.String()
		hasTurn := strings.Contains(got, "turn 1")
		if hasTurn != verbose {
			t.Errorf("verbose=%v: turn line rendered=%v, want %v (out=%q)", verbose, hasTurn, verbose, got)
		}
	}
}

func TestStdoutSink_Event_FinalRendersAssistantContent(t *testing.T) {
	s, out, _ := newTestSink(t, "", false)
	s.Event("coding.final", map[string]any{"content": "Cortex is a coding harness."})
	if !strings.Contains(out.String(), "Cortex is a coding harness.") {
		t.Errorf("final content missing; got %q", out.String())
	}
}

func TestStdoutSink_Event_UnknownKindSwallowedNonVerbose(t *testing.T) {
	s, out, _ := newTestSink(t, "", false)
	s.Event("custom.unknown", "anything")
	if out.Len() != 0 {
		t.Errorf("unknown kind should be silent non-verbose; got %q", out.String())
	}
	// Verbose path surfaces it as a fallback.
	s2, out2, _ := newTestSink(t, "", true)
	s2.Event("custom.unknown", "anything")
	if !strings.Contains(out2.String(), "custom.unknown") {
		t.Errorf("verbose should surface unknown kinds; got %q", out2.String())
	}
}

func TestStdoutSink_ConcurrentWritesDontInterleaveLines(t *testing.T) {
	// Concurrent Info+Event writers should each produce intact lines —
	// no mid-line interleaving. We assert byte-level integrity by
	// counting expected substrings post-flush.
	s, out, _ := newTestSink(t, "", false)
	var wg sync.WaitGroup
	const n = 50
	wg.Add(2 * n)
	for i := 0; i < n; i++ {
		go func() { defer wg.Done(); s.Info("AAAA") }()
		go func() { defer wg.Done(); s.Event("coding.tool_call", map[string]any{"name": "BBBB"}) }()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2*n {
		t.Fatalf("expected %d lines, got %d", 2*n, len(lines))
	}
	// Every line must be intact: pure "AAAA" or the "⚙ BBBB" shape.
	for i, ln := range lines {
		if ln != "AAAA" && !strings.Contains(ln, "⚙ BBBB") {
			t.Errorf("line %d corrupted: %q", i, ln)
		}
	}
}

func TestStdoutSink_BannerWritesToStdout(t *testing.T) {
	s, out, errW := newTestSink(t, "", false)
	s.Banner("cortex · /wd · gpt · /help")
	if !strings.Contains(out.String(), "cortex · /wd · gpt · /help") {
		t.Errorf("banner missing from stdout; got %q", out.String())
	}
	if errW.Len() != 0 {
		t.Errorf("banner should not hit stderr; got %q", errW.String())
	}
}

func TestStdoutSink_SetVerbose_FlipsBehavior(t *testing.T) {
	s, out, _ := newTestSink(t, "", false)
	s.Event("coding.turn", map[string]any{"turn": 1, "tool_calls": 1, "cumulative_in": 100, "cumulative_out": 50, "cumulative_usd": 0.001})
	if out.Len() != 0 {
		t.Fatalf("baseline non-verbose should hide turn; got %q", out.String())
	}
	s.SetVerbose(true)
	s.Event("coding.turn", map[string]any{"turn": 2, "tool_calls": 1, "cumulative_in": 200, "cumulative_out": 100, "cumulative_usd": 0.002})
	if !strings.Contains(out.String(), "turn 2") {
		t.Errorf("after SetVerbose(true), turn should render; got %q", out.String())
	}
}
