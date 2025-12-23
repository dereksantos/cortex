package eval

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TreeEvaluator handles multi-path and tree evaluation scenarios
type TreeEvaluator struct {
	provider llm.Provider
	scorer   *Scorer
	verbose  bool
}

// NewTreeEvaluator creates a new tree evaluator
func NewTreeEvaluator(provider llm.Provider) *TreeEvaluator {
	return &TreeEvaluator{
		provider: provider,
		scorer:   NewScorer(),
		verbose:  false,
	}
}

// SetVerbose enables verbose output
func (e *TreeEvaluator) SetVerbose(v bool) {
	e.verbose = v
}

// TreeEvalResult extends EvalResult with path-specific data
type TreeEvalResult struct {
	EvalResult

	// Path information
	PathID        string `json:"path_id"`
	PathName      string `json:"path_name"`
	ContextPathID string `json:"context_path_id"` // Which path's context was used

	// Cross-contamination detection
	IsContaminationTest bool    `json:"is_contamination_test"`
	ContaminationScore  float64 `json:"contamination_score,omitempty"`
}

// TreeEvalRun represents a complete tree evaluation run
type TreeEvalRun struct {
	ID        string           `json:"id"`
	Timestamp time.Time        `json:"timestamp"`
	Provider  string           `json:"provider"`
	Scenario  string           `json:"scenario"`
	Results   []TreeEvalResult `json:"results"`
	Summary   TreeSummary      `json:"summary"`
}

// TreeSummary contains tree-eval specific metrics
type TreeSummary struct {
	RunSummary

	// Per-path statistics
	PathStats map[string]PathStats `json:"path_stats"`

	// Contamination metrics
	ContaminationTests    int     `json:"contamination_tests"`
	ContaminationDetected int     `json:"contamination_detected"`
	ContaminationRate     float64 `json:"contamination_rate"`

	// Path adherence
	AvgPathAdherence float64 `json:"avg_path_adherence"`
}

// PathStats contains statistics for a single path
type PathStats struct {
	PathID     string  `json:"path_id"`
	PathName   string  `json:"path_name"`
	Prompts    int     `json:"prompts"`
	PassCount  int     `json:"pass_count"`
	PassRate   float64 `json:"pass_rate"`
	CortexWins int     `json:"cortex_wins"`
	AvgScore   float64 `json:"avg_score"`
}

// RunMultiPath evaluates a multi-path scenario
func (e *TreeEvaluator) RunMultiPath(scenario *Scenario) (*TreeEvalRun, error) {
	if len(scenario.Paths) < 2 {
		return nil, fmt.Errorf("multi-path scenario requires at least 2 paths")
	}

	run := &TreeEvalRun{
		ID:        fmt.Sprintf("tree-eval-%s", time.Now().Format("20060102-150405")),
		Timestamp: time.Now(),
		Provider:  e.provider.Name(),
		Scenario:  scenario.ID,
		Results:   make([]TreeEvalResult, 0),
	}

	if e.verbose {
		fmt.Printf("Running multi-path scenario: %s (%d paths)\n", scenario.ID, len(scenario.Paths))
	}

	// Phase 1: Run each path with its own context
	for _, path := range scenario.Paths {
		if e.verbose {
			fmt.Printf("  Path %s: %s (%d prompts)\n", path.ID, path.Name, len(path.TestPrompts))
		}

		contextStr := buildPathContext(path)

		for _, prompt := range path.TestPrompts {
			result, err := e.evaluatePathPrompt(path, prompt, contextStr, false)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate prompt %s: %w", prompt.ID, err)
			}
			run.Results = append(run.Results, result)
		}
	}

	// Phase 2: Cross-contamination tests
	// Run Path A prompts with Path B context (should score lower)
	for i, pathA := range scenario.Paths {
		for j, pathB := range scenario.Paths {
			if i == j {
				continue // Skip same path
			}

			if e.verbose {
				fmt.Printf("  Contamination test: %s prompts with %s context\n", pathA.Name, pathB.Name)
			}

			// Use pathB's context for pathA's prompts
			wrongContext := buildPathContext(pathB)

			for _, prompt := range pathA.TestPrompts {
				result, err := e.evaluateContamination(pathA, pathB, prompt, wrongContext)
				if err != nil {
					return nil, fmt.Errorf("contamination test failed: %w", err)
				}
				run.Results = append(run.Results, result)
			}
		}
	}

	// Calculate summary
	run.Summary = e.calculateTreeSummary(run.Results, scenario.Paths)

	return run, nil
}

// evaluatePathPrompt evaluates a single prompt within a path
func (e *TreeEvaluator) evaluatePathPrompt(path Path, prompt TestPrompt, contextStr string, isContamination bool) (TreeEvalResult, error) {
	ctx := context.Background()

	result := TreeEvalResult{
		PathID:              path.ID,
		PathName:            path.Name,
		ContextPathID:       path.ID,
		IsContaminationTest: isContamination,
	}

	result.EvalResult = EvalResult{
		ScenarioID: path.ID,
		PromptID:   prompt.ID,
		Prompt:     prompt.Prompt,
	}

	// With context
	startA := time.Now()
	responseA, errA := e.provider.GenerateWithSystem(ctx, prompt.Prompt, contextStr)
	latencyA := time.Since(startA).Milliseconds()

	result.WithCortex = Response{
		Output:   responseA,
		Latency:  latencyA,
		Provider: e.provider.Name(),
	}
	if errA != nil {
		result.WithCortex.Error = errA.Error()
	}

	// Without context (baseline)
	startB := time.Now()
	responseB, errB := e.provider.Generate(ctx, prompt.Prompt)
	latencyB := time.Since(startB).Milliseconds()

	result.WithoutCortex = Response{
		Output:   responseB,
		Latency:  latencyB,
		Provider: e.provider.Name(),
	}
	if errB != nil {
		result.WithoutCortex.Error = errB.Error()
	}

	// Score
	scoresA, assertionsA := e.scorer.ScoreResponse(responseA, prompt.GroundTruth)
	scoresB, _ := e.scorer.ScoreResponse(responseB, prompt.GroundTruth)

	result.Assertions = assertionsA
	result.Scores = scoresA
	result.Winner = e.scorer.CompareScores(scoresA, scoresB)
	result.Pass = e.scorer.AllAssertionsPass(assertionsA)

	if e.verbose {
		fmt.Printf("    %s: score=%.2f winner=%s\n", prompt.ID, scoresA.Overall, result.Winner)
	}

	return result, nil
}

// evaluateContamination tests if wrong context hurts performance
func (e *TreeEvaluator) evaluateContamination(correctPath, wrongPath Path, prompt TestPrompt, wrongContext string) (TreeEvalResult, error) {
	ctx := context.Background()

	result := TreeEvalResult{
		PathID:              correctPath.ID,
		PathName:            correctPath.Name,
		ContextPathID:       wrongPath.ID, // Using wrong path's context
		IsContaminationTest: true,
	}

	result.EvalResult = EvalResult{
		ScenarioID: fmt.Sprintf("%s-contaminated-by-%s", correctPath.ID, wrongPath.ID),
		PromptID:   prompt.ID,
		Prompt:     prompt.Prompt,
	}

	// With WRONG context (contamination test)
	startA := time.Now()
	responseA, errA := e.provider.GenerateWithSystem(ctx, prompt.Prompt, wrongContext)
	latencyA := time.Since(startA).Milliseconds()

	result.WithCortex = Response{
		Output:   responseA,
		Latency:  latencyA,
		Provider: e.provider.Name(),
	}
	if errA != nil {
		result.WithCortex.Error = errA.Error()
	}

	// Baseline (no context)
	startB := time.Now()
	responseB, errB := e.provider.Generate(ctx, prompt.Prompt)
	latencyB := time.Since(startB).Milliseconds()

	result.WithoutCortex = Response{
		Output:   responseB,
		Latency:  latencyB,
		Provider: e.provider.Name(),
	}
	if errB != nil {
		result.WithoutCortex.Error = errB.Error()
	}

	// Score against CORRECT path's ground truth
	scoresA, assertionsA := e.scorer.ScoreResponse(responseA, prompt.GroundTruth)
	scoresB, _ := e.scorer.ScoreResponse(responseB, prompt.GroundTruth)

	result.Assertions = assertionsA
	result.Scores = scoresA
	result.Winner = e.scorer.CompareScores(scoresA, scoresB)
	result.Pass = e.scorer.AllAssertionsPass(assertionsA)

	// Contamination score: how much worse is wrong context vs baseline?
	// If wrong context hurts (score < baseline), contamination is detected
	result.ContaminationScore = scoresB.Overall - scoresA.Overall

	if e.verbose {
		contaminated := "no"
		if result.ContaminationScore > 0.1 {
			contaminated = "YES"
		}
		fmt.Printf("    %s: contamination=%s (delta=%.2f)\n", prompt.ID, contaminated, result.ContaminationScore)
	}

	return result, nil
}

// buildPathContext creates context string from a path
func buildPathContext(path Path) string {
	result := fmt.Sprintf("# %s\n\n", path.Name)
	if path.Description != "" {
		result += path.Description + "\n\n"
	}

	for _, event := range path.ContextChain {
		result += fmt.Sprintf("## %s", capitalizeFirst(event.Type))
		if event.File != "" {
			result += fmt.Sprintf(" (%s)", event.File)
		}
		result += "\n\n"
		result += event.Content + "\n"
		if event.Rationale != "" {
			result += fmt.Sprintf("\nRationale: %s\n", event.Rationale)
		}
		result += "\n"
	}

	return result
}

// calculateTreeSummary computes tree-specific summary statistics
func (e *TreeEvaluator) calculateTreeSummary(results []TreeEvalResult, paths []Path) TreeSummary {
	summary := TreeSummary{
		PathStats: make(map[string]PathStats),
	}

	// Initialize path stats
	for _, path := range paths {
		summary.PathStats[path.ID] = PathStats{
			PathID:   path.ID,
			PathName: path.Name,
		}
	}

	// Calculate per-path and overall stats
	var totalScore float64
	contamTests := 0
	contamDetected := 0

	for _, r := range results {
		summary.TotalPrompts++

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
		default:
			summary.Ties++
		}

		totalScore += r.Scores.Overall

		// Track per-path stats (for non-contamination tests)
		if !r.IsContaminationTest {
			stats := summary.PathStats[r.PathID]
			stats.Prompts++
			if r.Pass {
				stats.PassCount++
			}
			if r.Winner == "cortex" {
				stats.CortexWins++
			}
			stats.AvgScore += r.Scores.Overall
			summary.PathStats[r.PathID] = stats
		} else {
			// Contamination test
			contamTests++
			if r.ContaminationScore > 0.1 { // Threshold: 10% worse
				contamDetected++
			}
		}
	}

	// Finalize averages
	if summary.TotalPrompts > 0 {
		summary.PassRate = float64(summary.PassCount) / float64(summary.TotalPrompts)
		summary.WinRate = float64(summary.CortexWins) / float64(summary.TotalPrompts)
		summary.AvgDelta = totalScore / float64(summary.TotalPrompts)
	}

	for id, stats := range summary.PathStats {
		if stats.Prompts > 0 {
			stats.PassRate = float64(stats.PassCount) / float64(stats.Prompts)
			stats.AvgScore = stats.AvgScore / float64(stats.Prompts)
			summary.PathStats[id] = stats
		}
	}

	// Contamination summary
	summary.ContaminationTests = contamTests
	summary.ContaminationDetected = contamDetected
	if contamTests > 0 {
		summary.ContaminationRate = float64(contamDetected) / float64(contamTests)
	}

	// Calculate path adherence (inverse of contamination rate)
	summary.AvgPathAdherence = 1 - summary.ContaminationRate

	// Count unique scenarios (paths)
	summary.TotalScenarios = len(paths)

	return summary
}

// RunTemporal evaluates a temporal/migration scenario
func (e *TreeEvaluator) RunTemporal(scenario *Scenario) (*TreeEvalRun, error) {
	if len(scenario.Phases) < 2 {
		return nil, fmt.Errorf("temporal scenario requires at least 2 phases")
	}

	run := &TreeEvalRun{
		ID:        fmt.Sprintf("temporal-eval-%s", time.Now().Format("20060102-150405")),
		Timestamp: time.Now(),
		Provider:  e.provider.Name(),
		Scenario:  scenario.ID,
		Results:   make([]TreeEvalResult, 0),
	}

	if e.verbose {
		fmt.Printf("Running temporal scenario: %s (%d phases)\n", scenario.ID, len(scenario.Phases))
	}

	// Run each phase with cumulative context up to that point
	cumulativeContext := ""
	for i, phase := range scenario.Phases {
		if e.verbose {
			fmt.Printf("  Phase %s: %s\n", phase.ID, phase.Name)
		}

		// Add this phase's context
		cumulativeContext += fmt.Sprintf("\n## Phase: %s\n\n", phase.Name)
		for _, event := range phase.ContextChain {
			cumulativeContext += event.Content + "\n"
		}

		// Convert Phase to Path for evaluation
		path := Path{
			ID:           phase.ID,
			Name:         phase.Name,
			ContextChain: phase.ContextChain,
			TestPrompts:  phase.TestPrompts,
		}

		for _, prompt := range phase.TestPrompts {
			result, err := e.evaluatePathPrompt(path, prompt, cumulativeContext, false)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate prompt %s: %w", prompt.ID, err)
			}
			run.Results = append(run.Results, result)
		}

		// Test temporal contamination: use future context for past prompts
		if i > 0 {
			prevPhase := scenario.Phases[i-1]
			for _, prompt := range prevPhase.TestPrompts {
				// Use current (future) context for previous phase's prompts
				prevPath := Path{ID: prevPhase.ID, Name: prevPhase.Name}
				result, err := e.evaluateContamination(prevPath, path, prompt, cumulativeContext)
				if err != nil {
					return nil, err
				}
				run.Results = append(run.Results, result)
			}
		}
	}

	// Calculate summary using paths derived from phases
	var paths []Path
	for _, phase := range scenario.Phases {
		paths = append(paths, Path{ID: phase.ID, Name: phase.Name})
	}
	run.Summary = e.calculateTreeSummary(run.Results, paths)

	return run, nil
}
