package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ReportMeasure writes human-readable measure eval results.
func ReportMeasure(w io.Writer, results *MeasureResults) {
	fmt.Fprintf(w, "Measure Eval: Promptability vs Response Quality\n")
	fmt.Fprintf(w, "=================================================\n\n")
	fmt.Fprintf(w, "Provider: %s", results.Provider)
	if results.Model != "" {
		fmt.Fprintf(w, " (%s)", results.Model)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	for _, s := range results.Scenarios {
		fmt.Fprintf(w, "Scenario: %s\n", s.Name)
		fmt.Fprintf(w, "Task: %s\n", s.Task)

		for _, v := range s.Variants {
			prompt := v.Prompt
			if len(prompt) > 60 {
				prompt = prompt[:57] + "..."
			}
			decompTag := ""
			if v.IsDecomposed {
				decompTag = fmt.Sprintf(" (%d subs)", v.SubCount)
			}
			fmt.Fprintf(w, "  %-16s P=%.2f [%s]  Q=%.2f  Tokens=%d%s\n",
				v.VariantID, v.Promptability, v.Grade, v.CompositeQuality, v.TotalTokens, decompTag)
		}
		fmt.Fprintf(w, "  Correlation: %.2f\n\n", s.Correlation)
	}

	// Overall
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 50))
	fmt.Fprintf(w, "Overall\n")
	fmt.Fprintf(w, "  Pearson Correlation:  %.2f\n", results.OverallCorrelation)
	if results.DecompositionLift != 0 {
		fmt.Fprintf(w, "  Decomposition Lift:  %+.0f%%\n", results.DecompositionLift*100)
	}

	verdict := "FAIL"
	if results.Pass {
		verdict = "PASS"
	}
	fmt.Fprintf(w, "\n  VERDICT: %s", verdict)
	if results.Pass {
		fmt.Fprintf(w, " - Promptability predicts quality (r >= 0.7)\n")
	} else {
		fmt.Fprintf(w, " - Correlation too weak (r < 0.7)\n")
	}
}

// ReportMeasureJSON writes JSON measure eval results.
func ReportMeasureJSON(w io.Writer, results *MeasureResults) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}
