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
	fmt.Fprintf(w, "Eval Results\n")
	fmt.Fprintf(w, "============\n\n")

	fmt.Fprintf(w, "Provider: %s\n", results.Provider)
	if results.Model != "" {
		fmt.Fprintf(w, "Model: %s\n", results.Model)
	}
	fmt.Fprintf(w, "\n")

	// Summary
	fmt.Fprintf(w, "Summary\n")
	fmt.Fprintf(w, "-------\n")
	fmt.Fprintf(w, "ABR:       %.2f", results.ABR)
	if results.ABR >= ABRThreshold {
		fmt.Fprintf(w, " (PASS)\n")
	} else {
		fmt.Fprintf(w, " (FAIL, need >= %.2f)\n", ABRThreshold)
	}
	fmt.Fprintf(w, "Pass Rate: %.0f%% (%d/%d scenarios)\n",
		results.PassRate*100,
		countPassing(results.Scenarios),
		len(results.Scenarios))
	fmt.Fprintf(w, "\n")

	// Scenarios
	fmt.Fprintf(w, "Scenarios\n")
	fmt.Fprintf(w, "---------\n")
	for _, s := range results.Scenarios {
		status := "PASS"
		if !s.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%-40s ABR: %.2f [%s]\n", truncate(s.ScenarioID, 40), s.ABR, status)
	}
	fmt.Fprintf(w, "\n")

	// Verdict
	if results.Pass {
		fmt.Fprintf(w, "VERDICT: PASS - ABR %.2f >= %.2f threshold\n", results.ABR, ABRThreshold)
	} else {
		fmt.Fprintf(w, "VERDICT: FAIL - ABR %.2f < %.2f threshold\n", results.ABR, ABRThreshold)
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
	fmt.Fprintf(w, "ABR: %.2f [%s] (%d scenarios)\n",
		results.ABR, status, len(results.Scenarios))
}

// ReportTrend writes ABR trend over recent runs.
func ReportTrend(w io.Writer, abrs []float64) {
	if len(abrs) == 0 {
		fmt.Fprintf(w, "No previous runs.\n")
		return
	}

	fmt.Fprintf(w, "ABR Trend (last %d runs)\n", len(abrs))
	fmt.Fprintf(w, "------------------------\n")

	// ASCII chart
	maxWidth := 40
	for i, abr := range abrs {
		barLen := int(abr * float64(maxWidth))
		bar := strings.Repeat("=", barLen)
		fmt.Fprintf(w, "%2d: [%-40s] %.2f\n", i+1, bar, abr)
	}

	// Trend direction
	if len(abrs) >= 2 {
		first := abrs[0]
		last := abrs[len(abrs)-1]
		diff := last - first
		if diff > 0.05 {
			fmt.Fprintf(w, "\nTrend: IMPROVING (+%.2f)\n", diff)
		} else if diff < -0.05 {
			fmt.Fprintf(w, "\nTrend: DECLINING (%.2f)\n", diff)
		} else {
			fmt.Fprintf(w, "\nTrend: STABLE\n")
		}
	}
}

func countPassing(scenarios []ScenarioResult) int {
	count := 0
	for _, s := range scenarios {
		if s.Pass {
			count++
		}
	}
	return count
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
