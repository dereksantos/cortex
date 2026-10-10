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

var turnLog struct {
	mu    sync.Mutex
	calls []CallRecord
}

// StartTurnLog clears the record for a new turn.
func StartTurnLog() {
	turnLog.mu.Lock()
	defer turnLog.mu.Unlock()
	turnLog.calls = nil
}

// TurnLog returns the current turn's calls so far.
func TurnLog() []CallRecord {
	turnLog.mu.Lock()
	defer turnLog.mu.Unlock()
	return append([]CallRecord(nil), turnLog.calls...)
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
	turnLog.calls = append(turnLog.calls, rec)
	turnLog.mu.Unlock()
}
