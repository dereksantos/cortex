package eval

import (
	"strings"
)

// Scorer handles rule-based evaluation scoring
type Scorer struct{}

// NewScorer creates a new scorer instance
func NewScorer() *Scorer {
	return &Scorer{}
}

// ScoreResponse evaluates a response against ground truth
func (s *Scorer) ScoreResponse(response string, groundTruth GroundTruth) (ScoreSet, []AssertionResult) {
	var assertions []AssertionResult

	// Normalize response for comparison
	responseLower := strings.ToLower(response)

	// Score must_include assertions
	includeScore, includeAssertions := s.scoreMustInclude(responseLower, groundTruth.MustInclude)
	assertions = append(assertions, includeAssertions...)

	// Score must_exclude assertions
	excludeScore, excludeAssertions := s.scoreMustExclude(responseLower, groundTruth.MustExclude)
	assertions = append(assertions, excludeAssertions...)

	// Calculate overall score (weighted average)
	overall := s.calculateOverall(includeScore, excludeScore, groundTruth)

	scores := ScoreSet{
		MustInclude: includeScore,
		MustExclude: excludeScore,
		Overall:     overall,
	}

	return scores, assertions
}

// scoreMustInclude checks for required terms (case-insensitive)
func (s *Scorer) scoreMustInclude(response string, terms []string) (float64, []AssertionResult) {
	if len(terms) == 0 {
		return 1.0, nil
	}

	var assertions []AssertionResult
	found := 0

	for _, term := range terms {
		termLower := strings.ToLower(term)
		isFound := strings.Contains(response, termLower)

		assertions = append(assertions, AssertionResult{
			Type:     "must_include",
			Expected: term,
			Found:    isFound,
			Pass:     isFound,
		})

		if isFound {
			found++
		}
	}

	return float64(found) / float64(len(terms)), assertions
}

// scoreMustExclude checks for forbidden terms (case-insensitive)
func (s *Scorer) scoreMustExclude(response string, terms []string) (float64, []AssertionResult) {
	if len(terms) == 0 {
		return 1.0, nil
	}

	var assertions []AssertionResult
	excluded := 0

	for _, term := range terms {
		termLower := strings.ToLower(term)
		isFound := strings.Contains(response, termLower)

		assertions = append(assertions, AssertionResult{
			Type:     "must_exclude",
			Expected: term,
			Found:    isFound,
			Pass:     !isFound, // Pass if NOT found
		})

		if !isFound {
			excluded++
		}
	}

	return float64(excluded) / float64(len(terms)), assertions
}

// calculateOverall computes a weighted overall score
func (s *Scorer) calculateOverall(includeScore, excludeScore float64, gt GroundTruth) float64 {
	// Weight based on number of assertions
	includeCount := len(gt.MustInclude)
	excludeCount := len(gt.MustExclude)
	totalCount := includeCount + excludeCount

	if totalCount == 0 {
		return 1.0
	}

	// Weighted average based on assertion counts
	includeWeight := float64(includeCount) / float64(totalCount)
	excludeWeight := float64(excludeCount) / float64(totalCount)

	return (includeScore * includeWeight) + (excludeScore * excludeWeight)
}

// CompareScores determines the winner between cortex and baseline
func (s *Scorer) CompareScores(cortexScore, baselineScore ScoreSet) string {
	delta := cortexScore.Overall - baselineScore.Overall

	// Require meaningful difference (5% threshold)
	if delta > 0.05 {
		return "cortex"
	} else if delta < -0.05 {
		return "baseline"
	}
	return "tie"
}

// CalculateDelta returns the score improvement (cortex - baseline)
func (s *Scorer) CalculateDelta(cortexScore, baselineScore ScoreSet) float64 {
	return cortexScore.Overall - baselineScore.Overall
}

// AllAssertionsPass checks if all assertions passed
func (s *Scorer) AllAssertionsPass(assertions []AssertionResult) bool {
	for _, a := range assertions {
		if !a.Pass {
			return false
		}
	}
	return true
}

// CountPassingAssertions returns count of passing assertions
func (s *Scorer) CountPassingAssertions(assertions []AssertionResult) (pass, total int) {
	total = len(assertions)
	for _, a := range assertions {
		if a.Pass {
			pass++
		}
	}
	return pass, total
}
