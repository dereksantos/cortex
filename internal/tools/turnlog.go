package tools

// turnlog.go keeps the full record of the current turn's tool calls for the
// REPL's last-turn detail view (docs/tui-polish.md, tracks 4–5). The
// scrollback shows a turn compactly — folded read runs, one summary line per
// call, nested calls capped — and this is where the unabridged version lives:
// every call, in order, with its line, its diff, and the head of its output.

import (
	"strings"
	"sync"
	"time"
)

// CallRecord is one finished tool call.
type CallRecord struct {
	At     time.Time
	Depth  int    // 0 for the coder's own calls, 1+ inside subagents
	Action string // as announced: "read_file(a.go:1-40)"
	Result string // the line's right-hand column
	Failed bool
	Diff   []string // rendered diff rows, when the call changed a file
	Output []string // the head of what the call returned, ≤ recordOutputLines
	More   int      // output lines past the head
}

// recordOutputLines bounds how much of each call's output the record keeps.
const recordOutputLines = 40

// turnLog is the interactive turn in progress: whether one is active (only
// then are calls recorded — a one-off `cortex study` or study-eval never grows
// it), and whether it has a live status row (only then are top-level lines
// held until the call finishes and read runs folded — without one, a held line
// would leave the screen silent for as long as the call runs).
var turnLog struct {
	mu     sync.Mutex
	active bool
	live   bool
	calls  []CallRecord
}

// BeginTurn starts recording a REPL turn. live reports that the REPL shows a
// running call on its status row (the anchored prompt), which is what makes
// holding a line until the call finishes safe.
func BeginTurn(live bool) {
	turnLog.mu.Lock()
	defer turnLog.mu.Unlock()
	turnLog.active, turnLog.live, turnLog.calls = true, live, nil
}

// EndTurn prints any read run still held, stops recording, and returns the
// turn's calls.
func EndTurn() []CallRecord {
	FlushFold()
	turnLog.mu.Lock()
	defer turnLog.mu.Unlock()
	turnLog.active, turnLog.live = false, false
	return append([]CallRecord(nil), turnLog.calls...)
}

// liveTurn reports whether lines may be held and runs folded.
func liveTurn() bool {
	turnLog.mu.Lock()
	defer turnLog.mu.Unlock()
	return turnLog.active && turnLog.live
}

// recordCall appends a finished call. Called by finishCall for every call that
// was announced (non-quiet), whether or not its line printed — folded and
// past-the-cap calls are exactly what the record is for.
func recordCall(p *pendingAction, depth int, result string, out string, err error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.TrimSpace(out) == "" {
		lines = nil
	}
	more := 0
	if len(lines) > recordOutputLines {
		more = len(lines) - recordOutputLines
		lines = lines[:recordOutputLines]
	}
	if err != nil {
		lines = append([]string{"error: " + err.Error()}, lines...)
	}
	rec := CallRecord{
		At: Now(), Depth: depth, Action: p.action, Result: result, Failed: err != nil,
		Diff: append([]string(nil), p.extra...), Output: lines, More: more,
	}
	turnLog.mu.Lock()
	if turnLog.active {
		turnLog.calls = append(turnLog.calls, rec)
	}
	turnLog.mu.Unlock()
}
