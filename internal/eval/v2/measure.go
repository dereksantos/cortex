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

// CalculateABR computes the Agentic Benefit Ratio.
// ABR = quality(Fast + Think) / quality(Full)
// Returns 1.0 if full quality is 0 (avoid division by zero).
func CalculateABR(fastScore, fullScore float64) float64 {
	if fullScore == 0 {
		if fastScore == 0 {
			return 1.0 // Both zero = equivalent
		}
		return 1.0 // Fast is non-zero, full is zero = fast wins
	}
	abr := fastScore / fullScore
	if abr > 1.0 {
		return 1.0 // Cap at 1.0 (fast can't exceed full quality)
	}
	return abr
}

// TestResult holds the result of a single test.
type TestResult struct {
	TestID    string  `json:"test_id"`
	Query     string  `json:"query"`
	FastScore float64 `json:"fast_score"` // Score with Fast mode (mechanical retrieval)
	FullScore float64 `json:"full_score"` // Score with Full mode (agentic reranking)
	ABR       float64 `json:"abr"`
	Pass      bool    `json:"pass"`
}

// ScenarioResult holds the result of running a scenario.
type ScenarioResult struct {
	ScenarioID string       `json:"scenario_id"`
	Name       string       `json:"name"`
	Tests      []TestResult `json:"tests"`
	ABR        float64      `json:"abr"`      // Average ABR across tests
	PassRate   float64      `json:"pass_rate"`
	Pass       bool         `json:"pass"`
}

// Results holds the results of an entire eval run.
type Results struct {
	Timestamp string           `json:"timestamp"`
	Provider  string           `json:"provider"`
	Model     string           `json:"model"`
	Scenarios []ScenarioResult `json:"scenarios"`
	ABR       float64          `json:"abr"`       // Overall ABR
	PassRate  float64          `json:"pass_rate"` // % of scenarios passing
	Pass      bool             `json:"pass"`      // ABR >= 0.9
}

// ABRThreshold is the minimum ABR required to pass.
const ABRThreshold = 0.9

// CalculateResults aggregates scenario results into overall results.
func CalculateResults(scenarios []ScenarioResult, provider, model string) *Results {
	if len(scenarios) == 0 {
		return &Results{
			Provider: provider,
			Model:    model,
			ABR:      0,
			PassRate: 0,
			Pass:     false,
		}
	}

	var totalABR float64
	var passCount int
	for _, s := range scenarios {
		totalABR += s.ABR
		if s.Pass {
			passCount++
		}
	}

	avgABR := totalABR / float64(len(scenarios))
	passRate := float64(passCount) / float64(len(scenarios))

	return &Results{
		Provider:  provider,
		Model:     model,
		Scenarios: scenarios,
		ABR:       avgABR,
		PassRate:  passRate,
		Pass:      avgABR >= ABRThreshold,
	}
}
