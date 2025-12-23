package eval

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Evaluator runs evaluation scenarios
type Evaluator struct {
	provider llm.Provider
	scorer   *Scorer
	verbose  bool
}

// NewEvaluator creates a new evaluator
func NewEvaluator(provider llm.Provider) *Evaluator {
	return &Evaluator{
		provider: provider,
		scorer:   NewScorer(),
		verbose:  false,
	}
}

// SetVerbose enables verbose output during evaluation
func (e *Evaluator) SetVerbose(v bool) {
	e.verbose = v
}

// RunScenario evaluates a single scenario with A/B comparison
func (e *Evaluator) RunScenario(scenario *Scenario) ([]EvalResult, error) {
	var results []EvalResult

	// Build context string from context chain
	contextStr := scenario.BuildContextString()

	if e.verbose {
		fmt.Printf("Running scenario: %s (%d prompts)\n", scenario.ID, len(scenario.TestPrompts))
	}

	for _, testPrompt := range scenario.TestPrompts {
		result, err := e.evaluatePrompt(scenario.ID, testPrompt, contextStr)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate prompt %s: %w", testPrompt.ID, err)
		}
		results = append(results, result)
	}

	return results, nil
}

// evaluatePrompt runs A/B comparison for a single prompt
func (e *Evaluator) evaluatePrompt(scenarioID string, testPrompt TestPrompt, contextStr string) (EvalResult, error) {
	ctx := context.Background()
	result := EvalResult{
		ScenarioID: scenarioID,
		PromptID:   testPrompt.ID,
		Prompt:     testPrompt.Prompt,
	}

	// Condition A: With Cortex context
	startA := time.Now()
	responseA, errA := e.provider.GenerateWithSystem(ctx, testPrompt.Prompt, contextStr)
	latencyA := time.Since(startA).Milliseconds()

	result.WithCortex = Response{
		Output:   responseA,
		Latency:  latencyA,
		Provider: e.provider.Name(),
	}
	if errA != nil {
		result.WithCortex.Error = errA.Error()
	}

	// Condition B: Without context (baseline)
	startB := time.Now()
	responseB, errB := e.provider.Generate(ctx, testPrompt.Prompt)
	latencyB := time.Since(startB).Milliseconds()

	result.WithoutCortex = Response{
		Output:   responseB,
		Latency:  latencyB,
		Provider: e.provider.Name(),
	}
	if errB != nil {
		result.WithoutCortex.Error = errB.Error()
	}

	// Score both responses
	scoresA, assertionsA := e.scorer.ScoreResponse(responseA, testPrompt.GroundTruth)
	scoresB, _ := e.scorer.ScoreResponse(responseB, testPrompt.GroundTruth)

	// Use cortex assertions for the result (primary evaluation)
	result.Assertions = assertionsA
	result.Scores = scoresA

	// Determine winner
	result.Winner = e.scorer.CompareScores(scoresA, scoresB)
	result.Pass = e.scorer.AllAssertionsPass(assertionsA)

	// Store both scores for comparison
	result.Scores.Overall = scoresA.Overall

	if e.verbose {
		fmt.Printf("  %s: cortex=%.2f baseline=%.2f winner=%s\n",
			testPrompt.ID, scoresA.Overall, scoresB.Overall, result.Winner)
	}

	return result, nil
}

// RunAll evaluates all scenarios in a directory
func (e *Evaluator) RunAll(scenarioDir string) (*EvalRun, error) {
	scenarios, err := LoadScenarios(scenarioDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load scenarios: %w", err)
	}

	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found in %s", scenarioDir)
	}

	run := &EvalRun{
		ID:        fmt.Sprintf("eval-%s", time.Now().Format("20060102-150405")),
		Timestamp: time.Now(),
		Provider:  e.provider.Name(),
		Scenarios: make([]string, 0, len(scenarios)),
		Results:   make([]EvalResult, 0),
	}

	for _, scenario := range scenarios {
		run.Scenarios = append(run.Scenarios, scenario.ID)

		results, err := e.RunScenario(scenario)
		if err != nil {
			return nil, fmt.Errorf("failed to run scenario %s: %w", scenario.ID, err)
		}

		run.Results = append(run.Results, results...)
	}

	// Calculate summary
	run.Summary = e.calculateSummary(run.Results)

	return run, nil
}

// RunSingle evaluates a single scenario file
func (e *Evaluator) RunSingle(scenarioPath string) (*EvalRun, error) {
	scenario, err := LoadScenario(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load scenario: %w", err)
	}

	run := &EvalRun{
		ID:        fmt.Sprintf("eval-%s", time.Now().Format("20060102-150405")),
		Timestamp: time.Now(),
		Provider:  e.provider.Name(),
		Scenarios: []string{scenario.ID},
		Results:   make([]EvalResult, 0),
	}

	results, err := e.RunScenario(scenario)
	if err != nil {
		return nil, err
	}

	run.Results = results
	run.Summary = e.calculateSummary(run.Results)

	return run, nil
}

// calculateSummary aggregates results into summary statistics
func (e *Evaluator) calculateSummary(results []EvalResult) RunSummary {
	summary := RunSummary{
		TotalPrompts: len(results),
	}

	// Track unique scenarios
	scenarioSet := make(map[string]bool)

	var totalDelta float64

	for _, r := range results {
		scenarioSet[r.ScenarioID] = true

		if r.Pass {
			summary.PassCount++
		} else {
			summary.FailCount++
		}

		switch r.Winner {
		case "cortex":
			summary.CortexWins++
		case "baseline":
			summary.BaselineWins++
		case "tie":
			summary.Ties++
		}

		// Calculate delta (would need baseline score stored - simplified here)
		totalDelta += r.Scores.Overall
	}

	summary.TotalScenarios = len(scenarioSet)

	if summary.TotalPrompts > 0 {
		summary.PassRate = float64(summary.PassCount) / float64(summary.TotalPrompts)
		summary.WinRate = float64(summary.CortexWins) / float64(summary.TotalPrompts)
		summary.AvgDelta = totalDelta / float64(summary.TotalPrompts)
	}

	return summary
}
