package eval

import (
	"strings"
)

// Score evaluates a response against expected includes/excludes.
// Returns a score from 0.0 to 1.0.
func Score(response string, expect Expect) float64 {
	if len(expect.Includes) == 0 && len(expect.Excludes) == 0 {
		return 1.0 // No expectations = pass
	}

	lower := strings.ToLower(response)
	total := len(expect.Includes) + len(expect.Excludes)
	passed := 0

	// Check includes
	for _, inc := range expect.Includes {
		if strings.Contains(lower, strings.ToLower(inc)) {
			passed++
		}
	}

	// Check excludes
	for _, exc := range expect.Excludes {
		if !strings.Contains(lower, strings.ToLower(exc)) {
			passed++
		}
	}

	return float64(passed) / float64(total)
}

// CalculateLift computes how much Cortex improves over baseline.
// Lift = (cortex - baseline) / baseline, as a percentage.
// Returns 0 if baseline is 0.
func CalculateLift(cortexScore, baselineScore float64) float64 {
	if baselineScore == 0 {
		if cortexScore == 0 {
			return 0 // Both zero = no lift
		}
		return 1.0 // Cortex helps when baseline fails (100% lift)
	}
	return (cortexScore - baselineScore) / baselineScore
}

// TestResult holds the result of a single test.
type TestResult struct {
	TestID string `json:"test_id"`
	Query  string `json:"query"`

	// Baseline: LLM response WITHOUT any Cortex context
	BaselineScore float64 `json:"baseline_score"`

	// Cortex: LLM response WITH Cortex context
	CortexScore float64 `json:"cortex_score"`

	// Lift: How much Cortex improves over baseline
	// Lift = (cortex - baseline) / baseline
	Lift float64 `json:"lift"`

	// Winner: "cortex", "baseline", or "tie"
	Winner string `json:"winner"`

	// Pass: Cortex score >= baseline (Cortex doesn't hurt)
	Pass bool `json:"pass"`
}

// ScenarioResult holds the result of running a scenario.
type ScenarioResult struct {
	ScenarioID string       `json:"scenario_id"`
	Name       string       `json:"name"`
	Tests      []TestResult `json:"tests"`

	// Average scores across tests
	AvgBaselineScore float64 `json:"avg_baseline_score"`
	AvgCortexScore   float64 `json:"avg_cortex_score"`
	AvgLift          float64 `json:"avg_lift"`

	// Win/loss stats
	CortexWins   int `json:"cortex_wins"`
	BaselineWins int `json:"baseline_wins"`
	Ties         int `json:"ties"`

	// Pass: Cortex doesn't cause regressions (lift >= 0)
	Pass bool `json:"pass"`
}

// Results holds the results of an entire eval run.
type Results struct {
	Timestamp string           `json:"timestamp"`
	Provider  string           `json:"provider"`
	Model     string           `json:"model"`
	Scenarios []ScenarioResult `json:"scenarios"`

	// Overall metrics
	AvgBaselineScore float64 `json:"avg_baseline_score"`
	AvgCortexScore   float64 `json:"avg_cortex_score"`
	AvgLift          float64 `json:"avg_lift"` // Average lift across all tests

	// Win/loss totals
	TotalCortexWins   int `json:"total_cortex_wins"`
	TotalBaselineWins int `json:"total_baseline_wins"`
	TotalTies         int `json:"total_ties"`

	// Pass rate: % of scenarios where Cortex doesn't regress
	PassRate float64 `json:"pass_rate"`
	Pass     bool    `json:"pass"`
}

// LiftThreshold is the minimum average lift required to pass.
// 0.0 means Cortex must not hurt (break even or better).
const LiftThreshold = 0.0

// WinThreshold determines winner margin (5% difference needed)
const WinThreshold = 0.05

// DetermineWinner compares scores and returns winner.
func DetermineWinner(cortexScore, baselineScore float64) string {
	diff := cortexScore - baselineScore
	if diff > WinThreshold {
		return "cortex"
	}
	if diff < -WinThreshold {
		return "baseline"
	}
	return "tie"
}

// CalculateScenarioResult aggregates test results into scenario result.
func CalculateScenarioResult(scenarioID, name string, tests []TestResult) *ScenarioResult {
	if len(tests) == 0 {
		return &ScenarioResult{
			ScenarioID: scenarioID,
			Name:       name,
			Pass:       false,
		}
	}

	var totalBaseline, totalCortex, totalLift float64
	cortexWins, baselineWins, ties := 0, 0, 0

	for _, t := range tests {
		totalBaseline += t.BaselineScore
		totalCortex += t.CortexScore
		totalLift += t.Lift

		switch t.Winner {
		case "cortex":
			cortexWins++
		case "baseline":
			baselineWins++
		default:
			ties++
		}
	}

	n := float64(len(tests))
	avgLift := totalLift / n

	return &ScenarioResult{
		ScenarioID:       scenarioID,
		Name:             name,
		Tests:            tests,
		AvgBaselineScore: totalBaseline / n,
		AvgCortexScore:   totalCortex / n,
		AvgLift:          avgLift,
		CortexWins:       cortexWins,
		BaselineWins:     baselineWins,
		Ties:             ties,
		Pass:             avgLift >= LiftThreshold, // Cortex doesn't hurt
	}
}

// CalculateResults aggregates scenario results into overall results.
func CalculateResults(scenarios []ScenarioResult, provider, model string) *Results {
	if len(scenarios) == 0 {
		return &Results{
			Provider: provider,
			Model:    model,
			Pass:     false,
		}
	}

	var totalBaseline, totalCortex, totalLift float64
	totalCortexWins, totalBaselineWins, totalTies := 0, 0, 0
	passCount := 0

	for _, s := range scenarios {
		totalBaseline += s.AvgBaselineScore
		totalCortex += s.AvgCortexScore
		totalLift += s.AvgLift
		totalCortexWins += s.CortexWins
		totalBaselineWins += s.BaselineWins
		totalTies += s.Ties

		if s.Pass {
			passCount++
		}
	}

	n := float64(len(scenarios))

	return &Results{
		Provider:          provider,
		Model:             model,
		Scenarios:         scenarios,
		AvgBaselineScore:  totalBaseline / n,
		AvgCortexScore:    totalCortex / n,
		AvgLift:           totalLift / n,
		TotalCortexWins:   totalCortexWins,
		TotalBaselineWins: totalBaselineWins,
		TotalTies:         totalTies,
		PassRate:          float64(passCount) / n,
		Pass:              totalLift/n >= LiftThreshold,
	}
}
