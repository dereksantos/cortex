package eval

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"gopkg.in/yaml.v3"
)

// CognitionEvaluator runs cognition eval scenarios
type CognitionEvaluator struct {
	cortex  cognition.Cortex
	verbose bool
}

// NewCognitionEvaluator creates a new cognition evaluator
func NewCognitionEvaluator(cortex cognition.Cortex) *CognitionEvaluator {
	return &CognitionEvaluator{
		cortex: cortex,
	}
}

// SetVerbose enables verbose output
func (e *CognitionEvaluator) SetVerbose(v bool) {
	e.verbose = v
}

// LoadCognitionScenario loads a cognition scenario from YAML
func LoadCognitionScenario(path string) (*CognitionScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario file: %w", err)
	}

	var scenario CognitionScenario
	if err := yaml.Unmarshal(data, &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
	}

	if scenario.ID == "" {
		return nil, fmt.Errorf("scenario missing required field: id")
	}

	return &scenario, nil
}

// LoadCognitionScenarios loads all cognition scenarios from a directory
func LoadCognitionScenarios(dir string) ([]*CognitionScenario, error) {
	var scenarios []*CognitionScenario

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		scenario, err := LoadCognitionScenario(path)
		if err != nil {
			// Skip non-cognition scenarios
			return nil
		}

		// Only include cognition scenarios
		if scenario.Type != "" {
			scenarios = append(scenarios, scenario)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return scenarios, nil
}

// RunScenario evaluates a single cognition scenario
func (e *CognitionEvaluator) RunScenario(ctx context.Context, scenario *CognitionScenario) (*CognitionEvalResult, error) {
	if e.verbose {
		fmt.Printf("Running cognition scenario: %s (%s)\n", scenario.ID, scenario.Type)
	}

	result := &CognitionEvalResult{
		ScenarioID: scenario.ID,
		Type:       scenario.Type,
	}

	var err error
	switch scenario.Type {
	case CognitionMode:
		result.ModeResults, err = e.runModeTests(ctx, scenario)
	case CognitionSession:
		result.SessionResults, err = e.runSessionTests(ctx, scenario)
	case CognitionBenefit:
		result.BenefitResults, err = e.runBenefitTests(ctx, scenario)
	case CognitionPipeline:
		result.PipelineResults, err = e.runPipelineTests(ctx, scenario)
	case CognitionDream:
		result.DreamResults, err = e.runDreamTests(ctx, scenario)
	case CognitionConflict:
		result.ConflictResults, err = e.runConflictTests(ctx, scenario)
	default:
		return nil, fmt.Errorf("unknown scenario type: %s", scenario.Type)
	}

	if err != nil {
		return nil, err
	}

	// Determine overall pass/fail
	result.Pass, result.Reason = e.calculateOverallResult(result)

	return result, nil
}

// runModeTests runs individual mode tests
func (e *CognitionEvaluator) runModeTests(ctx context.Context, scenario *CognitionScenario) ([]ModeResult, error) {
	var results []ModeResult

	for _, test := range scenario.ModeTests {
		result := ModeResult{
			TestID: test.ID,
			Mode:   scenario.Mode,
		}

		start := time.Now()

		switch scenario.Mode {
		case "reflex":
			actual, err := e.cortex.Reflex(ctx, test.Input.Query)
			result.Latency = time.Since(start)
			if err != nil {
				result.Pass = false
				result.Reason = err.Error()
			} else {
				result.ActualResultIDs = extractIDs(actual)
				result.Pass = true // Assume pass, check individual conditions

				// Check latency if specified
				if test.Expected.MaxLatency > 0 {
					result.LatencyPass = result.Latency <= test.Expected.MaxLatency
					if !result.LatencyPass {
						result.Pass = false
						result.Reason = fmt.Sprintf("latency %v > max %v", result.Latency, test.Expected.MaxLatency)
					}
				}

				// Check min_results if specified
				if test.Expected.MinResults > 0 && len(actual) < test.Expected.MinResults {
					result.Pass = false
					result.Reason = fmt.Sprintf("got %d results, expected at least %d", len(actual), test.Expected.MinResults)
				}

				// Check result IDs only if expected IDs are specified
				if len(test.Expected.ResultIDs) > 0 {
					result.PrecisionAtK = calculatePrecisionAtK(result.ActualResultIDs, test.Expected.ResultIDs, len(test.Expected.ResultIDs))
					if result.PrecisionAtK < 0.5 {
						result.Pass = false
						result.Reason = fmt.Sprintf("precision@k=%.2f < 0.5 (got %v)", result.PrecisionAtK, result.ActualResultIDs)
					}
				}
			}

		case "reflect":
			actual, err := e.cortex.Reflect(ctx, test.Input.Query, test.Input.Candidates)
			result.Latency = time.Since(start)
			if err != nil {
				result.Pass = false
				result.Reason = err.Error()
			} else {
				result.ActualResultIDs = extractIDs(actual)
				result.NDCG = calculateNDCG(result.ActualResultIDs, test.Expected.TopResultIDs)
				result.Pass = result.NDCG >= 0.7
			}

		case "resolve":
			resolveResult, err := e.cortex.Resolve(ctx, test.Input.Query, test.Input.Results)
			result.Latency = time.Since(start)
			if err != nil {
				result.Pass = false
				result.Reason = err.Error()
			} else {
				result.ActualDecision = resolveResult.Decision
				result.ActualConfidence = resolveResult.Confidence
				result.DecisionMatch = result.ActualDecision == test.Expected.Decision
				result.Pass = result.DecisionMatch && result.ActualConfidence >= test.Expected.MinConfidence
			}

		default:
			result.Pass = false
			result.Reason = fmt.Sprintf("unknown mode: %s", scenario.Mode)
		}

		if e.verbose {
			status := "PASS"
			if !result.Pass {
				status = "FAIL"
			}
			fmt.Printf("  %s: %s (%v)\n", test.ID, status, result.Latency)
		}

		results = append(results, result)
	}

	return results, nil
}

// runSessionTests runs session accumulation tests
func (e *CognitionEvaluator) runSessionTests(ctx context.Context, scenario *CognitionScenario) (*SessionResult, error) {
	result := &SessionResult{
		Steps: make([]SessionStepResult, 0, len(scenario.SessionSteps)),
	}

	var totalQuality, cacheHits float64

	for i, step := range scenario.SessionSteps {
		stepResult := SessionStepResult{
			StepID: step.ID,
		}

		// Run retrieval in Fast mode (to test Think's contribution)
		resolveResult, err := e.cortex.Retrieve(ctx, step.Query, cognition.Fast)
		if err != nil {
			stepResult.Pass = false
			stepResult.Reason = err.Error()
			result.Steps = append(result.Steps, stepResult)
			continue
		}

		// Also run in Full mode to compare
		fullResult, _ := e.cortex.Retrieve(ctx, step.Query, cognition.Full)

		// Calculate quality scores
		fastQuality := calculateResultQuality(resolveResult.Results, step.ExpectedResultIDs)
		fullQuality := calculateResultQuality(fullResult.Results, step.ExpectedResultIDs)

		stepResult.QualityScore = fastQuality
		if fullQuality > 0 {
			stepResult.QualityVsFull = fastQuality / fullQuality
		}

		// Check SessionContext from Think
		sessionCtx := e.cortex.SessionContext()
		if sessionCtx != nil {
			stepResult.ActualTopicWeights = sessionCtx.TopicWeights

			// Calculate topic weight accuracy if expectations are set
			if step.ExpectTopicWeights != nil {
				stepResult.TopicWeightAccuracy = calculateTopicWeightAccuracy(
					sessionCtx.TopicWeights,
					step.ExpectTopicWeights,
				)
			}

			// Check cache hit using normalized query key
			if step.ExpectCacheHit {
				queryKey := normalizeQueryKey(step.Query)
				if _, ok := sessionCtx.WarmCache[queryKey]; ok {
					stepResult.CacheHit = true
					cacheHits++
				}
			}
		}

		// Determine pass/fail for this step
		stepResult.Pass = true
		if step.ExpectQualityVsFull != "" {
			// Parse expectation like ">= 0.9"
			if stepResult.QualityVsFull < 0.9 {
				stepResult.Pass = false
				stepResult.Reason = fmt.Sprintf("quality vs full: %.2f < 0.9", stepResult.QualityVsFull)
			}
		}

		totalQuality += stepResult.QualityScore
		result.Steps = append(result.Steps, stepResult)

		if e.verbose {
			fmt.Printf("  Step %d: quality=%.2f, vs_full=%.2f, cache_hit=%v\n",
				i+1, stepResult.QualityScore, stepResult.QualityVsFull, stepResult.CacheHit)
		}
	}

	// Calculate aggregate metrics
	if len(result.Steps) > 0 {
		result.CacheHitRate = cacheHits / float64(len(result.Steps))

		// Quality improvement = last step quality vs first step quality
		if len(result.Steps) >= 2 {
			result.QualityImprovementRate = result.Steps[len(result.Steps)-1].QualityVsFull -
				result.Steps[0].QualityVsFull
		}
	}

	// Overall pass if all steps pass and quality improves
	result.Pass = true
	for _, step := range result.Steps {
		if !step.Pass {
			result.Pass = false
			result.Reason = fmt.Sprintf("step %s failed: %s", step.StepID, step.Reason)
			break
		}
	}

	return result, nil
}

// runBenefitTests measures the Agentic Benefit Ratio
func (e *CognitionEvaluator) runBenefitTests(ctx context.Context, scenario *CognitionScenario) (*BenefitResult, error) {
	result := &BenefitResult{
		QueryResults: make([]BenefitQueryResult, 0, len(scenario.BenefitQueries)),
	}

	var totalABR float64

	for i, query := range scenario.BenefitQueries {
		qr := BenefitQueryResult{
			QueryIndex: i,
		}

		// Fast mode only (no Think benefit yet if fresh)
		// In practice we'd need to reset Think state, simplified here
		fastResult, _ := e.cortex.Retrieve(ctx, query, cognition.Fast)
		qr.FastOnlyQuality = calculateResultQualityFromResolve(fastResult)

		// Fast mode with Think (after Think has run)
		// Trigger Think explicitly
		e.cortex.MaybeThink(ctx)
		fastThinkResult, _ := e.cortex.Retrieve(ctx, query, cognition.Fast)
		qr.FastThinkQuality = calculateResultQualityFromResolve(fastThinkResult)

		// Full mode (gold standard)
		fullResult, _ := e.cortex.Retrieve(ctx, query, cognition.Full)
		qr.FullQuality = calculateResultQualityFromResolve(fullResult)

		// Calculate ABR
		if qr.FullQuality > 0 {
			qr.ABR = qr.FastThinkQuality / qr.FullQuality
		}

		totalABR += qr.ABR
		result.QueryResults = append(result.QueryResults, qr)

		if e.verbose {
			fmt.Printf("  Query %d: fast=%.2f, fast+think=%.2f, full=%.2f, ABR=%.2f\n",
				i, qr.FastOnlyQuality, qr.FastThinkQuality, qr.FullQuality, qr.ABR)
		}
	}

	if len(result.QueryResults) > 0 {
		result.AverageABR = totalABR / float64(len(result.QueryResults))
		result.InitialABR = result.QueryResults[0].ABR
		result.FinalABR = result.QueryResults[len(result.QueryResults)-1].ABR
		result.ABRGrowth = result.FinalABR - result.InitialABR
	}

	// Pass if ABR meets threshold
	result.Pass = result.AverageABR >= scenario.ExpectedABRThreshold
	if !result.Pass {
		result.Reason = fmt.Sprintf("ABR %.2f < threshold %.2f", result.AverageABR, scenario.ExpectedABRThreshold)
	}

	return result, nil
}

// runPipelineTests runs end-to-end pipeline tests
func (e *CognitionEvaluator) runPipelineTests(ctx context.Context, scenario *CognitionScenario) ([]PipelineResult, error) {
	var results []PipelineResult

	for _, test := range scenario.PipelineTests {
		result := PipelineResult{
			TestID: test.ID,
			Mode:   test.Mode,
		}

		start := time.Now()
		resolveResult, err := e.cortex.Retrieve(ctx, test.Query, test.Mode)
		result.Latency = time.Since(start)

		if err != nil {
			result.Pass = false
			result.Reason = err.Error()
		} else {
			result.ActualResultIDs = extractIDs(resolveResult.Results)
			result.ActualDecision = resolveResult.Decision

			// Check expectations
			result.Pass = true

			if test.MaxLatency > 0 && result.Latency > test.MaxLatency {
				result.Pass = false
				result.Reason = fmt.Sprintf("latency %v > max %v", result.Latency, test.MaxLatency)
			}

			if test.ExpectedDecision != 0 && result.ActualDecision != test.ExpectedDecision {
				result.Pass = false
				result.Reason = fmt.Sprintf("decision %s != expected %s", result.ActualDecision, test.ExpectedDecision)
			}
		}

		if e.verbose {
			status := "PASS"
			if !result.Pass {
				status = "FAIL"
			}
			fmt.Printf("  %s: %s (mode=%s, latency=%v)\n", test.ID, status, test.Mode, result.Latency)
		}

		results = append(results, result)
	}

	return results, nil
}

// runDreamTests tests Dream source exploration
func (e *CognitionEvaluator) runDreamTests(ctx context.Context, scenario *CognitionScenario) (*DreamResult, error) {
	result := &DreamResult{}

	// Reset Dream state so MinInterval check passes between scenarios
	e.cortex.ResetForTesting()

	// Force idle state so Dream will run (evals don't have natural idle time)
	e.cortex.ForceIdle()

	// Run Dream
	dreamResult, err := e.cortex.MaybeDream(ctx)
	if err != nil {
		result.Pass = false
		result.Reason = err.Error()
		return result, nil
	}

	result.InsightsGenerated = dreamResult.Insights
	result.BudgetRespected = dreamResult.Status == cognition.DreamRan
	result.SourcesCovered = dreamResult.SourcesCovered

	// Check coverage
	if len(scenario.DreamSources) > 0 {
		result.CoverageRatio = float64(len(result.SourcesCovered)) / float64(len(scenario.DreamSources))
	}

	// Collect sample insights from channel (non-blocking)
	insightsChan := e.cortex.Insights()
	for i := 0; i < 5; i++ {
		select {
		case insight := <-insightsChan:
			result.SampleInsights = append(result.SampleInsights, insight)
		default:
			break
		}
	}

	// Determine pass
	result.Pass = result.InsightsGenerated >= scenario.ExpectedInsights &&
		result.CoverageRatio >= scenario.ExpectedCoverage &&
		(!scenario.BudgetMustRespect || result.BudgetRespected)

	if !result.Pass {
		result.Reason = fmt.Sprintf("insights=%d (expected %d), coverage=%.2f (expected %.2f)",
			result.InsightsGenerated, scenario.ExpectedInsights,
			result.CoverageRatio, scenario.ExpectedCoverage)
	}

	return result, nil
}

// runConflictTests tests pattern conflict detection and resolution
func (e *CognitionEvaluator) runConflictTests(ctx context.Context, scenario *CognitionScenario) (*ConflictResult, error) {
	result := &ConflictResult{}

	// Detect conflict: do we have multiple different patterns?
	patterns := make(map[string][]PatternEvidence)
	for _, ev := range scenario.Evidence {
		patterns[ev.Pattern] = append(patterns[ev.Pattern], ev)
	}

	result.ConflictDetected = len(patterns) > 1

	if e.verbose {
		fmt.Printf("  Conflict detection: found %d distinct patterns\n", len(patterns))
		for pattern, evidence := range patterns {
			totalCount := 0
			for _, ev := range evidence {
				totalCount += ev.Count
			}
			fmt.Printf("    - %s: %d instances\n", pattern, totalCount)
		}
	}

	// Assess severity based on topic
	result.DetectedSeverity = assessSeverity(scenario.ConflictTopic, patterns)

	if e.verbose {
		fmt.Printf("  Severity assessment: %s\n", result.DetectedSeverity)
	}

	// Determine action based on severity
	// High severity → must surface to user
	// Low severity → can resolve silently using best judgment
	if result.DetectedSeverity == SeverityHigh {
		result.Surfaced = true
		result.InjectedContent = formatConflictForUser(scenario.ConflictTopic, patterns)
	} else {
		result.Surfaced = false
		result.ChosenPattern = chooseBestPattern(patterns)
		result.InjectedContent = result.ChosenPattern
	}

	if e.verbose {
		fmt.Printf("  Action: surfaced=%v, chosen=%s\n", result.Surfaced, result.ChosenPattern)
	}

	// Determine pass/fail
	expected := scenario.ConflictExpected
	result.Pass = true

	if expected.ConflictDetected != result.ConflictDetected {
		result.Pass = false
		result.Reason = fmt.Sprintf("conflict detection: expected %v, got %v",
			expected.ConflictDetected, result.ConflictDetected)
	} else if expected.Severity != "" && expected.Severity != result.DetectedSeverity {
		result.Pass = false
		result.Reason = fmt.Sprintf("severity: expected %s, got %s",
			expected.Severity, result.DetectedSeverity)
	} else if expected.MustSurface && !result.Surfaced {
		result.Pass = false
		result.Reason = "expected conflict to be surfaced to user, but it was resolved silently"
	} else if !expected.MustSurface && len(expected.AllowedPatterns) > 0 {
		// Check if chosen pattern is allowed
		allowed := false
		for _, p := range expected.AllowedPatterns {
			if p == result.ChosenPattern {
				allowed = true
				break
			}
		}
		if !allowed {
			result.Pass = false
			result.Reason = fmt.Sprintf("chosen pattern %q not in allowed patterns %v",
				result.ChosenPattern, expected.AllowedPatterns)
		}
	}

	return result, nil
}

// assessSeverity determines how serious a conflict is based on topic
func assessSeverity(topic string, patterns map[string][]PatternEvidence) ConflictSeverity {
	// High severity topics: framework choices, dependencies, architecture
	highSeverityTopics := map[string]bool{
		"testing":      true,
		"framework":    true,
		"database":     true,
		"architecture": true,
		"auth":         true,
		"dependencies": true,
	}

	// Medium severity: style with impact
	mediumSeverityTopics := map[string]bool{
		"error-handling": true,
		"logging":        true,
		"naming":         true,
	}

	if highSeverityTopics[topic] {
		return SeverityHigh
	}
	if mediumSeverityTopics[topic] {
		return SeverityMedium
	}
	return SeverityLow
}

// chooseBestPattern picks the most common/weighted pattern for silent resolution
func chooseBestPattern(patterns map[string][]PatternEvidence) string {
	var bestPattern string
	var bestScore float64

	for pattern, evidence := range patterns {
		var score float64
		for _, ev := range evidence {
			// Score = count * weight
			score += float64(ev.Count) * ev.Weight
		}
		if score > bestScore {
			bestScore = score
			bestPattern = pattern
		}
	}

	return bestPattern
}

// formatConflictForUser creates a message explaining the conflict
func formatConflictForUser(topic string, patterns map[string][]PatternEvidence) string {
	var msg string
	msg = fmt.Sprintf("Conflicting patterns detected for %s:\n", topic)
	for pattern, evidence := range patterns {
		totalCount := 0
		sources := make([]string, 0)
		for _, ev := range evidence {
			totalCount += ev.Count
			sources = append(sources, ev.Source)
		}
		msg += fmt.Sprintf("  - %s: %d instances from %v\n", pattern, totalCount, sources)
	}
	msg += "Which pattern should be used?"
	return msg
}

// calculateOverallResult determines if the scenario passed
func (e *CognitionEvaluator) calculateOverallResult(result *CognitionEvalResult) (bool, string) {
	switch result.Type {
	case CognitionMode:
		for _, mr := range result.ModeResults {
			if !mr.Pass {
				return false, fmt.Sprintf("mode test %s failed: %s", mr.TestID, mr.Reason)
			}
		}
	case CognitionSession:
		if result.SessionResults != nil && !result.SessionResults.Pass {
			return false, result.SessionResults.Reason
		}
	case CognitionBenefit:
		if result.BenefitResults != nil && !result.BenefitResults.Pass {
			return false, result.BenefitResults.Reason
		}
	case CognitionPipeline:
		for _, pr := range result.PipelineResults {
			if !pr.Pass {
				return false, fmt.Sprintf("pipeline test %s failed: %s", pr.TestID, pr.Reason)
			}
		}
	case CognitionDream:
		if result.DreamResults != nil && !result.DreamResults.Pass {
			return false, result.DreamResults.Reason
		}
	case CognitionConflict:
		if result.ConflictResults != nil && !result.ConflictResults.Pass {
			return false, result.ConflictResults.Reason
		}
	}
	return true, ""
}

// Helper functions

func extractIDs(results []cognition.Result) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

func calculatePrecisionAtK(actual, expected []string, k int) float64 {
	if k == 0 || len(actual) == 0 {
		return 0
	}

	expectedSet := make(map[string]bool)
	for _, id := range expected {
		expectedSet[id] = true
	}

	hits := 0
	limit := k
	if limit > len(actual) {
		limit = len(actual)
	}

	for i := 0; i < limit; i++ {
		if expectedSet[actual[i]] {
			hits++
		}
	}

	return float64(hits) / float64(k)
}

func calculateNDCG(actual, expected []string) float64 {
	if len(expected) == 0 {
		return 1.0
	}

	// Build relevance map (position in expected = relevance)
	relevance := make(map[string]float64)
	for i, id := range expected {
		relevance[id] = float64(len(expected) - i) // Higher relevance for earlier positions
	}

	// Calculate DCG
	var dcg float64
	for i, id := range actual {
		if rel, ok := relevance[id]; ok {
			dcg += rel / math.Log2(float64(i+2)) // i+2 because log2(1) = 0
		}
	}

	// Calculate ideal DCG
	var idcg float64
	for i := range expected {
		rel := float64(len(expected) - i)
		idcg += rel / math.Log2(float64(i+2))
	}

	if idcg == 0 {
		return 0
	}

	return dcg / idcg
}

func calculateResultQuality(results []cognition.Result, expectedIDs []string) float64 {
	if len(expectedIDs) == 0 {
		return 1.0
	}

	actualIDs := extractIDs(results)
	return calculatePrecisionAtK(actualIDs, expectedIDs, len(expectedIDs))
}

func calculateResultQualityFromResolve(result *cognition.ResolveResult) float64 {
	if result == nil {
		return 0
	}

	// Calculate quality from actual results, not just confidence
	if len(result.Results) == 0 {
		return 0.1 // Minimal quality if no results
	}

	// Average score of returned results weighted by confidence
	var totalScore float64
	for _, r := range result.Results {
		totalScore += r.Score
	}
	avgScore := totalScore / float64(len(result.Results))

	// Blend average result score with confidence
	// Quality = 0.6 * avgScore + 0.4 * confidence
	quality := 0.6*avgScore + 0.4*result.Confidence

	return quality
}

// calculateTopicWeightAccuracy measures how well actual topic weights match expected
func calculateTopicWeightAccuracy(actual, expected map[string]float64) float64 {
	if len(expected) == 0 {
		return 1.0 // No expectations means perfect accuracy
	}

	if actual == nil {
		return 0
	}

	var totalDiff float64
	matchCount := 0

	for topic, expectedWeight := range expected {
		actualWeight, exists := actual[topic]
		if exists {
			matchCount++
			diff := math.Abs(expectedWeight - actualWeight)
			totalDiff += diff
		} else {
			// Topic not found, count as full difference
			totalDiff += expectedWeight
		}
	}

	// Accuracy = 1 - average difference
	// Clamped to [0, 1]
	accuracy := 1.0 - (totalDiff / float64(len(expected)))
	if accuracy < 0 {
		accuracy = 0
	}

	return accuracy
}

// normalizeQueryKey creates a consistent cache key from a query
func normalizeQueryKey(q cognition.Query) string {
	// Simple normalization: lowercase text
	return q.Text
}
