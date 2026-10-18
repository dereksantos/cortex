// Package presence reports the agent's lifecycle to a terminal host that
// tracks running coding agents — today Supacode, whose sidebar shows an
// "active session" badge per agent and posts a notification when a turn ends.
//
// The transport is OSC 3008 (the UAPI hierarchical context signal), written
// to the controlling terminal:
//
//	ESC ] 3008 ; start=cortex ; event=<event> [; pid=<pid>] ESC \
//	ESC ] 3008 ; start=cortex ; kind=notify ; title=<b64> ; body=<b64> ESC \
//
// Attribution is by the terminal surface the bytes arrive on, so no surface
// id is carried, and the sequence also works over SSH. Any other terminal
// ignores OSC 3008, but emission is still gated on SUPACODE_SURFACE_ID so
// nothing is written outside a Supacode surface at all. The pid rides only
// when SUPACODE_SOCKET_PATH is set (the host is local), so the host's
// liveness sweep can clear the badge of a cortex that died without
// reporting session_end. The wire shape is Supacode's AgentPresenceOSC
// (SupacodeSettingsShared/BusinessLogic/AgentPresenceOSC.swift).
//
// Every method is safe on a nil *Emitter — the "not under Supacode" case —
// and every write is best-effort: a failed write is dropped silently, since
// the REPL owns the terminal and a stray warning would tear its prompt.
package presence

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Agent is the context id the host keys the badge on.
const Agent = "cortex"

// Event is one presence state, spelled as the host's HookEvent raw values;
// the host drops any other value.
type Event string

const (
	SessionStart  Event = "session_start"
	SessionEnd    Event = "session_end"
	Busy          Event = "busy"
	AwaitingInput Event = "awaiting_input"
	Idle          Event = "idle"
	Error         Event = "error"
	Compacting    Event = "compacting"
)

// Byte budgets for the notify fields, matching the host's emit-side caps:
// the whole sequence must stay under libghostty's 2048-byte OSC buffer, over
// which it is discarded rather than truncated.
const (
	notifyTitleBudget = 160
	notifyBodyBudget  = 1000
)

const (
	surfaceEnv = "SUPACODE_SURFACE_ID"
	socketEnv  = "SUPACODE_SOCKET_PATH"
)

// Emitter writes presence signals to the controlling terminal. The zero
// value is not usable; build one with FromEnv.
type Emitter struct {
	mu   sync.Mutex
	open func() (io.WriteCloser, error)
	pid  int // 0 omits the pid field
	last Event
}

// FromEnv returns an Emitter for this process when it runs on a Supacode
// surface, else nil (whose methods are no-ops).
func FromEnv() *Emitter {
	return newEmitter(os.Getenv, openTTY, os.Getpid())
}

func newEmitter(getenv func(string) string, open func() (io.WriteCloser, error), pid int) *Emitter {
	if getenv(surfaceEnv) == "" {
		return nil
	}
	if getenv(socketEnv) == "" {
		pid = 0
	}
	return New(open, pid)
}

// New returns an Emitter that writes each sequence to a writer from open,
// carrying pid when it is positive. FromEnv is the production constructor;
// New lets a caller (or a test) supply the terminal.
func New(open func() (io.WriteCloser, error), pid int) *Emitter {
	e := &Emitter{open: open}
	if pid > 0 {
		e.pid = pid
	}
	return e
}

func openTTY() (io.WriteCloser, error) {
	return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
}

// Emit reports ev. A repeat of the last event emitted is skipped — a plan
// run starts each step as its own turn, and each would otherwise re-send
// busy.
func (e *Emitter) Emit(ev Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ev == e.last {
		return
	}
	e.last = ev
	e.write(presenceSequence(ev, e.pid))
}

// Notify posts a notification. An empty title makes the host fall back to
// the agent's name; the body may be empty.
func (e *Emitter) Notify(title, body string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.write(notifySequence(title, body))
}

// write sends seq in one Write call (os.File.Write retries short writes
// itself), so a half sequence never reaches the terminal parser.
func (e *Emitter) write(seq string) {
	w, err := e.open()
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, seq)
	_ = w.Close()
}

func presenceSequence(ev Event, pid int) string {
	action := "start"
	if ev == SessionEnd {
		action = "end"
	}
	meta := "event=" + string(ev)
	if pid > 0 {
		meta += ";pid=" + strconv.Itoa(pid)
	}
	return "\x1b]3008;" + action + "=" + Agent + ";" + meta + "\x1b\\"
}

func notifySequence(title, body string) string {
	meta := "kind=notify" +
		";title=" + notifyField(title, notifyTitleBudget) +
		";body=" + notifyField(body, notifyBodyBudget)
	return "\x1b]3008;start=" + Agent + ";" + meta + "\x1b\\"
}

// notifyField is the host's notify value encoding: the text JSON-string
// escaped (without the surrounding quotes), capped at budget bytes, then
// standard base64. The cap is applied a rune at a time so it never splits a
// UTF-8 sequence or an escape — the host would recover from either, but
// only by dropping bytes.
func notifyField(s string, budget int) string {
	var b strings.Builder
	for _, r := range s {
		esc := jsonEscape(string(r))
		if b.Len()+len(esc) > budget {
			break
		}
		b.WriteString(esc)
	}
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// jsonEscape is s as a JSON string literal minus its quotes. HTML escaping
// is off: "<" costs one byte of the budget instead of six.
func jsonEscape(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	out := strings.TrimSuffix(b.String(), "\n")
	return out[1 : len(out)-1]
}
