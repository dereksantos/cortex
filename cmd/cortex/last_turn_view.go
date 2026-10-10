package main

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// lastTurnView is the last turn's tool calls, unabridged, for the inspector
// (docs/tui-polish.md, tracks 4–5): every call in order — including the ones
// the scrollback folded into a summary line or dropped past a subagent's cap —
// with its line, its diff, and the head of what it returned. Opened by /last
// and by Ctrl-O at the prompt.
type lastTurnView struct {
	calls []tools.CallRecord
}

func (v lastTurnView) Title() string {
	if len(v.calls) == 0 {
		return "last turn"
	}
	return "last turn — " + tools.CountNoun(len(v.calls), "tool call")
}

func (v lastTurnView) Lines(width int) []string {
	if len(v.calls) == 0 {
		return []string{style.Paint("no tool calls yet — this fills in after a turn that uses tools", style.Dim)}
	}
	content := min(width, style.MaxContentWidth)
	var out []string
	for i, c := range v.calls {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, tools.FormatCallLine(c, content))
		out = append(out, c.Diff...)
		indent := tools.GutterPad + strings.Repeat("  ", c.Depth) + "  "
		for _, l := range c.Output {
			l = strings.ReplaceAll(l, "\t", "    ")
			if width > 0 {
				l = style.Clip(l, max(1, width-len(indent)))
			}
			out = append(out, indent+style.Paint(l, style.Dim))
		}
		if c.More > 0 {
			out = append(out, indent+style.Paint(fmt.Sprintf("… %d more lines", c.More), style.Dim))
		}
	}
	return out
}

// openLastTurn shows the last turn's calls in the inspector.
func openLastTurn(editor *lineedit.Terminal, cs *CortexSession) {
	_ = inspectSession(editor, lastTurnView{calls: cs.lastTurnCalls})
}
