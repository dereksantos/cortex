package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
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

	if results.AvgABR > 0 {
		fmt.Fprintf(w, "Avg ABR:            %.2f\n", results.AvgABR)
	}
	fmt.Fprintf(w, "\n")

	// Token usage
	if results.TotalBaselineTokens > 0 || results.TotalCortexTokens > 0 {
		fmt.Fprintf(w, "Tokens\n")
		fmt.Fprintf(w, "------\n")
		fmt.Fprintf(w, "Baseline: %d → Cortex: %d (%.0f%% reduction)\n",
			results.TotalBaselineTokens, results.TotalCortexTokens, results.AvgTokenReduction*100)
		fmt.Fprintf(w, "\n")
	}

	// Model Parity (only when compare provider is set)
	if results.CompareModel != "" {
		fmt.Fprintf(w, "Model Parity (%s + Cortex vs %s)\n", results.Model, results.CompareModel)
		fmt.Fprintf(w, "%s\n", strings.Repeat("-", 50))
		fmt.Fprintf(w, "Small + Cortex:    %.2f\n", results.AvgCortexScore)
		fmt.Fprintf(w, "Frontier (no ctx): %.2f\n", results.AvgCompareScore)
		fmt.Fprintf(w, "MPR:               %.2f\n", results.AvgMPR)
		if results.TotalCompareTokens > 0 {
			fmt.Fprintf(w, "Compare tokens:    %d\n", results.TotalCompareTokens)
		}
		fmt.Fprintf(w, "\n")
	}

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

	// Judge scores (if enabled)
	hasJudge := false
	var avgBaselineCorrect, avgCortexCorrect float64
	var avgBaselineUnderstand, avgCortexUnderstand float64
	var avgBaselineHallucinate, avgCortexHallucinate float64
	judgeCount := 0
	for _, s := range results.Scenarios {
		for _, t := range s.Tests {
			if t.JudgeUsed {
				hasJudge = true
				avgBaselineCorrect += t.BaselineJudgeCorrectness
				avgCortexCorrect += t.CortexJudgeCorrectness
				avgBaselineUnderstand += t.BaselineJudgeUnderstanding
				avgCortexUnderstand += t.CortexJudgeUnderstanding
				avgBaselineHallucinate += t.BaselineJudgeHallucination
				avgCortexHallucinate += t.CortexJudgeHallucination
				judgeCount++
			}
		}
	}
	if hasJudge && judgeCount > 0 {
		n := float64(judgeCount)
		fmt.Fprintf(w, "LLM Judge Scores\n")
		fmt.Fprintf(w, "----------------\n")
		fmt.Fprintf(w, "                    Baseline    Cortex\n")
		fmt.Fprintf(w, "Correctness:        %.2f        %.2f\n", avgBaselineCorrect/n, avgCortexCorrect/n)
		fmt.Fprintf(w, "Understanding:      %.2f        %.2f\n", avgBaselineUnderstand/n, avgCortexUnderstand/n)
		fmt.Fprintf(w, "Hallucination:      %.2f        %.2f\n", avgBaselineHallucinate/n, avgCortexHallucinate/n)
		fmt.Fprintf(w, "\n")
	}

	// Scenarios
	fmt.Fprintf(w, "Scenarios\n")
	fmt.Fprintf(w, "---------\n")
	for _, s := range results.Scenarios {
		status := "PASS"
		if !s.Pass {
			status = "REGRESS"
		}
		line := fmt.Sprintf("%-35s Lift: %+5.0f%%", truncate(s.ScenarioID, 35), s.AvgLift*100)
		if s.HasRanking && s.AvgABR > 0 {
			line += fmt.Sprintf("  ABR: %.2f", s.AvgABR)
		}
		fmt.Fprintf(w, "%s [%s]\n", line, status)
	}
	fmt.Fprintf(w, "\n")

	// NDCG by depth (if any)
	for _, s := range results.Scenarios {
		if s.HasRanking && len(s.NDCGByDepth) > 1 {
			fmt.Fprintf(w, "Retrieval NDCG by Depth (%s)\n", s.ScenarioID)
			fmt.Fprintf(w, "----------------------------\n")
			for depth := 0; depth < 10; depth++ {
				if ndcg, ok := s.NDCGByDepth[depth]; ok {
					fmt.Fprintf(w, "  Depth %d: %.2f\n", depth, ndcg)
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
		status = "FAIL"
	}
	line := fmt.Sprintf("Lift: %+.0f%% [%s] - Cortex: %d wins, Baseline: %d wins",
		results.AvgLift*100, status, results.TotalCortexWins, results.TotalBaselineWins)
	if results.AvgABR > 0 {
		line += fmt.Sprintf(" - ABR: %.2f", results.AvgABR)
	}
	if results.AvgMPR > 0 {
		line += fmt.Sprintf(" - MPR: %.2f", results.AvgMPR)
	}
	fmt.Fprintf(w, "%s\n", line)
}

// ReportABRTrend writes ABR trend over recent runs.
func ReportABRTrend(w io.Writer, points []ABRTrendPoint) {
	if len(points) == 0 {
		fmt.Fprintf(w, "No ABR data from previous runs.\n")
		return
	}

	fmt.Fprintf(w, "ABR Trend (last %d runs)\n", len(points))
	fmt.Fprintf(w, "========================\n")

	// ASCII chart - show ABR as bar from 0.0 to 1.0, with target line at 0.9
	for i, pt := range points {
		bar := abrBar(pt.AvgABR)
		sha := ""
		if len(pt.GitCommitSHA) >= 7 {
			sha = " " + pt.GitCommitSHA[:7]
		}
		fmt.Fprintf(w, "%2d: %s %.2f%s\n", i+1, bar, pt.AvgABR, sha)
	}

	// Trend direction
	if len(points) >= 2 {
		first := points[0].AvgABR
		last := points[len(points)-1].AvgABR
		diff := last - first
		if diff > 0.05 {
			fmt.Fprintf(w, "\nTrend: IMPROVING\n")
		} else if diff < -0.05 {
			fmt.Fprintf(w, "\nTrend: DECLINING\n")
		} else {
			fmt.Fprintf(w, "\nTrend: STABLE\n")
		}
	}

	// Target comparison
	latest := points[len(points)-1].AvgABR
	if latest >= 0.9 {
		fmt.Fprintf(w, "Status: TARGET MET (ABR >= 0.9)\n")
	} else {
		fmt.Fprintf(w, "Status: %.0f%% to target (0.9)\n", (0.9-latest)*100)
	}
}

// abrBar creates an ASCII bar for ABR visualization (0.0 to 1.0)
// with a target marker at 0.9
func abrBar(abr float64) string {
	width := 20
	if abr < 0 {
		abr = 0
	}
	if abr > 1 {
		abr = 1
	}

	chars := int(abr * float64(width))
	targetPos := int(0.9 * float64(width)) // target at 0.9

	bar := make([]byte, width)
	for i := range bar {
		if i < chars {
			bar[i] = '#'
		} else if i == targetPos {
			bar[i] = '|'
		} else {
			bar[i] = '.'
		}
	}

	return "[" + string(bar) + "]"
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
