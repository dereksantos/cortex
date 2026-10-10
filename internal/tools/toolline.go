package tools

// toolline.go lays out one tool line (docs/tui-polish.md, track 2):
//
//	14:02:19  edit     cmd/cortex/learn.go                         +9 -2
//	14:02:24  bash     go test ./cmd/cortex -run Learn      6.8s  31 lines
//
// The verb sits in a fixed column so a run of calls reads as a list, the
// target follows in the default color, and the result right-aligns at the
// content width (style.ContentWidth, capped at 100) so results form their own
// column. Every line is printed once, when its call finishes, so it can carry
// that result; while a call runs, the REPL's live status row names it.
// Plain text: the column and color carry the structure, no glyphs.

import (
	"strings"

	"github.com/dereksantos/cortex/internal/style"
)

// verbColumn is the width the verb is padded to. Fits every short verb below;
// a longer one (memory_search) just pushes its target right.
const verbColumn = 8

// displayVerbs shortens the model-facing tool names where the short form reads
// as the same thing. Names not listed show as-is.
var displayVerbs = map[string]string{
	FunctionReadFile:  "read",
	FunctionWriteFile: "write",
	FunctionEditFile:  "edit",
	FunctionRemove:    "remove",
	FunctionFetchURL:  "fetch",
	FunctionWebSearch: "search",
}

// splitAction splits an action string like `grep("x", dir)` or
// `read_file(a.go) → skeleton (~40k tokens, too large)` into its verb and its
// target text (the argument list without the outer parens, plus any trailing
// note). An action that isn't name(args)-shaped is all verb.
func splitAction(action string) (verb, target string) {
	open := strings.IndexByte(action, '(')
	if open <= 0 || strings.ContainsRune(action[:open], ' ') {
		return action, ""
	}
	name := action[:open]
	depth, closeAt := 0, -1
	for i := open; i < len(action); i++ {
		switch action[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closeAt = i
			}
		}
		if closeAt >= 0 {
			break
		}
	}
	if v, ok := displayVerbs[name]; ok {
		verb = v
	} else {
		verb = name
	}
	if closeAt < 0 {
		return verb, action[open+1:]
	}
	target = action[open+1 : closeAt]
	if rest := strings.TrimSpace(action[closeAt+1:]); rest != "" {
		target += " " + rest
	}
	return verb, target
}

// ShortAction is an action as the tool lines name it — "read greet.py" for
// "read_file(greet.py)" — for surfaces that label a call in flight (the
// REPL's live status row) so they match the line it will print.
func ShortAction(action string) string {
	verb, target := splitAction(action)
	return strings.TrimSpace(verb + " " + target)
}

// formatToolLine renders one finished tool call at nesting margin indent.
// result is the right-hand column ("+9 -2", "6.8s  31 lines, 1.2 KB"); failed
// paints the verb and result as an error. Width 0 (piped, CI) clips nothing
// and joins the result with two spaces.
func formatToolLine(indent, action, result string, failed bool) string {
	verb, target := splitAction(action)
	pad := ""
	if target != "" {
		pad = strings.Repeat(" ", max(1, verbColumn-len(verb)+1))
	}

	verbRole, resultRole := style.Action, style.Dim
	if failed {
		verbRole, resultRole = style.Err, style.Err
	}

	width := style.ContentWidth()
	if width > 0 {
		avail := width - len(gutterPad) - style.Width(indent+verb+pad)
		// The target names the call, so it outranks the summary: the result
		// keeps at most a third of the room (never under 12 cells), and only
		// then does the target give.
		if limit := max(12, avail/3); result != "" && style.Width(result) > limit {
			result = style.Clip(result, limit)
		}
		room := avail
		if result != "" {
			room -= style.Width(result) + 2
		}
		if target != "" && style.Width(target) > room {
			target = style.Clip(target, max(1, room))
		}
	}

	left := indent + style.Paint(verb, verbRole) + pad + target
	if result == "" {
		return TimestampPrefix() + left
	}
	painted := style.Paint(result, resultRole)
	if width <= 0 {
		return TimestampPrefix() + left + "  " + painted
	}
	return TimestampPrefix() + style.Justify(left, painted, width-len(gutterPad), 2)
}
