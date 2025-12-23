package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Reporter handles output formatting for eval results
type Reporter struct {
	verbose bool
}

// NewReporter creates a new reporter
func NewReporter(verbose bool) *Reporter {
	return &Reporter{verbose: verbose}
}

// ReportHuman writes human-readable output
func (r *Reporter) ReportHuman(w io.Writer, run *EvalRun) error {
	fmt.Fprintf(w, "\nCortex Eval Results\n")
	fmt.Fprintf(w, "===================\n\n")

	fmt.Fprintf(w, "Run ID:    %s\n", run.ID)
	fmt.Fprintf(w, "Provider:  %s\n", run.Provider)
	fmt.Fprintf(w, "Timestamp: %s\n", run.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "Scenarios: %d\n\n", len(run.Scenarios))

	// Group results by scenario
	resultsByScenario := make(map[string][]EvalResult)
	for _, result := range run.Results {
		resultsByScenario[result.ScenarioID] = append(resultsByScenario[result.ScenarioID], result)
	}

	// Print each scenario's results
	for _, scenarioID := range run.Scenarios {
		results := resultsByScenario[scenarioID]
		fmt.Fprintf(w, "Scenario: %s\n", scenarioID)
		fmt.Fprintf(w, "%s\n", strings.Repeat("-", 40))

		for _, result := range results {
			statusIcon := "PASS"
			if !result.Pass {
				statusIcon = "FAIL"
			}

			winnerIcon := ""
			switch result.Winner {
			case "cortex":
				winnerIcon = "[CORTEX WINS]"
			case "baseline":
				winnerIcon = "[BASELINE WINS]"
			case "tie":
				winnerIcon = "[TIE]"
			}

			pass, total := countAssertions(result.Assertions)

			fmt.Fprintf(w, "\n  Prompt: %q\n", truncate(result.Prompt, 60))
			fmt.Fprintf(w, "    Status: %s %s\n", statusIcon, winnerIcon)
			fmt.Fprintf(w, "    Score:  %.2f (%d/%d assertions)\n", result.Scores.Overall, pass, total)

			if r.verbose {
				fmt.Fprintf(w, "    Latency: cortex=%dms baseline=%dms\n",
					result.WithCortex.Latency, result.WithoutCortex.Latency)

				// Show failed assertions
				for _, a := range result.Assertions {
					if !a.Pass {
						fmt.Fprintf(w, "    FAILED: %s %q\n", a.Type, a.Expected)
					}
				}

				// Show response preview
				fmt.Fprintf(w, "    Response (cortex): %s\n", truncate(result.WithCortex.Output, 100))
			}
		}
		fmt.Fprintf(w, "\n")
	}

	// Print summary
	fmt.Fprintf(w, "\nSummary\n")
	fmt.Fprintf(w, "%s\n", strings.Repeat("=", 40))
	fmt.Fprintf(w, "Total Prompts:  %d\n", run.Summary.TotalPrompts)
	fmt.Fprintf(w, "Pass Rate:      %.0f%% (%d/%d)\n",
		run.Summary.PassRate*100, run.Summary.PassCount, run.Summary.TotalPrompts)
	fmt.Fprintf(w, "\nA/B Comparison:\n")
	fmt.Fprintf(w, "  Cortex Wins:   %d (%.0f%%)\n",
		run.Summary.CortexWins, run.Summary.WinRate*100)
	fmt.Fprintf(w, "  Baseline Wins: %d\n", run.Summary.BaselineWins)
	fmt.Fprintf(w, "  Ties:          %d\n", run.Summary.Ties)

	// Statistical Analysis
	stats := CalculateStatistics(run.Results)
	if stats.N > 1 {
		fmt.Fprintf(w, "\nStatistical Analysis\n")
		fmt.Fprintf(w, "%s\n", strings.Repeat("-", 40))
		fmt.Fprintf(w, "Sample Size:     %d\n", stats.N)
		fmt.Fprintf(w, "Mean Delta:      %.3f (cortex - baseline)\n", stats.MeanDelta)
		fmt.Fprintf(w, "95%% CI:          [%.3f, %.3f]\n", stats.CI95Lower, stats.CI95Upper)
		fmt.Fprintf(w, "Cohen's d:       %.3f (%s effect)\n", stats.CohensD, stats.EffectSize)
		fmt.Fprintf(w, "p-value:         %.4f", stats.PValue)
		if stats.Significant {
			fmt.Fprintf(w, " *\n")
		} else {
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "Win Rate 95%% CI: [%.0f%%, %.0f%%]\n",
			stats.WinRateCI95Lower*100, stats.WinRateCI95Upper*100)
	}

	// Verdict
	fmt.Fprintf(w, "\n")
	if stats.Significant && stats.MeanDelta > 0 {
		fmt.Fprintf(w, "Verdict: Cortex provides statistically significant improvement\n")
	} else if run.Summary.WinRate > 0.6 {
		fmt.Fprintf(w, "Verdict: Cortex provides significant value\n")
	} else if run.Summary.WinRate > 0.4 {
		fmt.Fprintf(w, "Verdict: Cortex provides moderate value\n")
	} else {
		fmt.Fprintf(w, "Verdict: Cortex needs improvement\n")
	}

	return nil
}

// ReportJSON writes Promptfoo-compatible JSON output
func (r *Reporter) ReportJSON(w io.Writer, run *EvalRun) error {
	output := PromptfooOutput{
		Version:   3,
		Timestamp: run.Timestamp.Format("2006-01-02T15:04:05Z"),
		Results:   make([]PromptfooResult, 0, len(run.Results)*2),
		Stats: PromptfooStats{
			Successes:     run.Summary.PassCount,
			Failures:      run.Summary.FailCount,
			TokenUsage:    TokenUsage{},
			TotalLatencyMs: 0,
		},
	}

	// Convert results to Promptfoo format (one entry per response)
	for _, result := range run.Results {
		// Cortex response
		output.Results = append(output.Results, PromptfooResult{
			Provider: ProviderInfo{
				ID:    result.WithCortex.Provider + "+cortex",
				Label: "With Cortex Context",
			},
			Prompt: PromptInfo{
				Raw:    result.Prompt,
				Label:  result.PromptID,
			},
			Vars: map[string]interface{}{
				"scenario_id": result.ScenarioID,
				"prompt_id":   result.PromptID,
				"condition":   "with_cortex",
			},
			Response: ResponseInfo{
				Output: result.WithCortex.Output,
			},
			Success:   result.Pass,
			Score:     result.Scores.Overall,
			LatencyMs: result.WithCortex.Latency,
			GradingResult: GradingResult{
				Pass:          result.Pass,
				Score:         result.Scores.Overall,
				Reason:        formatAssertionSummary(result.Assertions),
				ComponentResults: convertAssertions(result.Assertions),
			},
		})

		// Baseline response
		baselineAssertions := make([]AssertionResult, 0)
		output.Results = append(output.Results, PromptfooResult{
			Provider: ProviderInfo{
				ID:    result.WithoutCortex.Provider + "+baseline",
				Label: "Without Context (Baseline)",
			},
			Prompt: PromptInfo{
				Raw:   result.Prompt,
				Label: result.PromptID,
			},
			Vars: map[string]interface{}{
				"scenario_id": result.ScenarioID,
				"prompt_id":   result.PromptID,
				"condition":   "baseline",
			},
			Response: ResponseInfo{
				Output: result.WithoutCortex.Output,
			},
			Success:   false, // Baseline is reference, not graded
			Score:     0,
			LatencyMs: result.WithoutCortex.Latency,
			GradingResult: GradingResult{
				Pass:          false,
				Score:         0,
				Reason:        "Baseline reference",
				ComponentResults: convertAssertions(baselineAssertions),
			},
		})

		output.Stats.TotalLatencyMs += result.WithCortex.Latency + result.WithoutCortex.Latency
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// Promptfoo-compatible types

type PromptfooOutput struct {
	Version   int               `json:"version"`
	Timestamp string            `json:"timestamp"`
	Results   []PromptfooResult `json:"results"`
	Stats     PromptfooStats    `json:"stats"`
}

type PromptfooResult struct {
	Provider      ProviderInfo           `json:"provider"`
	Prompt        PromptInfo             `json:"prompt"`
	Vars          map[string]interface{} `json:"vars"`
	Response      ResponseInfo           `json:"response"`
	Success       bool                   `json:"success"`
	Score         float64                `json:"score"`
	LatencyMs     int64                  `json:"latencyMs"`
	GradingResult GradingResult          `json:"gradingResult"`
}

type ProviderInfo struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

type PromptInfo struct {
	Raw   string `json:"raw"`
	Label string `json:"label,omitempty"`
}

type ResponseInfo struct {
	Output string `json:"output"`
}

type GradingResult struct {
	Pass             bool              `json:"pass"`
	Score            float64           `json:"score"`
	Reason           string            `json:"reason"`
	ComponentResults []ComponentResult `json:"componentResults,omitempty"`
}

type ComponentResult struct {
	Pass     bool    `json:"pass"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason"`
	Assertion string `json:"assertion"`
}

type PromptfooStats struct {
	Successes      int        `json:"successes"`
	Failures       int        `json:"failures"`
	TokenUsage     TokenUsage `json:"tokenUsage"`
	TotalLatencyMs int64      `json:"totalLatencyMs"`
}

type TokenUsage struct {
	Total      int `json:"total,omitempty"`
	Prompt     int `json:"prompt,omitempty"`
	Completion int `json:"completion,omitempty"`
}

// Helper functions

func countAssertions(assertions []AssertionResult) (pass, total int) {
	total = len(assertions)
	for _, a := range assertions {
		if a.Pass {
			pass++
		}
	}
	return
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func formatAssertionSummary(assertions []AssertionResult) string {
	pass, total := countAssertions(assertions)
	if pass == total {
		return fmt.Sprintf("All %d assertions passed", total)
	}
	return fmt.Sprintf("%d/%d assertions passed", pass, total)
}

func convertAssertions(assertions []AssertionResult) []ComponentResult {
	results := make([]ComponentResult, 0, len(assertions))
	for _, a := range assertions {
		score := 0.0
		if a.Pass {
			score = 1.0
		}
		results = append(results, ComponentResult{
			Pass:      a.Pass,
			Score:     score,
			Reason:    fmt.Sprintf("%s: %q", a.Type, a.Expected),
			Assertion: a.Type,
		})
	}
	return results
}
