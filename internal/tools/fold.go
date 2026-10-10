package tools

// fold.go folds a run of read-only calls into one line (docs/tui-polish.md,
// track 2). Orientation reads — outline, grep, a few read_files — are the
// bulk of most turns' tool calls and the least interesting part of the
// scrollback; folded, a run reads
//
//	14:02:13  outline learn.go · grep 2 searches · read 3 files
//
// The run is held while it lasts and printed when anything else happens: a
// call that doesn't fold, the model's prose or breadcrumb (FlushFold, called
// from cmd/cortex), or the end of the turn. A run of one prints its normal
// line. Only the coder's own calls fold — a subagent's are already counted and
// capped by nesting.go — and a failed call never folds: errors stay visible.
// Off under CORTEX_LOOP_RENDER=0, the plain path.

import (
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

// foldNouns are the tools that fold, and what a count of their calls is
// called in the summary ("read 3 files").
var foldNouns = map[string]string{
	FunctionReadFile:     "files",
	FunctionOutline:      "files",
	FunctionGrep:         "searches",
	FunctionRecall:       "citations",
	FunctionMemoryRead:   "notes",
	FunctionMemorySearch: "searches",
}

// foldEntry is one held call.
type foldEntry struct {
	name, action, result string
	elapsed              time.Duration
}

var fold struct {
	run []foldEntry
}

// actionName is the tool name an action string starts with.
func actionName(action string) string {
	if i := strings.IndexByte(action, '('); i > 0 {
		return action[:i]
	}
	return action
}

// holdForFold keeps a finished call in the current run instead of printing it.
// Reports false — print it normally — for a call that doesn't fold. Caller
// holds nest.mu.
func holdForFold(p *pendingAction, d time.Duration, out string, err error) bool {
	if richRenderDisabled || len(nest.frames) > 0 || err != nil || len(p.extra) > 0 {
		return false
	}
	name := actionName(p.action)
	if _, ok := foldNouns[name]; !ok {
		return false
	}
	fold.run = append(fold.run, foldEntry{name: name, action: p.action, result: callResult(p, d, out, nil), elapsed: d})
	return true
}

// takeFold empties the run and returns it. Caller holds nest.mu.
func takeFold() []foldEntry {
	run := fold.run
	fold.run = nil
	return run
}

// FlushFold prints the held run, if any. cmd/cortex calls it before printing
// anything of its own mid-turn (prose, a breadcrumb) and when a turn ends, so
// the folded line lands where the run happened.
func FlushFold() {
	nest.mu.Lock()
	run := takeFold()
	nest.mu.Unlock()
	printFold(run)
}

// printFold prints a run: a single call as its normal line, more as one
// summary line.
func printFold(run []foldEntry) {
	switch len(run) {
	case 0:
		return
	case 1:
		fmt.Println(formatToolLine("", run[0].action, run[0].result, false))
		return
	}
	fmt.Println(formatFoldLine(run))
}

// formatFoldLine renders a run of two or more as one dim line: each tool in
// order of first use, with its single target or its count, and the run's total
// time on the right once it passes a second.
func formatFoldLine(run []foldEntry) string {
	var order []string
	count := map[string]int{}
	first := map[string]string{}
	var total time.Duration
	for _, e := range run {
		if count[e.name] == 0 {
			order = append(order, e.name)
			_, target := splitAction(e.action)
			first[e.name] = shortTarget(target)
		}
		count[e.name]++
		total += e.elapsed
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		verb, _ := splitAction(name + "()")
		if count[name] == 1 && first[name] != "" {
			parts = append(parts, verb+" "+first[name])
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d %s", verb, count[name], foldNouns[name]))
	}
	summary := strings.Join(parts, " · ")
	result := ""
	if total >= elapsedShownAfter {
		result = fmtElapsed(total)
	}

	width := style.ContentWidth()
	if width > 0 {
		room := width - len(gutterPad)
		if result != "" {
			room -= style.Width(result) + 2
		}
		summary = style.Clip(summary, max(1, room))
	}
	line := style.Paint(summary, style.Dim)
	if result != "" {
		painted := style.Paint(result, style.Dim)
		if width > 0 {
			line = style.Justify(line, painted, width-len(gutterPad), 2)
		} else {
			line += "  " + painted
		}
	}
	return TimestampPrefix() + line
}

// shortTarget is a folded call's target in a few cells: a path's base name,
// the first argument otherwise.
func shortTarget(target string) string {
	t := strings.TrimSpace(target)
	if i := strings.Index(t, ", "); i > 0 {
		t = t[:i]
	}
	if i := strings.IndexByte(t, ':'); i > 0 && !strings.HasPrefix(t, `"`) {
		t = t[:i] // read_file(a.go:40-96) → a.go
	}
	if i := strings.LastIndexByte(t, '/'); i >= 0 && i < len(t)-1 && !strings.HasPrefix(t, `"`) {
		t = t[i+1:]
	}
	return style.Clip(t, 24)
}
