package tools

// nesting.go makes a subagent's work legible as a subagent's work.
//
// study and agent run their own multi-step tool loops on the shared engine
// (cmd/cortex/loop.go), but every call in those loops printed through the same
// printToolAction as the coder's own — flat, at the same margin, so a study
// that ran seven greps looked exactly like the coder running seven greps.
// Here a subagent's calls are indented one level per depth, and each one is
// announced ONCE, on completion, carrying its elapsed time and a one-line
// summary of what came back.
//
// Announcing on completion (rather than the coder's print-then-run) is what
// keeps a busy subagent readable: one line per call instead of two, with the
// two facts you actually want from a black box — how long, and what came out.
// The coder's own calls keep printing before they run, since there they are
// the live "something is happening" signal; inside a subagent that signal is
// already carried by the run line and the coder's own activity row.
//
// Plain text, per the 2026-07-19 de-glyph decision: indentation alone carries
// the tree. No connectors, no rules, no icons — two spaces per level.
//
// The state is a package global for the same reason internal/tools' Limits is
// (see limits.go): printToolAction is called deep inside free functions with
// no display parameter to thread, and there is exactly one terminal. It is
// mutex-guarded, and the only non-quiet consumer is the interactive REPL,
// which runs one turn — and one tool call within it — at a time; every
// concurrent host (serve, discord, headless turn) is Quiet() and prints
// nothing at all.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

// nestedCallDisplayCap bounds how many of one subagent's calls are printed. A
// bounded subagent rarely exceeds it (Study MaxIter 12, Agent 20), but a
// multi-call batch on every round can; past the cap the lines stop and the
// count is reported on the subagent's done line, so a chatty run costs a
// fixed number of rows instead of an unbounded scroll.
const nestedCallDisplayCap = 24

// summaryTextCap bounds a one-line result echoed verbatim into the summary,
// before the terminal-width clip applies on top.
const summaryTextCap = 60

// nestFrame is one in-flight subagent run: what it prints for its children
// and what it will report when it finishes.
type nestFrame struct {
	name       string
	indent     string // the margin this frame's CHILD calls print at
	start      time.Time
	calls      int
	suppressed int
}

// pendingAction is a call's announcement, held back until the call finishes
// so its line can carry the elapsed time and result. extra collects anything
// the tool printed meanwhile (a file diff), which must land AFTER the
// announcement it belongs to; result, when set, replaces the generic summary
// (a diff's "+9 -2").
type pendingAction struct {
	indent string
	action string
	extra  []string
	result string
}

var nest struct {
	mu      sync.Mutex
	frames  []*nestFrame
	pending *pendingAction
}

// pushNest opens a subagent frame; its children print one level deeper.
func pushNest(name string) *nestFrame {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	f := &nestFrame{name: name, start: time.Now()}
	nest.frames = append(nest.frames, f)
	f.indent = strings.Repeat("  ", len(nest.frames))
	return f
}

// popNest closes the innermost subagent frame.
func popNest() {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	if n := len(nest.frames); n > 0 {
		nest.frames = nest.frames[:n-1]
	}
	nest.pending = nil
}

// inSubagent reports whether the current tool call is running inside one.
func inSubagent() bool {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	return len(nest.frames) > 0
}

// indentPrefix is the margin the CURRENT level prints at: "" for the coder's
// own calls, two spaces per enclosing subagent below that.
func indentPrefix() string {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	return strings.Repeat("  ", len(nest.frames))
}

// IndentPrefix exposes the current nesting margin to the composition root, so
// the lines cmd/cortex prints around a subagent run (its "run: <name> via
// <model>" banner) sit in the same column as the tool lines below them.
func IndentPrefix() string { return indentPrefix() }

// captureAction holds a call's action line back until the call finishes, at
// every level (track 2: each line carries its result). A second action from
// the same call (bash spilling its output to the summarizer, say) flushes the
// first rather than losing it.
func captureAction(action string) {
	nest.mu.Lock()
	prev := nest.pending
	nest.pending = &pendingAction{indent: strings.Repeat("  ", len(nest.frames)), action: action}
	var run []foldEntry
	if prev != nil {
		run = takeFold()
	}
	nest.mu.Unlock()
	if prev != nil {
		printFold(run)
		printPending(prev, "", false)
	}
}

// flushAction prints the held-back line now, without a result. A subagent
// call uses it: its line must come out before the nested calls it runs, and
// its result arrives on the done line instead.
func flushAction() {
	nest.mu.Lock()
	p := nest.pending
	nest.pending = nil
	run := takeFold()
	nest.mu.Unlock()
	printFold(run)
	if p != nil {
		printPending(p, "", false)
	}
}

// setPendingResult overrides the generic result summary for the call in
// flight — a diff reports its +/- counts.
func setPendingResult(result string) {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	if nest.pending != nil {
		nest.pending.result = result
	}
}

// captureExtra attaches already-rendered lines (a file diff) to the pending
// announcement so they print under it, not before it. Returns false when there
// is nothing pending — the top level, where they print directly.
func captureExtra(lines []string) bool {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	if nest.pending == nil {
		return false
	}
	nest.pending.extra = append(nest.pending.extra, lines...)
	return true
}

// beginNestedCall counts a call against the innermost frame.
func beginNestedCall() {
	nest.mu.Lock()
	defer nest.mu.Unlock()
	if n := len(nest.frames); n > 0 {
		nest.frames[n-1].calls++
	}
}

// finishCall prints the call's one line — action, elapsed, result — plus
// whatever it buffered. Inside a subagent, past the display cap the line is
// dropped and counted for the done line instead.
func finishCall(d time.Duration, out string, err error) {
	nest.mu.Lock()
	p := nest.pending
	nest.pending = nil
	over := false
	if n := len(nest.frames); n > 0 {
		f := nest.frames[n-1]
		if f.calls > nestedCallDisplayCap {
			over = true
			if p != nil {
				f.suppressed++
			}
		}
	}
	held := false
	var run []foldEntry
	if p != nil && !over {
		if held = holdForFold(p, d, out, err); !held {
			run = takeFold()
		}
	}
	nest.mu.Unlock()
	if p == nil || over || held {
		return
	}
	printFold(run) // a read-only run before this call lands above it
	printPending(p, callResult(p, d, out, err), err != nil)
}

// callResult is the right-hand column for a finished call: the tool's own
// result when it set one (a diff's counts), else a one-line summary of what
// came back — skipped when a diff already shows the change in full — led by
// the elapsed time once a call takes long enough for it to matter.
func callResult(p *pendingAction, d time.Duration, out string, err error) string {
	result := p.result
	switch {
	case err != nil:
		result = summarizeResult(out, err)
	case result == "" && len(p.extra) == 0:
		result = summarizeResult(out, nil)
	}
	if d >= elapsedShownAfter {
		result = strings.TrimSpace(fmtElapsed(d) + "  " + result)
	}
	return result
}

// elapsedShownAfter is how long a call runs before its line shows the time; a
// column of "3ms" on every read is noise.
const elapsedShownAfter = time.Second

// printPending emits a held-back announcement and its buffered lines.
func printPending(p *pendingAction, result string, failed bool) {
	fmt.Println(formatToolLine(p.indent, p.action, result, failed))
	for _, l := range p.extra {
		fmt.Println(l)
	}
}

// printSubagentDone closes a subagent block with what the run cost and what it
// produced — the counterpart to the action line that opened it.
func printSubagentDone(deps Quieter, f *nestFrame, digest string, err error) {
	if deps.Quiet() {
		return
	}
	fmt.Println(formatSubagentDone(f, time.Since(f.start), digest, err))
}

// formatSubagentDone renders the closing line of a subagent block: calls,
// elapsed, anything suppressed, and the digest size or the error.
func formatSubagentDone(f *nestFrame, elapsed time.Duration, digest string, err error) string {
	parts := []string{CountNoun(f.calls, "call"), fmtElapsed(elapsed)}
	if f.suppressed > 0 {
		parts = append(parts, fmt.Sprintf("%d not shown", f.suppressed))
	}
	if err != nil {
		parts = append(parts, "error: "+style.Clip(firstLine(err.Error()), summaryTextCap))
	} else {
		parts = append(parts, "digest "+humanSize(len(digest)))
	}
	plain := f.indent + f.name + " done: " + strings.Join(parts, ", ")
	if w := style.ContentWidth(); w > 0 {
		plain = style.Clip(plain, w-len(gutterPad))
	}
	return TimestampPrefix() + style.Paint(plain, style.Dim)
}

// subagentAction renders the parent-level action line for a subagent call. The
// goal is what distinguishes two studies of the same path, so it rides along,
// clipped.
func subagentAction(name, path, goal string) string {
	if g := strings.TrimSpace(firstLine(goal)); g != "" {
		return fmt.Sprintf("%s(%s, %s)", name, path, style.Clip(g, 48))
	}
	return fmt.Sprintf("%s(%s)", name, path)
}

// summarizeResult reduces a tool result to the one line printed beside its
// elapsed time: the result itself when it's short enough to just show, its
// shape otherwise.
func summarizeResult(out string, err error) string {
	if err != nil {
		return "error: " + style.Clip(firstLine(err.Error()), summaryTextCap)
	}
	t := strings.TrimSpace(out)
	if t == "" {
		return "no output"
	}
	if n := strings.Count(t, "\n") + 1; n > 1 {
		return CountNoun(n, "line") + ", " + humanSize(len(out))
	}
	// Collapse whitespace: a tab or control byte in tool output must not land
	// raw in the result column.
	return style.Clip(strings.Join(strings.Fields(t), " "), summaryTextCap)
}

// fmtElapsed renders a call's wall time at the precision that reads: whole
// milliseconds under a second, tenths of a second under a minute.
func fmtElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d/time.Second)%60)
	}
}

// humanSize renders a byte count compactly.
func humanSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
