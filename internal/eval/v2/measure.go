package eval

import (
	"math"
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

// ScoreRanking computes NDCG for retrieval results against expected ranking.
// Returns NDCG score from 0.0 to 1.0.
// expectedRanking: substrings in ideal order (most relevant first)
// actualResults: retrieved results as strings
func ScoreRanking(expectedRanking []string, actualResults []string) float64 {
	if len(expectedRanking) == 0 {
		return 0
	}

	// Build relevance scores: items earlier in expected ranking are more relevant
	// Relevance = len(expectedRanking) - position (so first item has highest score)
	relevanceMap := make(map[int]float64)
	for i, expected := range expectedRanking {
		relevance := float64(len(expectedRanking) - i)
		// Find which actual result contains this expected substring
		for j, actual := range actualResults {
			if strings.Contains(strings.ToLower(actual), strings.ToLower(expected)) {
				relevanceMap[j] = relevance
				break
			}
		}
	}

	// Calculate DCG (Discounted Cumulative Gain)
	dcg := 0.0
	for i := 0; i < len(actualResults) && i < len(expectedRanking); i++ {
		if rel, ok := relevanceMap[i]; ok {
			dcg += rel / math.Log2(float64(i+2)) // +2 because log2(1) = 0
		}
	}

	// Calculate IDCG (Ideal DCG) - perfect ranking
	idcg := 0.0
	for i := 0; i < len(expectedRanking); i++ {
		rel := float64(len(expectedRanking) - i)
		idcg += rel / math.Log2(float64(i+2))
	}

	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// CalculateABR computes Agentic Benefit Ratio.
// ABR = NDCG(Fast) / NDCG(Full)
// Goal: ABR → 1.0 as Think improves Fast mode.
func CalculateABR(fastNDCG, fullNDCG float64) float64 {
	if fullNDCG == 0 {
		if fastNDCG == 0 {
			return 1.0 // Both zero = equivalent
		}
		return 1.0 // Fast better than Full (unusual but possible)
	}
	abr := fastNDCG / fullNDCG
	if abr > 1.0 {
		return 1.0 // Cap at 1.0
	}
	return abr
}

// TestResult holds the result of a single test.
type TestResult struct {
	TestID string `json:"test_id"`
	Query  string `json:"query"`
	Depth  int    `json:"depth"` // Tree depth (for ABR progression tracking)

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

	// ABR metrics (only populated when expect.ranking is specified)
	HasRanking bool    `json:"has_ranking,omitempty"`
	FastNDCG   float64 `json:"fast_ndcg,omitempty"` // NDCG for Fast mode (Reflex only)
	FullNDCG   float64 `json:"full_ndcg,omitempty"` // NDCG for Full mode (Reflex + Reflect)
	ABR        float64 `json:"abr,omitempty"`       // FastNDCG / FullNDCG
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

	// ABR metrics (averaged across tests with ranking)
	HasABR      bool               `json:"has_abr,omitempty"`
	AvgFastNDCG float64            `json:"avg_fast_ndcg,omitempty"`
	AvgFullNDCG float64            `json:"avg_full_ndcg,omitempty"`
	AvgABR      float64            `json:"avg_abr,omitempty"`
	ABRByDepth  map[int]float64    `json:"abr_by_depth,omitempty"` // ABR at each tree depth

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

	// ABR aggregation
	var totalFastNDCG, totalFullNDCG, totalABR float64
	abrCount := 0
	abrByDepth := make(map[int][]float64)

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

		// Aggregate ABR metrics
		if t.HasRanking {
			totalFastNDCG += t.FastNDCG
			totalFullNDCG += t.FullNDCG
			totalABR += t.ABR
			abrCount++
			abrByDepth[t.Depth] = append(abrByDepth[t.Depth], t.ABR)
		}
	}

	n := float64(len(tests))
	avgLift := totalLift / n

	result := &ScenarioResult{
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

	// Add ABR metrics if any tests had ranking
	if abrCount > 0 {
		result.HasABR = true
		result.AvgFastNDCG = totalFastNDCG / float64(abrCount)
		result.AvgFullNDCG = totalFullNDCG / float64(abrCount)
		result.AvgABR = totalABR / float64(abrCount)

		// Calculate average ABR by depth
		result.ABRByDepth = make(map[int]float64)
		for depth, abrs := range abrByDepth {
			sum := 0.0
			for _, abr := range abrs {
				sum += abr
			}
			result.ABRByDepth[depth] = sum / float64(len(abrs))
		}
	}

	return result
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
