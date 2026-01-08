package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// AgenticEvaluator runs scenarios using the Claude CLI to measure tool usage.
type AgenticEvaluator struct {
	cli     *llm.ClaudeCLI
	verbose bool
	workDir string // Base working directory for scenarios
}

// NewAgenticEvaluator creates a new agentic evaluator.
func NewAgenticEvaluator(claudeBinary string, args ...string) (*AgenticEvaluator, error) {
	cli, err := llm.NewClaudeCLI(claudeBinary, args...)
	if err != nil {
		return nil, err
	}

	return &AgenticEvaluator{
		cli: cli,
	}, nil
}

// SetVerbose enables verbose output.
func (e *AgenticEvaluator) SetVerbose(v bool) {
	e.verbose = v
}

// SetWorkDir sets the base working directory for scenarios.
func (e *AgenticEvaluator) SetWorkDir(dir string) {
	e.workDir = dir
}

// AgenticTestResult holds results comparing baseline vs cortex tool usage.
type AgenticTestResult struct {
	TestID string `json:"test_id"`
	Query  string `json:"query"`

	// Baseline: Claude response WITHOUT any Cortex context
	BaselineToolCalls   int            `json:"baseline_tool_calls"`
	BaselineCallsByType map[string]int `json:"baseline_calls_by_type"`
	BaselineDurationMs  int64          `json:"baseline_duration_ms"`
	BaselineNumTurns    int            `json:"baseline_num_turns"`
	BaselineCostUSD     float64        `json:"baseline_cost_usd"`
	BaselineScore       float64        `json:"baseline_score"`

	// Cortex: Claude response WITH Cortex context injected
	CortexToolCalls   int            `json:"cortex_tool_calls"`
	CortexCallsByType map[string]int `json:"cortex_calls_by_type"`
	CortexDurationMs  int64          `json:"cortex_duration_ms"`
	CortexNumTurns    int            `json:"cortex_num_turns"`
	CortexCostUSD     float64        `json:"cortex_cost_usd"`
	CortexScore       float64        `json:"cortex_score"`

	// Reduction metrics
	ToolCallReduction float64 `json:"tool_call_reduction"` // (baseline - cortex) / baseline
	TimeReduction     float64 `json:"time_reduction"`      // (baseline - cortex) / baseline
	CostReduction     float64 `json:"cost_reduction"`      // (baseline - cortex) / baseline

	// Standard eval metrics
	Lift   float64 `json:"lift"`
	Winner string  `json:"winner"`
	Pass   bool    `json:"pass"`
}

// AgenticScenarioResult holds results for a scenario.
type AgenticScenarioResult struct {
	ScenarioID string               `json:"scenario_id"`
	Name       string               `json:"name"`
	Tests      []AgenticTestResult  `json:"tests"`

	// Average metrics
	AvgBaselineToolCalls float64 `json:"avg_baseline_tool_calls"`
	AvgCortexToolCalls   float64 `json:"avg_cortex_tool_calls"`
	AvgToolCallReduction float64 `json:"avg_tool_call_reduction"`
	AvgTimeReduction     float64 `json:"avg_time_reduction"`
	AvgCostReduction     float64 `json:"avg_cost_reduction"`
	AvgLift              float64 `json:"avg_lift"`

	// Win/loss
	CortexWins   int  `json:"cortex_wins"`
	BaselineWins int  `json:"baseline_wins"`
	Ties         int  `json:"ties"`
	Pass         bool `json:"pass"`
}

// AgenticResults holds overall agentic eval results.
type AgenticResults struct {
	Timestamp string                  `json:"timestamp"`
	Scenarios []AgenticScenarioResult `json:"scenarios"`

	// Overall metrics
	AvgToolCallReduction float64 `json:"avg_tool_call_reduction"`
	AvgTimeReduction     float64 `json:"avg_time_reduction"`
	AvgCostReduction     float64 `json:"avg_cost_reduction"`
	AvgLift              float64 `json:"avg_lift"`

	TotalBaselineToolCalls int `json:"total_baseline_tool_calls"`
	TotalCortexToolCalls   int `json:"total_cortex_tool_calls"`

	TotalCortexWins   int     `json:"total_cortex_wins"`
	TotalBaselineWins int     `json:"total_baseline_wins"`
	TotalTies         int     `json:"total_ties"`
	PassRate          float64 `json:"pass_rate"`
	Pass              bool    `json:"pass"`
}

// Run executes all scenarios in a directory.
func (e *AgenticEvaluator) Run(dir string) (*AgenticResults, error) {
	scenarios, err := LoadAll(dir)
	if err != nil {
		return nil, fmt.Errorf("load scenarios: %w", err)
	}

	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found in %s", dir)
	}

	var results []AgenticScenarioResult
	for _, s := range scenarios {
		result, err := e.RunScenario(s)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] %s: %v\n", s.ID, err)
			}
			continue
		}
		results = append(results, *result)
	}

	return e.calculateResults(results), nil
}

// RunScenario executes a single scenario with agentic measurement.
func (e *AgenticEvaluator) RunScenario(s *Scenario) (*AgenticScenarioResult, error) {
	if e.verbose {
		fmt.Printf("Running agentic scenario: %s\n", s.ID)
	}

	// Build cortex context from scenario
	cortexContext := buildCortexContext(s)

	ctx := context.Background()
	var testResults []AgenticTestResult

	for _, test := range s.Tests {
		result, err := e.runTest(ctx, test, cortexContext)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] Test %s failed: %v\n", test.ID, err)
			}
			continue
		}
		testResults = append(testResults, *result)
	}

	if len(testResults) == 0 {
		return nil, fmt.Errorf("no tests completed")
	}

	return e.calculateScenarioResult(s.ID, s.Name, testResults), nil
}

// runTest executes a single test: baseline vs cortex with tool tracking.
func (e *AgenticEvaluator) runTest(ctx context.Context, test Test, cortexContext string) (*AgenticTestResult, error) {
	if e.verbose {
		fmt.Printf("  Test: %s\n", test.ID)
		fmt.Printf("    Query: %s\n", truncateVerbose(test.Query, 80))
	}

	// 1. BASELINE: Run without context
	if e.verbose {
		fmt.Printf("    [baseline] Running without context...\n")
	}
	baseline, err := e.cli.RunBaseline(ctx, test.Query)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}

	baselineScore := Score(baseline.Response, test.Expect)

	if e.verbose {
		fmt.Printf("    [baseline] %d tool calls, %dms, score=%.2f\n",
			baseline.ToolStats.TotalCalls, baseline.DurationMs, baselineScore)
		for tool, count := range baseline.ToolStats.CallsByType {
			fmt.Printf("      %s: %d\n", tool, count)
		}
	}

	// 2. CORTEX: Run with context injected
	if e.verbose {
		fmt.Printf("    [cortex] Running with context (%d chars)...\n", len(cortexContext))
	}
	cortex, err := e.cli.RunWithContext(ctx, test.Query, cortexContext)
	if err != nil {
		return nil, fmt.Errorf("cortex: %w", err)
	}

	cortexScore := Score(cortex.Response, test.Expect)

	if e.verbose {
		fmt.Printf("    [cortex] %d tool calls, %dms, score=%.2f\n",
			cortex.ToolStats.TotalCalls, cortex.DurationMs, cortexScore)
		for tool, count := range cortex.ToolStats.CallsByType {
			fmt.Printf("      %s: %d\n", tool, count)
		}
	}

	// Calculate reductions
	toolCallReduction := calculateReduction(float64(baseline.ToolStats.TotalCalls), float64(cortex.ToolStats.TotalCalls))
	timeReduction := calculateReduction(float64(baseline.DurationMs), float64(cortex.DurationMs))
	costReduction := calculateReduction(baseline.TotalCostUSD, cortex.TotalCostUSD)

	lift := CalculateLift(cortexScore, baselineScore)
	winner := DetermineWinner(cortexScore, baselineScore)

	if e.verbose {
		fmt.Printf("    Tool call reduction: %.0f%%\n", toolCallReduction*100)
		fmt.Printf("    Time reduction: %.0f%%\n", timeReduction*100)
		fmt.Printf("    Cost reduction: %.0f%%\n", costReduction*100)
		fmt.Printf("    Winner: %s (lift=%.0f%%)\n", winner, lift*100)
	}

	return &AgenticTestResult{
		TestID: test.ID,
		Query:  test.Query,

		BaselineToolCalls:   baseline.ToolStats.TotalCalls,
		BaselineCallsByType: baseline.ToolStats.CallsByType,
		BaselineDurationMs:  baseline.DurationMs,
		BaselineNumTurns:    baseline.NumTurns,
		BaselineCostUSD:     baseline.TotalCostUSD,
		BaselineScore:       baselineScore,

		CortexToolCalls:   cortex.ToolStats.TotalCalls,
		CortexCallsByType: cortex.ToolStats.CallsByType,
		CortexDurationMs:  cortex.DurationMs,
		CortexNumTurns:    cortex.NumTurns,
		CortexCostUSD:     cortex.TotalCostUSD,
		CortexScore:       cortexScore,

		ToolCallReduction: toolCallReduction,
		TimeReduction:     timeReduction,
		CostReduction:     costReduction,

		Lift:   lift,
		Winner: winner,
		Pass:   cortexScore >= baselineScore && toolCallReduction >= 0,
	}, nil
}

// buildCortexContext creates the context string from scenario context items.
func buildCortexContext(s *Scenario) string {
	var parts []string

	for _, ctx := range s.Context {
		prefix := ""
		switch ctx.Type {
		case "decision":
			prefix = "Decision:"
		case "pattern":
			prefix = "Pattern:"
		case "correction":
			prefix = "Correction:"
		case "constraint":
			prefix = "Constraint:"
		default:
			prefix = fmt.Sprintf("[%s]", ctx.Type)
		}
		parts = append(parts, fmt.Sprintf("%s %s", prefix, ctx.Content))
	}

	// Include events as context too
	for _, event := range s.Events {
		parts = append(parts, fmt.Sprintf("[%s] %s", event.Time, event.Content))
	}

	return strings.Join(parts, "\n\n")
}

// calculateReduction computes (baseline - cortex) / baseline.
func calculateReduction(baseline, cortex float64) float64 {
	if baseline == 0 {
		if cortex == 0 {
			return 0
		}
		return -1 // Cortex used more when baseline used none (bad)
	}
	return (baseline - cortex) / baseline
}

// calculateScenarioResult aggregates test results.
func (e *AgenticEvaluator) calculateScenarioResult(scenarioID, name string, tests []AgenticTestResult) *AgenticScenarioResult {
	if len(tests) == 0 {
		return &AgenticScenarioResult{
			ScenarioID: scenarioID,
			Name:       name,
			Pass:       false,
		}
	}

	var totalBaselineTools, totalCortexTools float64
	var totalToolReduction, totalTimeReduction, totalCostReduction, totalLift float64
	cortexWins, baselineWins, ties := 0, 0, 0

	for _, t := range tests {
		totalBaselineTools += float64(t.BaselineToolCalls)
		totalCortexTools += float64(t.CortexToolCalls)
		totalToolReduction += t.ToolCallReduction
		totalTimeReduction += t.TimeReduction
		totalCostReduction += t.CostReduction
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

	return &AgenticScenarioResult{
		ScenarioID:           scenarioID,
		Name:                 name,
		Tests:                tests,
		AvgBaselineToolCalls: totalBaselineTools / n,
		AvgCortexToolCalls:   totalCortexTools / n,
		AvgToolCallReduction: totalToolReduction / n,
		AvgTimeReduction:     totalTimeReduction / n,
		AvgCostReduction:     totalCostReduction / n,
		AvgLift:              totalLift / n,
		CortexWins:           cortexWins,
		BaselineWins:         baselineWins,
		Ties:                 ties,
		Pass:                 totalToolReduction/n >= 0, // Cortex doesn't increase tool calls
	}
}

// CalculateAgenticResults aggregates scenario results into overall results.
// This is exported so it can be used when running single scenarios.
func CalculateAgenticResults(scenarios []AgenticScenarioResult) *AgenticResults {
	return calculateAgenticResults(scenarios)
}

// calculateResults aggregates all scenario results.
func (e *AgenticEvaluator) calculateResults(scenarios []AgenticScenarioResult) *AgenticResults {
	return calculateAgenticResults(scenarios)
}

// calculateAgenticResults is the internal implementation.
func calculateAgenticResults(scenarios []AgenticScenarioResult) *AgenticResults {
	if len(scenarios) == 0 {
		return &AgenticResults{
			Timestamp: Timestamp(),
			Pass:      false,
		}
	}

	var totalToolReduction, totalTimeReduction, totalCostReduction, totalLift float64
	var totalBaselineTools, totalCortexTools int
	totalCortexWins, totalBaselineWins, totalTies := 0, 0, 0
	passCount := 0

	for _, s := range scenarios {
		totalToolReduction += s.AvgToolCallReduction
		totalTimeReduction += s.AvgTimeReduction
		totalCostReduction += s.AvgCostReduction
		totalLift += s.AvgLift
		totalBaselineTools += int(s.AvgBaselineToolCalls * float64(len(s.Tests)))
		totalCortexTools += int(s.AvgCortexToolCalls * float64(len(s.Tests)))
		totalCortexWins += s.CortexWins
		totalBaselineWins += s.BaselineWins
		totalTies += s.Ties

		if s.Pass {
			passCount++
		}
	}

	n := float64(len(scenarios))

	return &AgenticResults{
		Timestamp:              Timestamp(),
		Scenarios:              scenarios,
		AvgToolCallReduction:   totalToolReduction / n,
		AvgTimeReduction:       totalTimeReduction / n,
		AvgCostReduction:       totalCostReduction / n,
		AvgLift:                totalLift / n,
		TotalBaselineToolCalls: totalBaselineTools,
		TotalCortexToolCalls:   totalCortexTools,
		TotalCortexWins:        totalCortexWins,
		TotalBaselineWins:      totalBaselineWins,
		TotalTies:              totalTies,
		PassRate:               float64(passCount) / n,
		Pass:                   totalToolReduction/n >= 0,
	}
}

// SetupTestWorkspace creates a temp workspace with test files for agentic scenarios.
// Returns the workspace path and a cleanup function.
func SetupTestWorkspace(files map[string]string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "cortex-agentic-*")
	if err != nil {
		return "", nil, err
	}

	cleanup := func() {
		os.RemoveAll(dir)
	}

	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	return dir, cleanup, nil
}
