package measure

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Report writes human-readable measurement results.
func Report(w io.Writer, result *Result) {
	fmt.Fprintf(w, "Prompt Quality Measurement\n")
	fmt.Fprintf(w, "==========================\n\n")

	// Truncate prompt for display
	prompt := result.Prompt
	if len(prompt) > 80 {
		prompt = prompt[:77] + "..."
	}
	fmt.Fprintf(w, "Prompt: %q\n", prompt)
	fmt.Fprintf(w, "Promptability: %.2f [%s]\n\n", result.Promptability, result.Grade)

	if result.Mechanical != nil {
		reportMechanical(w, result.Mechanical)
	}

	if result.Agentic != nil {
		reportAgentic(w, result.Agentic)
	}
}

func reportMechanical(w io.Writer, m *MechanicalResult) {
	fmt.Fprintf(w, "Mechanical Signals\n")
	fmt.Fprintf(w, "------------------\n")
	fmt.Fprintf(w, "Scope:    %.2f (%d actions, %d files, %d conditionals, %d concerns)\n",
		m.ScopeScore, m.ActionVerbCount, m.FileReferences, m.ConditionalCount, m.ConcernCount)
	fmt.Fprintf(w, "Clarity:  %.2f (specificity=%.2f, %d constraints, examples=%v, %d questions)\n",
		m.ClarityScore, m.SpecificityScore, m.ConstraintCount, m.HasExamples, m.QuestionCount)
	fmt.Fprintf(w, "Decomp:   %.2f\n", m.DecompositionScore)
	fmt.Fprintf(w, "Est. input:  ~%d tokens\n", m.InputTokens)
	fmt.Fprintf(w, "Est. output: ~%d tokens\n\n", m.EstimatedOutputTokens)
}

func reportAgentic(w io.Writer, a *AgenticResult) {
	fmt.Fprintf(w, "Agentic Assessment\n")
	fmt.Fprintf(w, "------------------\n")
	fmt.Fprintf(w, "Scope: %s\n", a.ScopeClassification)
	if a.ScopeExplanation != "" {
		fmt.Fprintf(w, "  %s\n", a.ScopeExplanation)
	}

	fmt.Fprintf(w, "Clarity: %.2f\n", a.ClarityScore)
	if len(a.Ambiguities) > 0 {
		fmt.Fprintf(w, "  Ambiguities:\n")
		for _, amb := range a.Ambiguities {
			fmt.Fprintf(w, "    - %s\n", amb)
		}
	}
	if len(a.MissingConstraints) > 0 {
		fmt.Fprintf(w, "  Missing constraints:\n")
		for _, mc := range a.MissingConstraints {
			fmt.Fprintf(w, "    - %s\n", mc)
		}
	}

	if a.Decomposable {
		fmt.Fprintf(w, "Decomposable: yes (%d sub-tasks, %d independent)\n", len(a.SubTasks), a.IndependentSubs)
		for _, st := range a.SubTasks {
			fmt.Fprintf(w, "    - %s\n", st)
		}
	} else {
		fmt.Fprintf(w, "Decomposable: no (already atomic)\n")
	}

	fmt.Fprintf(w, "Context fit: %.2f\n", a.ContextWindowFit)
	if a.FitExplanation != "" {
		fmt.Fprintf(w, "  %s\n", a.FitExplanation)
	}
	fmt.Fprintln(w)
}

// ReportJSON writes JSON measurement results.
func ReportJSON(w io.Writer, result *Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// ReportBatch writes results for multiple prompts.
func ReportBatch(w io.Writer, results []*Result) {
	for i, r := range results {
		if i > 0 {
			fmt.Fprintf(w, "%s\n", strings.Repeat("-", 40))
		}
		Report(w, r)
	}

	// Summary
	if len(results) > 1 {
		fmt.Fprintf(w, "%s\n", strings.Repeat("=", 40))
		fmt.Fprintf(w, "Summary: %d prompts\n", len(results))

		var totalScore float64
		grades := make(map[string]int)
		for _, r := range results {
			totalScore += r.Promptability
			grades[r.Grade]++
		}

		fmt.Fprintf(w, "Avg Promptability: %.2f\n", totalScore/float64(len(results)))
		fmt.Fprintf(w, "Grades: ")
		for _, g := range []string{"A", "B", "C", "D", "F"} {
			if count, ok := grades[g]; ok {
				fmt.Fprintf(w, "%s=%d ", g, count)
			}
		}
		fmt.Fprintln(w)
	}
}
