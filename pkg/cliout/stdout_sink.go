package cliout

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// StdoutSink is the readline-style implementation of Sink: writes to
// os.Stdout (Info, Event, Banner) and os.Stderr (Warn, Error), reads
// from os.Stdin via bufio.Scanner. Preserves the pre-TUI REPL
// behavior verbatim — every formatting choice mirrors what the
// fmt.Println call sites produced before the migration.
//
// Construction is cheap: New takes no args and wires the standard
// streams. Tests use NewWith to inject their own readers/writers.
//
// Thread-safety: writes go through an internal mutex so concurrent
// goroutines (e.g. the harness Notify callback firing while a slash
// command also prints) don't interleave bytes within a single line.
// ReadLine takes the same mutex while waiting for input so a writer
// can't print into the middle of a prompt.
type StdoutSink struct {
	mu     sync.Mutex
	out    io.Writer
	err    io.Writer
	in     io.Reader
	reader *bufio.Reader

	// verbose threads through to Event so the per-event formatter
	// can decide what to surface vs hide. Mirrors the pre-migration
	// verbose flag the REPL passed to makeREPLNotifier.
	verbose bool
}

// New returns a StdoutSink bound to the standard streams. verbose
// enables verbose Event rendering (per-turn token/cost counters,
// etc.) — same gate as the REPL's --verbose flag.
func New(verbose bool) *StdoutSink {
	return NewWith(os.Stdout, os.Stderr, os.Stdin, verbose)
}

// NewWith returns a StdoutSink with injectable streams. Tests pass
// bytes.Buffers / strings.Readers; production callers use New.
func NewWith(out, errW io.Writer, in io.Reader, verbose bool) *StdoutSink {
	return &StdoutSink{
		out:     out,
		err:     errW,
		in:      in,
		reader:  bufio.NewReader(in),
		verbose: verbose,
	}
}

// SetVerbose flips the verbose flag mid-session. Used by the REPL's
// /verbose slash command once it exists; today only construction
// sets it.
func (s *StdoutSink) SetVerbose(v bool) {
	s.mu.Lock()
	s.verbose = v
	s.mu.Unlock()
}

// Info writes msg to stdout with a trailing newline.
func (s *StdoutSink) Info(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.out, msg)
}

// Warn writes "warn: msg" to stderr. Distinct prefix so a user
// scanning the transcript can spot non-fatal anomalies without
// reading the routing.
func (s *StdoutSink) Warn(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.err, "warn: "+msg)
}

// Error writes "error: <err>" to stderr. Nil err is a no-op so call
// sites can pass err directly without guarding.
func (s *StdoutSink) Error(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.err, "error: %v\n", err)
}

// Banner writes the once-per-session welcome line to stdout. Same
// rendering as Info today; the type separation matters mostly for
// the TUI implementation that pins it.
func (s *StdoutSink) Banner(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.out, text)
}

// Event dispatches a structured (kind, payload) tuple to the
// renderer. The shape matches harness.Loop.Notify so callers can do
// `loop.Notify = sink.Event` directly.
//
// Per-event formatting lives in this method (not in the call site)
// so the TUI implementation can choose a different format for the
// same event kind without rippling through the harness.
func (s *StdoutSink) Event(kind string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	renderEvent(s.out, kind, payload, s.verbose)
}

// ReadLine prints prompt (if non-empty) and blocks until one line of
// input is available. Returns the line with the trailing newline
// stripped, or ("", io.EOF) on Ctrl-D / underlying reader EOF.
//
// Holds the sink mutex while waiting for input so a concurrent
// writer can't print into the middle of the prompt — that would
// look like:
//
//	[bootstrap] discovered foo.go
//	~ ▮     ← prompt was here; output bumped it down
//
// Releasing the lock after reading lets writers flush behind the
// user's submission.
func (s *StdoutSink) ReadLine(prompt string) (string, error) {
	s.mu.Lock()
	if prompt != "" {
		fmt.Fprint(s.out, prompt)
	}
	line, err := s.reader.ReadString('\n')
	s.mu.Unlock()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read input: %w", err)
	}
	// On a successful read or graceful EOF with a trailing partial
	// line, strip the newline. On EOF with no bytes, return io.EOF.
	if len(line) == 0 && errors.Is(err, io.EOF) {
		return "", io.EOF
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// renderEvent is the per-kind formatter. Mirrors what the REPL's
// makeREPLNotifier function produced pre-migration; consolidates the
// formatting here so the call sites stay agnostic.
//
// Unknown kinds in verbose mode are surfaced as a fallback "<kind>
// %v" line so new event types don't disappear silently. Non-verbose
// mode swallows them — surface in trace files instead.
func renderEvent(w io.Writer, kind string, payload any, verbose bool) {
	m, _ := payload.(map[string]any)
	switch kind {
	case "coding.session_start":
		// Banner is printed elsewhere; the session-start event is
		// metadata — surface only in verbose.
		if !verbose {
			return
		}
		fmt.Fprintf(w, "  · session start: model=%v turns≤%v\n", m["model"], m["max_turns"])

	case "coding.turn":
		if !verbose {
			return
		}
		fmt.Fprintf(w, "  · turn %v: %v tool calls, cumulative %v in / %v out tokens, $%.4f\n",
			m["turn"], m["tool_calls"], m["cumulative_in"], m["cumulative_out"], coerceFloat(m["cumulative_usd"]))

	case "coding.tool_call":
		// Always surfaced: the per-tool-call line is the user-facing
		// "what is the agent doing" signal. Concise format.
		fmt.Fprintf(w, "  ⚙ %v\n", m["name"])

	case "coding.tool_result":
		// Verbose-only: tool results are a token cost line; the
		// content lands in the next assistant turn anyway.
		if !verbose {
			return
		}
		fmt.Fprintf(w, "    · %v: %v chars\n", m["name"], m["output_chars"])

	case "coding.final":
		// The model's final assistant text. Always shown.
		fmt.Fprintf(w, "\n%v\n", m["content"])

	case "coding.no_progress":
		fmt.Fprintf(w, "  · stopped (no progress in last %v turns)\n", m["window"])

	case "coding.budget_exceeded":
		fmt.Fprintf(w, "  · stopped (budget): %v tokens, $%.4f\n",
			m["cumulative_tokens"], coerceFloat(m["cost_usd"]))

	case "coding.turn_limit":
		fmt.Fprintf(w, "  · stopped (turn limit) after %v turns\n", m["turns"])

	case "coding.error":
		fmt.Fprintf(w, "  ⚠ turn %v error: %v\n", m["turn"], m["error"])

	default:
		if verbose {
			fmt.Fprintf(w, "  · %s: %v\n", kind, payload)
		}
	}
}

// coerceFloat handles the json-decoded float64 / int variants the
// notify payload uses interchangeably. Verbose statements format
// money + cost so a uniform float view keeps the renderer simple.
func coerceFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
