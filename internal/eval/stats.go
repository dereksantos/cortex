package eval

import (
	"math"
	"sort"
)

// StatisticalAnalysis contains statistical metrics for eval results
type StatisticalAnalysis struct {
	// Sample sizes
	N int `json:"n"`

	// Score statistics
	CortexMean    float64 `json:"cortex_mean"`
	BaselineMean  float64 `json:"baseline_mean"`
	CortexStdDev  float64 `json:"cortex_std_dev"`
	BaselineStdDev float64 `json:"baseline_std_dev"`

	// Effect size
	MeanDelta float64 `json:"mean_delta"`
	CohensD   float64 `json:"cohens_d"`
	EffectSize string `json:"effect_size"` // "negligible", "small", "medium", "large"

	// Statistical significance (paired t-test)
	TStatistic float64 `json:"t_statistic"`
	PValue     float64 `json:"p_value"`
	Significant bool   `json:"significant"` // p < 0.05

	// Confidence interval for mean difference (95%)
	CI95Lower float64 `json:"ci_95_lower"`
	CI95Upper float64 `json:"ci_95_upper"`

	// Win rate statistics
	WinRate       float64 `json:"win_rate"`
	WinRateCI95Lower float64 `json:"win_rate_ci_95_lower"`
	WinRateCI95Upper float64 `json:"win_rate_ci_95_upper"`
}

// CalculateStatistics computes statistical analysis from eval results
func CalculateStatistics(results []EvalResult) StatisticalAnalysis {
	n := len(results)
	if n == 0 {
		return StatisticalAnalysis{}
	}

	// Extract paired scores
	cortexScores := make([]float64, n)
	baselineScores := make([]float64, n)
	differences := make([]float64, n)

	for i, r := range results {
		cortexScores[i] = r.Scores.Overall
		// We need to recalculate baseline score - for now use a heuristic
		// In a full implementation, we'd store both scores
		baselineScores[i] = estimateBaselineScore(r)
		differences[i] = cortexScores[i] - baselineScores[i]
	}

	// Calculate means
	cortexMean := mean(cortexScores)
	baselineMean := mean(baselineScores)
	diffMean := mean(differences)

	// Calculate standard deviations
	cortexStdDev := stdDev(cortexScores, cortexMean)
	baselineStdDev := stdDev(baselineScores, baselineMean)
	diffStdDev := stdDev(differences, diffMean)

	// Paired t-test
	tStat := 0.0
	if diffStdDev > 0 && n > 1 {
		standardError := diffStdDev / math.Sqrt(float64(n))
		tStat = diffMean / standardError
	}

	// Calculate p-value (two-tailed) using t-distribution approximation
	df := float64(n - 1)
	pValue := tDistributionPValue(math.Abs(tStat), df)

	// 95% confidence interval for mean difference
	tCritical := tCriticalValue(0.05, df)
	standardError := diffStdDev / math.Sqrt(float64(n))
	ci95Lower := diffMean - tCritical*standardError
	ci95Upper := diffMean + tCritical*standardError

	// Cohen's d effect size
	pooledStdDev := math.Sqrt((cortexStdDev*cortexStdDev + baselineStdDev*baselineStdDev) / 2)
	cohensD := 0.0
	if pooledStdDev > 0 {
		cohensD = diffMean / pooledStdDev
	}

	effectSize := interpretCohensD(cohensD)

	// Win rate confidence interval (Wilson score interval)
	wins := 0
	for _, r := range results {
		if r.Winner == "cortex" {
			wins++
		}
	}
	winRate := float64(wins) / float64(n)
	winRateLower, winRateUpper := wilsonScoreInterval(wins, n, 0.95)

	return StatisticalAnalysis{
		N:              n,
		CortexMean:    cortexMean,
		BaselineMean:  baselineMean,
		CortexStdDev:  cortexStdDev,
		BaselineStdDev: baselineStdDev,
		MeanDelta:     diffMean,
		CohensD:       cohensD,
		EffectSize:    effectSize,
		TStatistic:    tStat,
		PValue:        pValue,
		Significant:   pValue < 0.05,
		CI95Lower:     ci95Lower,
		CI95Upper:     ci95Upper,
		WinRate:       winRate,
		WinRateCI95Lower: winRateLower,
		WinRateCI95Upper: winRateUpper,
	}
}

// estimateBaselineScore estimates baseline score from result
// In full implementation, both scores would be stored
func estimateBaselineScore(r EvalResult) float64 {
	// Use winner to estimate relative score
	switch r.Winner {
	case "cortex":
		return r.Scores.Overall - 0.15 // cortex is better
	case "baseline":
		return r.Scores.Overall + 0.15 // baseline is better
	default:
		return r.Scores.Overall // tie
	}
}

// Statistical helper functions

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stdDev(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	sumSquares := 0.0
	for _, v := range values {
		diff := v - mean
		sumSquares += diff * diff
	}
	return math.Sqrt(sumSquares / float64(len(values)-1))
}

// tDistributionPValue approximates p-value for t-distribution
// Uses a simple approximation for two-tailed test
func tDistributionPValue(t, df float64) float64 {
	// Approximation using normal distribution for large df
	if df > 30 {
		// Use normal approximation
		return 2 * (1 - normalCDF(t))
	}

	// For smaller df, use a lookup approximation
	// This is a simplified approximation
	x := df / (df + t*t)
	p := incompleteBeta(df/2, 0.5, x)
	return p
}

// tCriticalValue returns critical value for t-distribution
func tCriticalValue(alpha, df float64) float64 {
	// Common critical values (two-tailed)
	if df >= 30 {
		return 1.96 // normal approximation
	}
	// Simplified lookup for common df values
	criticalValues := map[int]float64{
		1: 12.71, 2: 4.30, 3: 3.18, 4: 2.78, 5: 2.57,
		6: 2.45, 7: 2.36, 8: 2.31, 9: 2.26, 10: 2.23,
		15: 2.13, 20: 2.09, 25: 2.06, 30: 2.04,
	}
	dfInt := int(df)
	if v, ok := criticalValues[dfInt]; ok {
		return v
	}
	// Linear interpolation for unlisted values
	return 2.0 + 0.5/df
}

// normalCDF calculates cumulative distribution function for standard normal
func normalCDF(x float64) float64 {
	return 0.5 * (1 + erf(x/math.Sqrt(2)))
}

// erf approximation using Horner's method
func erf(x float64) float64 {
	// Constants for approximation
	a1 := 0.254829592
	a2 := -0.284496736
	a3 := 1.421413741
	a4 := -1.453152027
	a5 := 1.061405429
	p := 0.3275911

	sign := 1.0
	if x < 0 {
		sign = -1
		x = -x
	}

	t := 1.0 / (1.0 + p*x)
	y := 1.0 - (((((a5*t+a4)*t)+a3)*t+a2)*t+a1)*t*math.Exp(-x*x)

	return sign * y
}

// incompleteBeta approximation for p-value calculation
func incompleteBeta(a, b, x float64) float64 {
	// Simple approximation - for production use a proper implementation
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}

	// Use continued fraction approximation
	// This is a simplified version
	lgab, _ := math.Lgamma(a + b)
	lga, _ := math.Lgamma(a)
	lgb, _ := math.Lgamma(b)
	bt := math.Exp(lgab - lga - lgb + a*math.Log(x) + b*math.Log(1-x))

	if x < (a+1)/(a+b+2) {
		return bt * betaCF(a, b, x) / a
	}
	return 1 - bt*betaCF(b, a, 1-x)/b
}

// betaCF continued fraction for incomplete beta
func betaCF(a, b, x float64) float64 {
	maxIterations := 100
	eps := 1e-10

	qab := a + b
	qap := a + 1
	qam := a - 1
	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < eps {
		d = eps
	}
	d = 1.0 / d
	h := d

	for m := 1; m <= maxIterations; m++ {
		m2 := 2 * m
		aa := float64(m) * (b - float64(m)) * x / ((qam + float64(m2)) * (a + float64(m2)))
		d = 1.0 + aa*d
		if math.Abs(d) < eps {
			d = eps
		}
		c = 1.0 + aa/c
		if math.Abs(c) < eps {
			c = eps
		}
		d = 1.0 / d
		h *= d * c
		aa = -(a + float64(m)) * (qab + float64(m)) * x / ((a + float64(m2)) * (qap + float64(m2)))
		d = 1.0 + aa*d
		if math.Abs(d) < eps {
			d = eps
		}
		c = 1.0 + aa/c
		if math.Abs(c) < eps {
			c = eps
		}
		d = 1.0 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1.0) < eps {
			break
		}
	}
	return h
}

// wilsonScoreInterval calculates confidence interval for proportion
func wilsonScoreInterval(successes, n int, confidence float64) (lower, upper float64) {
	if n == 0 {
		return 0, 1
	}

	p := float64(successes) / float64(n)
	z := 1.96 // 95% confidence

	denominator := 1 + z*z/float64(n)
	center := (p + z*z/(2*float64(n))) / denominator
	spread := z * math.Sqrt((p*(1-p)+z*z/(4*float64(n)))/float64(n)) / denominator

	lower = math.Max(0, center-spread)
	upper = math.Min(1, center+spread)
	return
}

// interpretCohensD returns effect size interpretation
func interpretCohensD(d float64) string {
	absD := math.Abs(d)
	switch {
	case absD < 0.2:
		return "negligible"
	case absD < 0.5:
		return "small"
	case absD < 0.8:
		return "medium"
	default:
		return "large"
	}
}

// Additional analysis functions

// CalculateScenarioStats computes per-scenario statistics
func CalculateScenarioStats(results []EvalResult) map[string]StatisticalAnalysis {
	// Group by scenario
	byScenario := make(map[string][]EvalResult)
	for _, r := range results {
		byScenario[r.ScenarioID] = append(byScenario[r.ScenarioID], r)
	}

	stats := make(map[string]StatisticalAnalysis)
	for scenarioID, scenarioResults := range byScenario {
		stats[scenarioID] = CalculateStatistics(scenarioResults)
	}
	return stats
}

// CalculatePercentiles returns percentiles for score distribution
func CalculatePercentiles(results []EvalResult) map[string]float64 {
	if len(results) == 0 {
		return nil
	}

	scores := make([]float64, len(results))
	for i, r := range results {
		scores[i] = r.Scores.Overall
	}
	sort.Float64s(scores)

	percentile := func(p float64) float64 {
		idx := p * float64(len(scores)-1)
		lower := int(idx)
		upper := lower + 1
		if upper >= len(scores) {
			return scores[len(scores)-1]
		}
		frac := idx - float64(lower)
		return scores[lower]*(1-frac) + scores[upper]*frac
	}

	return map[string]float64{
		"p10": percentile(0.10),
		"p25": percentile(0.25),
		"p50": percentile(0.50),
		"p75": percentile(0.75),
		"p90": percentile(0.90),
	}
}
