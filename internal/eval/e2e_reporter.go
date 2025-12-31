package eval

import (
	"fmt"
	"io"
	"strings"
)

// E2EReporter formats E2E journey evaluation results
type E2EReporter struct {
	verbose bool
}

// NewE2EReporter creates a new E2E reporter
func NewE2EReporter(verbose bool) *E2EReporter {
	return &E2EReporter{verbose: verbose}
}

// FormatJourneyResult formats a complete journey result (treatment + baseline + comparison)
func (r *E2EReporter) FormatJourneyResult(result *E2EJourneyResult) string {
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("E2E Journey Eval: %s\n", result.JourneyID))
	sb.WriteString(strings.Repeat("=", 40+len(result.JourneyID)))
	sb.WriteString("\n\n")

	// Summary line
	if result.TreatmentResults != nil {
		sessions := len(result.TreatmentResults.SessionResults)
		events := result.TreatmentResults.EventsStored
		tasks := result.TreatmentResults.TasksTotal
		sb.WriteString(fmt.Sprintf("Sessions: %d | Events: %d | Tasks: %d\n\n", sessions, events, tasks))
	}

	// Treatment results
	if result.TreatmentResults != nil {
		sb.WriteString(r.FormatRunResults(result.TreatmentResults, "Treatment Run (Cortex-Enabled)"))
		sb.WriteString("\n")
	}

	// Baseline results
	if result.BaselineResults != nil {
		sb.WriteString(r.FormatRunResults(result.BaselineResults, "Baseline Run (No Memory)"))
		sb.WriteString("\n")
	}

	// Comparison
	if result.Comparison != nil {
		sb.WriteString(r.FormatComparison(result.Comparison))
		sb.WriteString("\n")
	}

	// Verdict
	if result.Reason != "" {
		sb.WriteString(fmt.Sprintf("Verdict: %s\n", result.Reason))
	}

	return sb.String()
}

// FormatRunResults formats a single run (treatment or baseline)
func (r *E2EReporter) FormatRunResults(results *E2ERunResults, label string) string {
	var sb strings.Builder

	sb.WriteString(label)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("-", len(label)))
	sb.WriteString("\n")

	// Format each session with a task
	for _, session := range results.SessionResults {
		if session.TaskResult != nil {
			sb.WriteString(r.formatSessionTaskResult(&session))
			sb.WriteString("\n")
		}
	}

	// Recall query summary
	totalQueries := 0
	correctQueries := 0
	for _, session := range results.SessionResults {
		for _, qr := range session.QueryResults {
			totalQueries++
			if qr.Pass {
				correctQueries++
			}
		}
	}

	if totalQueries > 0 {
		pct := float64(correctQueries) / float64(totalQueries) * 100
		sb.WriteString(fmt.Sprintf("Recall Queries: %d/%d correct (%.1f%%)\n", correctQueries, totalQueries, pct))
	}

	return sb.String()
}

// formatSessionTaskResult formats a single session's task result
func (r *E2EReporter) formatSessionTaskResult(session *E2ESessionResult) string {
	var sb strings.Builder
	task := session.TaskResult

	// Session header with task description
	desc := truncateString(task.TaskDescription, 40)
	sb.WriteString(fmt.Sprintf("Session %s: %s\n", session.SessionID, desc))

	// Completed status
	completedStr := formatCheckmark(task.Completed)
	if task.Completed && !task.CompletedSuccessfully {
		completedStr += " (retry)"
	} else if !task.Completed {
		completedStr += " (max turns)"
	}
	sb.WriteString(fmt.Sprintf("  Completed:     %s\n", completedStr))

	// Tests
	totalTests := task.TestsPassed + task.TestsFailed
	if totalTests > 0 {
		sb.WriteString(fmt.Sprintf("  Tests:         %d/%d passed\n", task.TestsPassed, totalTests))
	}

	// Patterns
	patternsFound := countPatternsFound(task.PatternMatches)
	patternsTotal := len(task.PatternMatches)
	violations := len(task.PatternViolations)
	if patternsTotal > 0 || violations > 0 {
		violationStr := ""
		if violations > 0 {
			violationStr = fmt.Sprintf(", %d violation", violations)
			if violations > 1 {
				violationStr += "s"
			}
			// Add first violation detail
			if r.verbose && len(task.PatternViolations) > 0 {
				violationStr += fmt.Sprintf(" (%s used)", task.PatternViolations[0].Pattern)
			}
		}
		sb.WriteString(fmt.Sprintf("  Patterns:      %d/%d found%s\n", patternsFound, patternsTotal, violationStr))
	}

	// Turns
	sb.WriteString(fmt.Sprintf("  Turns:         %d\n", task.Turns))

	// Corrections (if any)
	if len(task.CorrectionsApplied) > 0 {
		sb.WriteString(fmt.Sprintf("  Corrections:   %d\n", len(task.CorrectionsApplied)))
	}

	// Verbose: show failure reason
	if r.verbose && task.FailureReason != "" {
		sb.WriteString(fmt.Sprintf("  Failure:       %s\n", task.FailureReason))
	}

	return sb.String()
}

// FormatTaskResult formats a single task result
func (r *E2EReporter) FormatTaskResult(result *E2ETaskResult) string {
	var sb strings.Builder

	// Task description
	desc := truncateString(result.TaskDescription, 60)
	sb.WriteString(fmt.Sprintf("Task: %s\n", desc))

	// Completed status
	completedStr := formatCheckmark(result.Completed)
	if result.Completed && !result.CompletedSuccessfully {
		completedStr += " (with errors)"
	}
	sb.WriteString(fmt.Sprintf("  Completed:     %s\n", completedStr))

	// Tests
	totalTests := result.TestsPassed + result.TestsFailed
	if totalTests > 0 {
		testStatus := formatCheckmark(result.TestsFailed == 0)
		sb.WriteString(fmt.Sprintf("  Tests:         %s %d/%d passed\n", testStatus, result.TestsPassed, totalTests))
	}

	// Build status
	if result.BuildSucceeded {
		sb.WriteString(fmt.Sprintf("  Build:         %s\n", formatCheckmark(true)))
	} else if result.BuildError != "" {
		sb.WriteString(fmt.Sprintf("  Build:         %s %s\n", formatCheckmark(false), truncateString(result.BuildError, 40)))
	}

	// Patterns
	patternsFound := countPatternsFound(result.PatternMatches)
	patternsTotal := len(result.PatternMatches)
	if patternsTotal > 0 {
		sb.WriteString(fmt.Sprintf("  Patterns:      %d/%d required found\n", patternsFound, patternsTotal))
	}

	// Violations
	if len(result.PatternViolations) > 0 {
		sb.WriteString(fmt.Sprintf("  Violations:    %d forbidden patterns found\n", len(result.PatternViolations)))
		if r.verbose {
			for _, v := range result.PatternViolations {
				sb.WriteString(fmt.Sprintf("                 - %s\n", v.Pattern))
			}
		}
	}

	// Turns and tokens
	sb.WriteString(fmt.Sprintf("  Turns:         %d\n", result.Turns))
	if result.TokensUsed > 0 {
		sb.WriteString(fmt.Sprintf("  Tokens:        %s\n", formatTokens(result.TokensUsed)))
	}

	// Duration
	if result.Duration > 0 {
		sb.WriteString(fmt.Sprintf("  Duration:      %s\n", result.Duration.String()))
	}

	return sb.String()
}

// FormatComparison formats the treatment vs baseline comparison
func (r *E2EReporter) FormatComparison(comparison *E2EComparison) string {
	var sb strings.Builder

	sb.WriteString("Comparison\n")
	sb.WriteString("==========\n")

	// We need the actual values from the runs to show them properly
	// Since E2EComparison only has deltas, we'll show the lifts/reductions
	sb.WriteString(fmt.Sprintf("%-26s %s\n", "Task Completion Lift:", formatLiftPercent(comparison.TaskCompletionLift)))
	sb.WriteString(fmt.Sprintf("%-26s %s\n", "Test Pass Lift:", formatLiftPercent(comparison.TestPassLift)))
	sb.WriteString(fmt.Sprintf("%-26s %s\n", "Violation Reduction:", formatReductionInt(comparison.ViolationReduction)))
	sb.WriteString(fmt.Sprintf("%-26s %s\n", "Turn Reduction:", formatReductionPercent(comparison.TurnReduction)))
	sb.WriteString(fmt.Sprintf("%-26s %s\n", "Token Reduction:", formatReductionPercent(comparison.TokenReduction)))

	if comparison.CostReduction != 0 {
		sb.WriteString(fmt.Sprintf("%-26s %s\n", "Cost Reduction:", formatReductionPercent(comparison.CostReduction)))
	}

	sb.WriteString(fmt.Sprintf("%-26s %s\n", "Overall Lift:", formatLiftPercent(comparison.OverallLift)))

	// Regression warning
	if comparison.Regression {
		sb.WriteString(fmt.Sprintf("\nWarning: Regression detected - %s\n", comparison.RegressionDetails))
	}

	return sb.String()
}

// FormatComparisonTable formats comparison with actual values from both runs
func (r *E2EReporter) FormatComparisonTable(treatment, baseline *E2ERunResults, comparison *E2EComparison) string {
	var sb strings.Builder

	sb.WriteString("Comparison\n")
	sb.WriteString("==========\n")
	sb.WriteString(fmt.Sprintf("%-26s %-10s %-10s %s\n", "", "Cortex", "Baseline", "Lift"))

	// Task Completion Rate
	sb.WriteString(fmt.Sprintf("%-26s %-10s %-10s %s\n",
		"Task Completion Rate:",
		formatPercent(treatment.TaskCompletionRate),
		formatPercent(baseline.TaskCompletionRate),
		formatLiftPercent(comparison.TaskCompletionLift)))

	// Test Pass Rate
	sb.WriteString(fmt.Sprintf("%-26s %-10s %-10s %s\n",
		"Avg Test Pass Rate:",
		formatPercent(treatment.TestPassRate),
		formatPercent(baseline.TestPassRate),
		formatLiftPercent(comparison.TestPassLift)))

	// Pattern Violations
	sb.WriteString(fmt.Sprintf("%-26s %-10d %-10d %s\n",
		"Pattern Violations:",
		treatment.PatternViolations,
		baseline.PatternViolations,
		formatReductionInt(comparison.ViolationReduction)))

	// Average Turns
	sb.WriteString(fmt.Sprintf("%-26s %-10.1f %-10.1f %s\n",
		"Avg Turns per Task:",
		treatment.AverageTurns,
		baseline.AverageTurns,
		formatReductionPercent(comparison.TurnReduction)))

	// Total Tokens
	sb.WriteString(fmt.Sprintf("%-26s %-10s %-10s %s\n",
		"Total Tokens:",
		formatTokens(treatment.TotalTokens),
		formatTokens(baseline.TotalTokens),
		formatReductionPercent(comparison.TokenReduction)))

	// Corrections
	if treatment.CorrectionsNeeded > 0 || baseline.CorrectionsNeeded > 0 {
		sb.WriteString(fmt.Sprintf("%-26s %-10d %-10d %s\n",
			"Corrections Needed:",
			treatment.CorrectionsNeeded,
			baseline.CorrectionsNeeded,
			formatReductionInt(comparison.CorrectionReduction)))
	}

	// Regression warning
	if comparison.Regression {
		sb.WriteString(fmt.Sprintf("\nWarning: Regression detected - %s\n", comparison.RegressionDetails))
	}

	return sb.String()
}

// FormatSummary generates a one-line summary for the journey
func (r *E2EReporter) FormatSummary(result *E2EJourneyResult) string {
	if result.Comparison == nil || result.TreatmentResults == nil || result.BaselineResults == nil {
		return fmt.Sprintf("Journey %s: incomplete results", result.JourneyID)
	}

	passStr := "PASS"
	if !result.Pass {
		passStr = "FAIL"
	}

	return fmt.Sprintf("[%s] %s: completion +%.0f%%, turns -%.0f%%, violations -%d",
		passStr,
		result.JourneyID,
		result.Comparison.TaskCompletionLift*100,
		result.Comparison.TurnReduction*100,
		result.Comparison.ViolationReduction)
}

// ReportE2E writes the full E2E journey report to a writer
func (r *E2EReporter) ReportE2E(w io.Writer, result *E2EJourneyResult) error {
	output := r.FormatJourneyResult(result)

	// If we have both runs, use the detailed comparison table
	if result.TreatmentResults != nil && result.BaselineResults != nil && result.Comparison != nil {
		// Replace the basic comparison with detailed table
		output = strings.Replace(
			output,
			r.FormatComparison(result.Comparison),
			r.FormatComparisonTable(result.TreatmentResults, result.BaselineResults, result.Comparison),
			1,
		)
	}

	_, err := fmt.Fprint(w, output)
	return err
}

// Helper functions

func formatCheckmark(pass bool) string {
	if pass {
		return "\u2713" // checkmark
	}
	return "\u2717" // X mark
}

func truncateString(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func countPatternsFound(matches []PatternMatch) int {
	count := 0
	for _, m := range matches {
		if m.Found {
			count++
		}
	}
	return count
}

func formatPercent(v float64) string {
	return fmt.Sprintf("%.0f%%", v*100)
}

func formatLiftPercent(v float64) string {
	if v > 0 {
		return fmt.Sprintf("+%.0f%%", v*100)
	} else if v < 0 {
		return fmt.Sprintf("%.0f%%", v*100)
	}
	return "0%"
}

func formatReductionPercent(v float64) string {
	// Positive reduction means improvement (less turns/tokens)
	if v > 0 {
		return fmt.Sprintf("-%.0f%%", v*100)
	} else if v < 0 {
		return fmt.Sprintf("+%.0f%%", -v*100) // Negative reduction = increase
	}
	return "0%"
}

func formatReductionInt(v int) string {
	if v > 0 {
		return fmt.Sprintf("-%d", v)
	} else if v < 0 {
		return fmt.Sprintf("+%d", -v)
	}
	return "0"
}

func formatTokens(tokens int) string {
	if tokens >= 1000 {
		return fmt.Sprintf("%dK", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}
