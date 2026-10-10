package main

import (
	"fmt"
	"time"

	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// turnSummary is what the footer under an answer reports about the turn
// (docs/tui-polish.md, track 2). Recorded by turn() on the session; printed
// only by the interactive REPL.
type turnSummary struct {
	Elapsed      time.Duration
	Tools        int
	FilesChanged int
	Cost         float64
}

// summarizeTurn counts what a turn did from its messages: tool calls made, and
// the distinct paths written, edited, or removed.
func summarizeTurn(msgs []Message, elapsed time.Duration, cost float64) turnSummary {
	s := turnSummary{Elapsed: elapsed, Cost: cost}
	changed := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			s.Tools++
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
// gutter: "20s · 7 tools · 1 file changed · 24k|131k · $0.004". gauge is the
// prompt bar's gauge (pre-colored); zero counts are left out.
func renderFooter(s turnSummary, gauge string) string {
	parts := []string{fmtTurnElapsed(s.Elapsed)}
	if s.Tools > 0 {
		parts = append(parts, tools.CountNoun(s.Tools, "tool"))
	}
	if s.FilesChanged > 0 {
		parts = append(parts, tools.CountNoun(s.FilesChanged, "file")+" changed")
	}
	line := style.Paint(joinDot(parts...), style.Dim)
	if gauge != "" {
		line += style.Paint(" · ", style.Dim) + gauge
	}
	if s.Cost > 0 {
		line += style.Paint(" · "+humanCost(s.Cost), style.Dim)
	}
	return gutterIndent + line
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
	fmt.Println(renderFooter(*cs.lastTurn, cs.coloredGauge(promptGaugeCells, cs.windowSize())))
}
