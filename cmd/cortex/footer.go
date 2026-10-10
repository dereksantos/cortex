package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// turnSummary is what the footer under an answer reports about the turn
// (docs/tui-polish.md, track 2). Recorded by turn() on the session; printed
// only by the interactive REPL.
type turnSummary struct {
	Elapsed      time.Duration
	Thought      string // "thought 38s" when the answer step deliberated (thoughtStat)
	Tools        int
	FilesChanged int
	Cost         float64
}

// summarizeTurn counts what a turn did from its messages: tool calls made, and
// the distinct paths written, edited, or removed — counting only calls whose
// result wasn't an error ("Error: …", the coder dispatcher's failure form), so
// an edit that never landed doesn't read as a changed file.
func summarizeTurn(msgs []Message, elapsed time.Duration, cost float64) turnSummary {
	s := turnSummary{Elapsed: elapsed, Cost: cost}
	failed := map[string]bool{}
	for _, m := range msgs {
		if m.Role == RoleTool && m.ToolCallID != "" && strings.HasPrefix(m.Content, "Error: ") {
			failed[m.ToolCallID] = true
		}
	}
	changed := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			s.Tools++
			if failed[tc.ID] {
				continue
			}
			switch tc.Function.Name {
			case FunctionWriteFile, FunctionEditFile, FunctionRemove:
				if p, err := tc.StringArg("path"); err == nil && p != "" {
					changed[p] = true
				}
			}
		}
	}
	s.FilesChanged = len(changed)
	return s
}

// renderFooter is the one dim line under an answer, indented under the
// gutter: "1m12s · thought 38s · 7 tools · 1 file changed · $0.004". Zero
// counts are left out; the gauge isn't repeated — the prompt row right below
// carries it.
func renderFooter(s turnSummary) string {
	parts := []string{fmtTurnElapsed(s.Elapsed)}
	if s.Thought != "" {
		parts = append(parts, s.Thought)
	}
	if s.Tools > 0 {
		parts = append(parts, tools.CountNoun(s.Tools, "tool"))
	}
	if s.FilesChanged > 0 {
		parts = append(parts, tools.CountNoun(s.FilesChanged, "file")+" changed")
	}
	if s.Cost > 0 {
		parts = append(parts, humanCost(s.Cost))
	}
	return gutterIndent + style.Paint(joinDot(parts...), style.Dim)
}

// noteThought keeps the latest step's thought stat for the footer; "" (a step
// that didn't deliberate) leaves an earlier one in place.
func (cs *CortexSession) noteThought(s string) {
	if s != "" {
		cs.turnThought = s
	}
}

// fmtTurnElapsed renders a turn's wall time at the precision a person reads
// it: "0.8s", "20s", "3m05s".
func fmtTurnElapsed(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d/time.Second)%60)
	}
}

// printTurnFooter prints the last turn's footer, unless the session is quiet
// (headless, served) or no turn has completed.
func (cs *CortexSession) printTurnFooter() {
	if cs.quiet || cs.lastTurn == nil {
		return
	}
	fmt.Println(renderFooter(*cs.lastTurn))
}
