package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// E2EEvaluator runs end-to-end evaluations through the real Cortex pipeline
type E2EEvaluator struct {
	provider llm.Provider
	scorer   *Scorer
	verbose  bool
	cfg      *config.Config
}

// NewE2EEvaluator creates a new E2E evaluator
func NewE2EEvaluator(provider llm.Provider, cfg *config.Config) *E2EEvaluator {
	return &E2EEvaluator{
		provider: provider,
		scorer:   NewScorer(),
		cfg:      cfg,
	}
}

// SetVerbose enables verbose output
func (e *E2EEvaluator) SetVerbose(v bool) {
	e.verbose = v
}

// RunE2EScenario executes an end-to-end evaluation scenario
func (e *E2EEvaluator) RunE2EScenario(scenario *Scenario) ([]EvalResult, error) {
	if scenario.Type != ScenarioE2E {
		return nil, fmt.Errorf("scenario %s is not an E2E scenario", scenario.ID)
	}

	if e.verbose {
		fmt.Printf("\n=== E2E Eval: %s ===\n", scenario.Name)
		fmt.Printf("Learning chain: %d turns\n", len(scenario.LearningChain))
		fmt.Printf("Recall prompts: %d\n", len(scenario.RecallPrompts))
	}

	// Create temporary Cortex environment
	tmpDir, err := os.MkdirTemp("", "cortex-eval-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create required subdirectories
	dbDir := filepath.Join(tmpDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db dir: %w", err)
	}

	// Create temp config for this eval
	evalCfg := &config.Config{
		ContextDir:  tmpDir,
		OllamaURL:   e.cfg.OllamaURL,
		OllamaModel: e.cfg.OllamaModel,
	}

	// Initialize storage in temp dir
	store, err := storage.New(evalCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	defer store.Close()

	if e.verbose {
		fmt.Println("\n--- Phase 1: Learning Chain ---")
	}

	// Phase 1: Simulate the learning chain
	// Events are stored; analysis happens via cognitive modes (Dream/Think)
	err = e.simulateLearningChain(scenario.LearningChain, store)
	if err != nil {
		return nil, fmt.Errorf("failed to simulate learning chain: %w", err)
	}

	if e.verbose {
		fmt.Println("\n--- Phase 2: Recall Tests ---")
	}

	// Phase 2: Run recall prompts with A/B comparison
	var results []EvalResult
	for _, recallPrompt := range scenario.RecallPrompts {
		result, err := e.evaluateRecallPrompt(scenario.ID, recallPrompt, store)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate recall prompt %s: %w", recallPrompt.ID, err)
		}
		results = append(results, result)
	}

	return results, nil
}

// simulateLearningChain processes the learning conversation through Cortex
func (e *E2EEvaluator) simulateLearningChain(chain []ConversationTurn, store *storage.Storage) error {
	for i, turn := range chain {
		if e.verbose {
			fmt.Printf("  Turn %d [%s]: %s\n", i+1, turn.Role, truncateStr(turn.Content, 60))
		}

		// Process tool calls as Cortex events
		for _, toolCall := range turn.ToolCalls {
			event := e.toolCallToEvent(toolCall, turn.Content)

			// Store the event
			if err := store.StoreEvent(event); err != nil {
				return fmt.Errorf("failed to store event: %w", err)
			}

			if e.verbose {
				fmt.Printf("    → Captured: %s %s\n", toolCall.Tool, toolCall.File)
			}

			// Note: Analysis now happens via cognitive modes (Dream/Think)
			// Events are stored and will be analyzed asynchronously
		}

		// Also capture the conversation content as context
		if turn.Role == "assistant" && len(turn.ToolCalls) == 0 {
			// Assistant reasoning without tool calls - capture as insight
			event := &events.Event{
				ID:        fmt.Sprintf("conv-%d-%d", time.Now().UnixNano(), i),
				Timestamp: time.Now(),
				Source:    events.SourceGeneric,
				EventType: events.EventAgent,
				ToolName:  "Conversation",
				ToolInput: map[string]interface{}{
					"role": turn.Role,
				},
				ToolResult: turn.Content,
				Context: events.EventContext{
					SessionID: "eval-session",
				},
			}
			if err := store.StoreEvent(event); err != nil {
				return fmt.Errorf("failed to store conversation: %w", err)
			}
		}
	}

	// Give time for async processing if any
	time.Sleep(100 * time.Millisecond)

	return nil
}

// toolCallToEvent converts a tool call to a Cortex event
func (e *E2EEvaluator) toolCallToEvent(tc ToolCall, context string) *events.Event {
	input := make(map[string]interface{})
	if tc.File != "" {
		input["file_path"] = tc.File
	}
	if tc.Input != "" {
		input["input"] = tc.Input
	}

	result := tc.Content
	if result == "" {
		result = context // Use conversation context if no explicit result
	}

	return &events.Event{
		ID:        fmt.Sprintf("eval-%d", time.Now().UnixNano()),
		Timestamp: time.Now(),
		Source:    events.SourceGeneric,
		EventType: events.EventToolUse,
		ToolName:  tc.Tool,
		ToolInput: input,
		ToolResult: result,
		Context: events.EventContext{
			SessionID: "eval-session",
		},
	}
}

// evaluateRecallPrompt runs A/B comparison for a recall prompt
func (e *E2EEvaluator) evaluateRecallPrompt(scenarioID string, prompt RecallPrompt, store *storage.Storage) (EvalResult, error) {
	ctx := context.Background()
	result := EvalResult{
		ScenarioID: scenarioID,
		PromptID:   prompt.ID,
		Prompt:     prompt.Prompt,
	}

	// Search for relevant context from stored insights
	relevantContext := e.searchRelevantContext(prompt.Prompt, store)

	if e.verbose {
		if relevantContext != "" {
			fmt.Printf("  Found context: %s\n", truncateStr(relevantContext, 100))
		} else {
			fmt.Printf("  No relevant context found\n")
		}
	}

	// Condition A: With Cortex context
	startA := time.Now()
	responseA, errA := e.provider.GenerateWithSystem(ctx, prompt.Prompt, relevantContext)
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

	// Score both responses
	scoresA, assertionsA := e.scorer.ScoreResponse(responseA, prompt.GroundTruth)
	scoresB, _ := e.scorer.ScoreResponse(responseB, prompt.GroundTruth)

	result.Assertions = assertionsA
	result.Scores = scoresA
	result.Winner = e.scorer.CompareScores(scoresA, scoresB)
	result.Pass = e.scorer.AllAssertionsPass(assertionsA)

	if e.verbose {
		fmt.Printf("  %s: cortex=%.2f baseline=%.2f winner=%s\n",
			prompt.ID, scoresA.Overall, scoresB.Overall, result.Winner)
	}

	return result, nil
}

// searchRelevantContext searches stored insights for relevant context
func (e *E2EEvaluator) searchRelevantContext(prompt string, store *storage.Storage) string {
	// Search for relevant events
	searchResults, err := store.SearchEvents(prompt, 5)
	if err != nil || len(searchResults) == 0 {
		return ""
	}

	// Build context from search results
	var contextParts []string
	contextParts = append(contextParts, "# Relevant Project Context\n")

	for _, event := range searchResults {
		var part string
		if fp, ok := event.ToolInput["file_path"].(string); ok && fp != "" {
			part = fmt.Sprintf("## %s (%s)\n%s\n", event.ToolName, filepath.Base(fp), event.ToolResult)
		} else {
			part = fmt.Sprintf("## %s\n%s\n", event.ToolName, event.ToolResult)
		}
		contextParts = append(contextParts, part)
	}

	// Also get stored insights
	insights, err := store.GetRecentInsights(5)
	if err == nil && len(insights) > 0 {
		contextParts = append(contextParts, "\n# Key Insights\n")
		for _, insight := range insights {
			contextParts = append(contextParts, fmt.Sprintf("- [%s] %s\n", insight.Category, insight.Summary))
		}
	}

	return strings.Join(contextParts, "\n")
}

// truncateStr truncates a string for display
func truncateStr(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// RunE2E runs all E2E scenarios in a directory
func (e *E2EEvaluator) RunE2E(scenarioDir string) (*EvalRun, error) {
	scenarios, err := LoadScenarios(scenarioDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load scenarios: %w", err)
	}

	// Filter to E2E scenarios only
	var e2eScenarios []*Scenario
	for _, s := range scenarios {
		if s.Type == ScenarioE2E {
			e2eScenarios = append(e2eScenarios, s)
		}
	}

	if len(e2eScenarios) == 0 {
		return nil, fmt.Errorf("no E2E scenarios found in %s", scenarioDir)
	}

	run := &EvalRun{
		ID:        fmt.Sprintf("e2e-eval-%s", time.Now().Format("20060102-150405")),
		Timestamp: time.Now(),
		Provider:  e.provider.Name(),
		Scenarios: make([]string, 0, len(e2eScenarios)),
		Results:   make([]EvalResult, 0),
	}

	for _, scenario := range e2eScenarios {
		run.Scenarios = append(run.Scenarios, scenario.ID)

		results, err := e.RunE2EScenario(scenario)
		if err != nil {
			return nil, fmt.Errorf("failed to run scenario %s: %w", scenario.ID, err)
		}

		run.Results = append(run.Results, results...)
	}

	// Calculate summary using the same method as regular evals
	run.Summary = calculateE2ESummary(run.Results)

	return run, nil
}

// calculateE2ESummary aggregates E2E results
func calculateE2ESummary(results []EvalResult) RunSummary {
	summary := RunSummary{
		TotalPrompts: len(results),
	}

	scenarioSet := make(map[string]bool)
	var totalScore float64

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

		totalScore += r.Scores.Overall
	}

	summary.TotalScenarios = len(scenarioSet)

	if summary.TotalPrompts > 0 {
		summary.PassRate = float64(summary.PassCount) / float64(summary.TotalPrompts)
		summary.WinRate = float64(summary.CortexWins) / float64(summary.TotalPrompts)
		summary.AvgDelta = totalScore / float64(summary.TotalPrompts)
	}

	return summary
}
