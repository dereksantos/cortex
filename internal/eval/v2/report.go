package eval

import (
	"encoding/json"
	"fmt"
	"io"
)

// Report writes human-readable results to the given writer.
func Report(w io.Writer, results *Results) {
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "Eval Results: Baseline vs Cortex\n")
	fmt.Fprintf(w, "=================================\n\n")

	fmt.Fprintf(w, "Provider: %s\n", results.Provider)
	if results.Model != "" {
		fmt.Fprintf(w, "Model: %s\n", results.Model)
	}
	fmt.Fprintf(w, "\n")

	// Summary
	fmt.Fprintf(w, "Summary\n")
	fmt.Fprintf(w, "-------\n")
	fmt.Fprintf(w, "Avg Baseline Score: %.2f\n", results.AvgBaselineScore)
	fmt.Fprintf(w, "Avg Cortex Score:   %.2f\n", results.AvgCortexScore)
	fmt.Fprintf(w, "Avg Lift:           %+.0f%%\n", results.AvgLift*100)
	fmt.Fprintf(w, "\n")

	// Win/Loss
	fmt.Fprintf(w, "Win/Loss\n")
	fmt.Fprintf(w, "--------\n")
	total := results.TotalCortexWins + results.TotalBaselineWins + results.TotalTies
	fmt.Fprintf(w, "Cortex Wins:   %d/%d (%.0f%%)\n",
		results.TotalCortexWins, total, percent(results.TotalCortexWins, total))
	fmt.Fprintf(w, "Baseline Wins: %d/%d (%.0f%%)\n",
		results.TotalBaselineWins, total, percent(results.TotalBaselineWins, total))
	fmt.Fprintf(w, "Ties:          %d/%d (%.0f%%)\n",
		results.TotalTies, total, percent(results.TotalTies, total))
	fmt.Fprintf(w, "\n")

	// Scenarios
	fmt.Fprintf(w, "Scenarios\n")
	fmt.Fprintf(w, "---------\n")
	for _, s := range results.Scenarios {
		status := "PASS"
		if !s.Pass {
			status = "REGRESS"
		}
		line := fmt.Sprintf("%-35s Lift: %+5.0f%%", truncate(s.ScenarioID, 35), s.AvgLift*100)
		if s.HasABR {
			line += fmt.Sprintf(" ABR: %.2f", s.AvgABR)
		}
		fmt.Fprintf(w, "%s [%s]\n", line, status)
	}
	fmt.Fprintf(w, "\n")

	// ABR by depth (if any)
	for _, s := range results.Scenarios {
		if s.HasABR && len(s.ABRByDepth) > 1 {
			fmt.Fprintf(w, "ABR by Depth (%s)\n", s.ScenarioID)
			fmt.Fprintf(w, "----------------\n")
			for depth := 0; depth < 10; depth++ {
				if abr, ok := s.ABRByDepth[depth]; ok {
					fmt.Fprintf(w, "  Depth %d: %.2f\n", depth, abr)
				}
			}
			fmt.Fprintf(w, "\n")
		}
	}

	// Verdict
	if results.Pass {
		fmt.Fprintf(w, "VERDICT: PASS - Cortex helps or doesn't hurt (lift: %+.0f%%)\n", results.AvgLift*100)
	} else {
		fmt.Fprintf(w, "VERDICT: FAIL - Cortex causes regressions (lift: %+.0f%%)\n", results.AvgLift*100)
	}
}

// ReportJSON writes JSON results to the given writer.
func ReportJSON(w io.Writer, results *Results) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

// ReportSummary writes a brief one-line summary.
func ReportSummary(w io.Writer, results *Results) {
	status := "PASS"
	if !results.Pass {
		status := "FAIL"
		fmt.Fprintf(w, "Lift: %+.0f%% [%s] - Cortex: %d wins, Baseline: %d wins\n",
			results.AvgLift*100, status, results.TotalCortexWins, results.TotalBaselineWins)
		return
	}
	fmt.Fprintf(w, "Lift: %+.0f%% [%s] - Cortex: %d wins, Baseline: %d wins\n",
		results.AvgLift*100, status, results.TotalCortexWins, results.TotalBaselineWins)
}

// ReportTrend writes lift trend over recent runs.
func ReportTrend(w io.Writer, lifts []float64) {
	if len(lifts) == 0 {
		fmt.Fprintf(w, "No previous runs.\n")
		return
	}

	fmt.Fprintf(w, "Lift Trend (last %d runs)\n", len(lifts))
	fmt.Fprintf(w, "-------------------------\n")

	// ASCII chart - show lift as bar from -100% to +100%
	for i, lift := range lifts {
		bar := liftBar(lift)
		fmt.Fprintf(w, "%2d: %s %+.0f%%\n", i+1, bar, lift*100)
	}

	// Trend direction
	if len(lifts) >= 2 {
		first := lifts[0]
		last := lifts[len(lifts)-1]
		diff := last - first
		if diff > 0.05 {
			fmt.Fprintf(w, "\nTrend: IMPROVING\n")
		} else if diff < -0.05 {
			fmt.Fprintf(w, "\nTrend: DECLINING\n")
		} else {
			fmt.Fprintf(w, "\nTrend: STABLE\n")
		}
	}
}

func percent(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// liftBar creates an ASCII bar for lift visualization
// Negative lift shows bars going left, positive going right
func liftBar(lift float64) string {
	// Scale: -1.0 to +1.0 maps to -20 to +20 chars
	width := 20
	center := width

	// Clamp lift to -1.0 to +1.0
	if lift > 1.0 {
		lift = 1.0
	}
	if lift < -1.0 {
		lift = -1.0
	}

	bar := make([]byte, width*2+1)
	for i := range bar {
		bar[i] = ' '
	}
	bar[center] = '|' // Center marker

	if lift >= 0 {
		// Positive: fill right of center
		chars := int(lift * float64(width))
		for i := 0; i < chars; i++ {
			bar[center+1+i] = '+'
		}
	} else {
		// Negative: fill left of center
		chars := int(-lift * float64(width))
		for i := 0; i < chars; i++ {
			bar[center-1-i] = '-'
		}
	}

	return "[" + string(bar) + "]"
}
