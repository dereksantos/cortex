//go:build !windows

package eval

import (
	"strings"
	"testing"
)

// TestCompareRuns_BaselineCortex confirms the report contains all five
// metrics, both rows, and a non-zero headline lift derived from
// cortex.ShapeSimilarity − baseline.ShapeSimilarity.
func TestCompareRuns_BaselineCortex(t *testing.T) {
	base := &LibraryServiceRun{
		Strategy: StrategyBaseline, Model: "qwen2.5",
		Score: LibraryServiceScore{
			ShapeSimilarity: 0.40, NamingAdherence: 0.50,
			SmellDensity: 1.20, TestParity: 0.55, EndToEndPassRate: 0.60,
		},
	}
	cor := &LibraryServiceRun{
		Strategy: StrategyCortex, Model: "qwen2.5",
		Score: LibraryServiceScore{
			ShapeSimilarity: 0.85, NamingAdherence: 0.90,
			SmellDensity: 0.45, TestParity: 0.92, EndToEndPassRate: 1.00,
		},
	}

	out := CompareRuns(base, cor, nil)
	for _, want := range []string{
		"Shape similarity",
		"Naming adherence",
		"Smell density",
		"Test parity",
		"End-to-end pass rate",
		"Headline shape-similarity lift",
		"+0.450", // cortex 0.85 - baseline 0.40
		"baseline:",
		"cortex:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- report ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "frontier:") {
		t.Errorf("report contained frontier line when frontier=nil: %q", out)
	}
}

func TestCompareRuns_WithFrontier(t *testing.T) {
	base := &LibraryServiceRun{Strategy: StrategyBaseline, Model: "small"}
	cor := &LibraryServiceRun{Strategy: StrategyCortex, Model: "small"}
	front := &LibraryServiceRun{
		Strategy: StrategyFrontier, Model: "sonnet",
		Score: LibraryServiceScore{ShapeSimilarity: 0.95},
	}
	out := CompareRuns(base, cor, front)
	if !strings.Contains(out, "frontier:") {
		t.Errorf("report missing frontier row: %s", out)
	}
	if !strings.Contains(out, "Frontier") {
		t.Errorf("report missing Frontier column: %s", out)
	}
}

func TestCompareRuns_NilGuards(t *testing.T) {
	out := CompareRuns(nil, nil, nil)
	if !strings.Contains(out, "required") {
		t.Errorf("CompareRuns(nil,nil,nil) = %q, want guard message", out)
	}
}
