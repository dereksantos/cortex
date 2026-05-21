package repltui

import (
	"fmt"
	"io"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dereksantos/cortex/pkg/cliout"
)

// TUISink is the Bubble Tea-backed implementation of cliout.Sink.
// Write methods (Info / Warn / Error / Event / Banner) translate into
// tea.Msg values and push them onto the Bubble Tea program via
// p.Send so the Update handler renders them.
//
// ReadLine is the trickier half. The REPL's main loop expects a
// blocking call that returns when the user submits one line of
// input. Bubble Tea's input is asynchronous (Update sees a
// tea.KeyMsg, then later another, then Enter). Bridge: ReadLine
// pushes a promptRequestedMsg so the View can update the prompt
// prefix, then blocks on inputCh until the View's Enter handler
// pushes the typed text. Ctrl-D / Ctrl-C deliver an io.EOF response
// so callers can distinguish "user submitted empty" from "user
// quit."
//
// The Program is set after construction (SetProgram) because the
// chicken-and-egg ordering between TUISink and tea.NewProgram makes
// constructor injection awkward. Calls into the sink before
// SetProgram silently drop — used by tests that exercise the
// channel side without spinning a real program.
type TUISink struct {
	program atomic.Pointer[tea.Program]

	// inputCh delivers submitted input lines from the Update handler
	// to ReadLine. Buffered=1 so a fast typist doesn't block the
	// View. Drained one-at-a-time by ReadLine; multiple concurrent
	// ReadLines would interleave (mid-turn user gate + main loop
	// can't both be reading) but the REPL's structure guarantees
	// one reader at a time.
	inputCh chan inputResponse

	verbose bool
}

// inputResponse carries one line of submitted input from Update
// down to ReadLine. err is io.EOF on Ctrl-D / program quit; nil
// otherwise. text is the typed line with trailing newlines
// stripped; may be empty when the user just hit Enter.
type inputResponse struct {
	text string
	err  error
}

// NewTUISink constructs a sink. verbose mirrors the REPL's
// --verbose flag — the Update handler may gate which event kinds
// it renders based on it.
func NewTUISink(verbose bool) *TUISink {
	return &TUISink{
		inputCh: make(chan inputResponse, 1),
		verbose: verbose,
	}
}

// SetProgram wires the sink to its Bubble Tea program. Called once
// during Run after tea.NewProgram returns; nil clears the wiring
// (only useful in tests).
func (s *TUISink) SetProgram(p *tea.Program) { s.program.Store(p) }

// SetVerbose flips the verbose-rendering flag at runtime. The
// Update handler reads this on the next Event message to decide
// whether to show verbose-gated kinds (coding.turn,
// coding.tool_result, coding.session_start).
//
// Calls are serialized by the same Bubble Tea send pipeline as
// other state changes — we push a verbosityMsg so the Model
// observes the flip in its message order rather than racing the
// next Event read.
func (s *TUISink) SetVerbose(v bool) {
	s.verbose = v
	s.send(verbosityMsg{verbose: v})
}

// send is the shared helper that drops the message when the
// program isn't wired yet. Avoids panics in test setups that
// build the sink but never start a program.
func (s *TUISink) send(m tea.Msg) {
	if p := s.program.Load(); p != nil {
		p.Send(m)
	}
}

// Info implements cliout.Sink.
func (s *TUISink) Info(msg string) { s.send(infoMsg{text: msg}) }

// Warn implements cliout.Sink.
func (s *TUISink) Warn(msg string) { s.send(warnMsg{text: msg}) }

// Error implements cliout.Sink.
func (s *TUISink) Error(err error) {
	if err == nil {
		return
	}
	s.send(errMsg{text: err.Error()})
}

// Event implements cliout.Sink. The payload type matches the
// harness Notify hook; the Update handler casts based on `kind`.
//
// Two kinds get special-case routing into typed messages so the
// Update handler can react without re-parsing a generic any:
//
//   - "dag.trace"        → dagTraceMsg (per-node DAG trace line)
//   - "bootstrap.progress" → bootstrapProgressMsg (ambient row)
//
// Everything else flows as a generic eventMsg and the renderEventLine
// switch decides how to display it.
func (s *TUISink) Event(kind string, payload any) {
	switch kind {
	case "dag.trace":
		if t, ok := toDagTraceMsg(payload); ok {
			s.send(t)
			return
		}
	case "bootstrap.progress":
		if t, ok := toBootstrapProgressMsg(payload); ok {
			s.send(t)
			return
		}
	}
	s.send(eventMsg{kind: kind, payload: payload})
}

// toDagTraceMsg coerces the loosely-typed payload map the
// makeREPLDAGTracer adapter emits into the typed message shape.
// Returns (msg, true) on a clean parse; (zero, false) when the
// payload isn't the expected shape so the caller can fall through
// to the generic eventMsg path.
func toDagTraceMsg(payload any) (dagTraceMsg, bool) {
	m, ok := payload.(map[string]any)
	if !ok {
		return dagTraceMsg{}, false
	}
	q, _ := m["qualified_name"].(string)
	id, _ := m["node_id"].(string)
	okFlag, _ := m["ok"].(bool)
	latency, _ := m["latency_ms"].(int)
	cause, _ := m["err_cause"].(string)
	var spawned []string
	if sp, ok := m["spawned"].([]string); ok {
		spawned = sp
	}
	return dagTraceMsg{
		QualifiedName: q,
		NodeID:        id,
		OK:            okFlag,
		LatencyMs:     latency,
		Spawned:       spawned,
		ErrCause:      cause,
	}, true
}

func toBootstrapProgressMsg(payload any) (bootstrapProgressMsg, bool) {
	m, ok := payload.(map[string]any)
	if !ok {
		return bootstrapProgressMsg{}, false
	}
	line, _ := m["line"].(string)
	done, _ := m["done"].(bool)
	return bootstrapProgressMsg{Line: line, Done: done}, true
}

// Banner implements cliout.Sink.
func (s *TUISink) Banner(text string) { s.send(bannerMsg{text: text}) }

// ReadLine blocks until the user submits a line in the TUI. Returns
// the submitted text (newlines stripped) or ("", io.EOF) on
// Ctrl-D / quit. Concurrent ReadLines from the same sink would race
// on inputCh; the REPL's structure guarantees serial reads.
func (s *TUISink) ReadLine(prompt string) (string, error) {
	s.send(promptRequestedMsg{prompt: prompt})
	resp, ok := <-s.inputCh
	if !ok {
		return "", io.EOF
	}
	return resp.text, resp.err
}

// deliverInput is the Update handler's hook back into the sink: it
// pushes one inputResponse onto inputCh. Closes the channel on EOF
// so a pending ReadLine returns immediately and any subsequent
// ReadLines see "channel closed" (also io.EOF).
//
// Not exported because only the Model in the same package calls it.
func (s *TUISink) deliverInput(text string, err error) {
	select {
	case s.inputCh <- inputResponse{text: text, err: err}:
	default:
		// inputCh is full — a previous ReadLine never drained it,
		// or two ReadLines raced. Drop the new value rather than
		// blocking the Update goroutine (which would freeze the
		// whole TUI). This is defensive; the REPL's call structure
		// shouldn't trigger it.
	}
}

// Close signals end-of-input to any blocked ReadLine. Called by
// Run when the program quits so the REPL goroutine can exit cleanly
// rather than hanging on a channel read.
func (s *TUISink) Close() {
	// Drain in flight then close so existing waiters see io.EOF.
	close(s.inputCh)
}

// compile-time check that TUISink satisfies cliout.Sink.
var _ cliout.Sink = (*TUISink)(nil)

// shut up the unused-import linter when fmt is only needed in tests.
var _ = fmt.Sprintf
