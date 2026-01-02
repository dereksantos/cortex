package eval

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// JourneyEvaluator runs end-to-end generative evaluations based on journey scenarios.
// It orchestrates LLM tasks, runs tests, checks patterns, and tracks metrics.
// This differs from E2EEvaluator which handles simpler learning-chain based evals.
type JourneyEvaluator struct {
	cortex        cognition.Cortex
	provider      llm.Provider
	judgeProvider llm.Provider // Separate judge provider for LLM-as-judge (can be same as provider)
	projectDir    string       // Path to scaffold project
	workDir       string       // Temp working directory
	verbose       bool

	// Event storage for tracking stored events during evaluation
	storedEvents map[string]*events.Event

	// Token tracking
	totalTokens int
}

// NewJourneyEvaluator creates a new journey-based E2E evaluator.
func NewJourneyEvaluator(cortex cognition.Cortex, provider llm.Provider, projectDir string, verbose bool) *JourneyEvaluator {
	return &JourneyEvaluator{
		cortex:       cortex,
		provider:     provider,
		projectDir:   projectDir,
		verbose:      verbose,
		storedEvents: make(map[string]*events.Event),
	}
}

// SetJudgeProvider sets a separate LLM provider for code review judging.
// If not set, no LLM-as-judge evaluation will be performed.
func (e *JourneyEvaluator) SetJudgeProvider(provider llm.Provider) {
	e.judgeProvider = provider
}

// RunJourney executes a complete E2E journey evaluation.
// Returns results for both Cortex-assisted (treatment) and baseline runs.
func (e *JourneyEvaluator) RunJourney(ctx context.Context, journey *E2EJourney) (*E2EJourneyResult, error) {
	startTime := time.Now()

	result := &E2EJourneyResult{
		JourneyID: journey.ID,
		RunID:     generateRunID(),
		StartTime: startTime,
	}

	// Validate journey before running
	if err := journey.Validate(); err != nil {
		return nil, fmt.Errorf("journey validation failed: %w", err)
	}

	if e.verbose {
		fmt.Printf("Starting E2E journey: %s (%s)\n", journey.Name, journey.ID)
		fmt.Printf("  Project: %s\n", journey.Project.Name)
		fmt.Printf("  Sessions: %d, Tasks: %d, Events: %d\n",
			len(journey.Sessions), journey.TotalTasks(), journey.TotalEvents())
	}

	// Run treatment (Cortex-assisted)
	if e.verbose {
		fmt.Println("\n=== Treatment Run (Cortex enabled) ===")
	}
	treatmentResults, err := e.runJourneyRun(ctx, journey, true)
	if err != nil {
		return nil, fmt.Errorf("treatment run failed: %w", err)
	}
	result.TreatmentResults = treatmentResults

	// Run baseline (no Cortex memory)
	if e.verbose {
		fmt.Println("\n=== Baseline Run (No memory) ===")
	}
	baselineResults, err := e.runJourneyRun(ctx, journey, false)
	if err != nil {
		return nil, fmt.Errorf("baseline run failed: %w", err)
	}
	result.BaselineResults = baselineResults

	// Calculate comparison
	result.Comparison = e.compareResults(treatmentResults, baselineResults)

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(startTime)

	// Determine overall pass/fail
	result.Pass, result.Reason = e.determineJourneyPass(result)

	if e.verbose {
		fmt.Printf("\nJourney completed in %v\n", result.Duration)
		fmt.Printf("  Pass: %v\n", result.Pass)
		if result.Reason != "" {
			fmt.Printf("  Reason: %s\n", result.Reason)
		}
	}

	return result, nil
}

// runJourneyRun executes all sessions for a single run (treatment or baseline).
func (e *JourneyEvaluator) runJourneyRun(ctx context.Context, journey *E2EJourney, cortexEnabled bool) (*E2ERunResults, error) {
	results := &E2ERunResults{
		SessionResults: make([]E2ESessionResult, 0, len(journey.Sessions)),
	}

	// Reset event storage for this run
	e.storedEvents = make(map[string]*events.Event)
	e.totalTokens = 0

	// Set up working directory
	workDir, err := os.MkdirTemp("", "cortex-e2e-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create work directory: %w", err)
	}
	defer os.RemoveAll(workDir)
	e.workDir = workDir

	// Copy scaffold project to work directory
	if err := e.copyScaffoldProject(journey.Project.Scaffold); err != nil {
		return nil, fmt.Errorf("failed to copy scaffold project: %w", err)
	}

	// Run each session sequentially
	for i, session := range journey.Sessions {
		if e.verbose {
			fmt.Printf("\nSession %d/%d: %s (phase: %s)\n",
				i+1, len(journey.Sessions), session.ID, session.Phase)
		}

		sessionResult, err := e.runSession(ctx, &session, cortexEnabled)
		if err != nil {
			return nil, fmt.Errorf("session %s failed: %w", session.ID, err)
		}

		results.SessionResults = append(results.SessionResults, *sessionResult)
		results.EventsStored += sessionResult.EventsStored

		// Aggregate task results
		if sessionResult.TaskResult != nil {
			results.TasksTotal++
			if sessionResult.TaskResult.CompletedSuccessfully {
				results.TasksCompleted++
			}
			results.TotalTurns += sessionResult.TaskResult.Turns
			results.TotalTokens += sessionResult.TaskResult.TokensUsed
			results.TestsTotal += sessionResult.TaskResult.TestsPassed + sessionResult.TaskResult.TestsFailed
			results.TestsPassed += sessionResult.TaskResult.TestsPassed
			results.PatternViolations += len(sessionResult.TaskResult.PatternViolations)
			results.CorrectionsNeeded += len(sessionResult.TaskResult.CorrectionsApplied)
		}
	}

	// Calculate aggregate rates
	if results.TasksTotal > 0 {
		results.TaskCompletionRate = float64(results.TasksCompleted) / float64(results.TasksTotal)
		results.AverageTurns = float64(results.TotalTurns) / float64(results.TasksTotal)
	}
	if results.TestsTotal > 0 {
		results.TestPassRate = float64(results.TestsPassed) / float64(results.TestsTotal)
	}

	return results, nil
}

// runSession processes a single session (store events, run task/queries).
func (e *JourneyEvaluator) runSession(ctx context.Context, session *E2ESession, cortexEnabled bool) (*E2ESessionResult, error) {
	result := &E2ESessionResult{
		SessionID: session.ID,
		Phase:     session.Phase,
	}

	// Store events (decisions, patterns, corrections)
	if cortexEnabled && len(session.Events) > 0 {
		for _, event := range session.Events {
			if err := e.storeEvent(ctx, &event); err != nil {
				return nil, fmt.Errorf("failed to store event %s: %w", event.ID, err)
			}
			result.EventsStored++
		}

		// For CLI mode, run ingest to process events into the database
		if cliCortex, ok := e.cortex.(*CLICortex); ok {
			if err := cliCortex.Ingest(); err != nil {
				return nil, fmt.Errorf("failed to ingest events: %w", err)
			}
		}

		if e.verbose {
			fmt.Printf("  Stored %d events\n", result.EventsStored)
		}
	}

	// Run task if present
	if session.Task != nil {
		taskResult, err := e.runTask(ctx, session.Task, cortexEnabled)
		if err != nil {
			return nil, fmt.Errorf("task failed: %w", err)
		}
		result.TaskResult = taskResult
	}

	// Run queries if present
	if len(session.Queries) > 0 {
		result.QueryResults = make([]E2EQueryResult, 0, len(session.Queries))
		for _, query := range session.Queries {
			queryResult, err := e.runQuery(ctx, &query, cortexEnabled)
			if err != nil {
				return nil, fmt.Errorf("query failed: %w", err)
			}
			result.QueryResults = append(result.QueryResults, *queryResult)
		}
	}

	return result, nil
}

// runTask has LLM implement a task and checks acceptance criteria.
func (e *JourneyEvaluator) runTask(ctx context.Context, task *E2ETask, cortexEnabled bool) (*E2ETaskResult, error) {
	startTime := time.Now()

	result := &E2ETaskResult{
		TaskDescription: task.Description,
		GeneratedCode:   make(map[string]string),
	}

	// Set timeout if specified
	timeout := 5 * time.Minute
	if task.Timeout != "" {
		if parsedTimeout, err := time.ParseDuration(task.Timeout); err == nil {
			timeout = parsedTimeout
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Prepare context from Cortex if enabled
	var contextStr string
	if cortexEnabled {
		results, err := e.queryContext(ctx, task.Description)
		if err == nil && len(results) > 0 {
			contextStr = e.formatContextForTask(results)
			if e.verbose {
				fmt.Printf("  Injected %d context items\n", len(results))
			}
		}
	}

	// Build prompt for LLM
	prompt := e.buildTaskPrompt(task, contextStr)

	// Execute task with LLM (up to MaxTurns iterations)
	maxTurns := task.MaxTurns
	if maxTurns == 0 {
		maxTurns = 15
	}

	for turn := 1; turn <= maxTurns; turn++ {
		result.Turns = turn

		if e.verbose {
			fmt.Printf("  Turn %d/%d\n", turn, maxTurns)
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			result.Completed = false
			result.FailureReason = "timeout"
			return result, nil
		default:
		}

		// Call LLM to generate code
		response, err := e.provider.Generate(ctx, prompt)
		if err != nil {
			result.FailureReason = fmt.Sprintf("LLM error: %v", err)
			return result, nil
		}

		if e.verbose && turn == 1 {
			fmt.Printf("  LLM Response (first 500 chars): %.500s\n", response)
		}

		// Track tokens (estimate based on response length)
		result.TokensUsed += len(prompt)/4 + len(response)/4

		// Apply generated code to files
		if err := e.applyGeneratedCode(task, response, result); err != nil {
			// Continue to next turn if application fails
			if e.verbose {
				fmt.Printf("  Code application failed: %v\n", err)
			}
			prompt = e.buildRetryPrompt(task, contextStr, err.Error())
			continue
		}

		// Check acceptance criteria
		acceptanceResult, err := e.checkAcceptance(ctx, task, result.GeneratedCode)
		if err != nil {
			result.FailureReason = fmt.Sprintf("acceptance check error: %v", err)
			return result, nil
		}

		// Record acceptance results
		result.TestResults = acceptanceResult.TestDetails
		result.TestsPassed = acceptanceResult.TestsPassed
		result.TestsFailed = acceptanceResult.TestsFailed
		result.BuildSucceeded = acceptanceResult.BuildsOK
		result.LintSucceeded = acceptanceResult.LintsOK
		result.PatternMatches = acceptanceResult.patternMatches
		result.PatternViolations = acceptanceResult.patternViolations
		result.CodeReviewResults = acceptanceResult.CodeReviewResults

		// Check if task is complete
		if e.isTaskComplete(acceptanceResult, task) {
			result.Completed = true
			result.CompletedSuccessfully = true
			break
		}

		if e.verbose {
			// Build code review summary
			codeReviewSummary := ""
			if len(acceptanceResult.CodeReviewResults) > 0 {
				passed := 0
				for _, r := range acceptanceResult.CodeReviewResults {
					if r.Passed {
						passed++
					}
				}
				codeReviewSummary = fmt.Sprintf(" code_review=%d/%d", passed, len(acceptanceResult.CodeReviewResults))
			}
			fmt.Printf("  Acceptance: build=%v lint=%v tests=%d/%d patterns=%d/%d violations=%d%s\n",
				acceptanceResult.BuildsOK, acceptanceResult.LintsOK,
				acceptanceResult.TestsPassed, acceptanceResult.TestsPassed+acceptanceResult.TestsFailed,
				len(acceptanceResult.PatternsFound), len(task.Acceptance.PatternsRequired),
				len(acceptanceResult.Violations), codeReviewSummary)
		}

		// Build retry prompt with feedback
		prompt = e.buildRetryPromptWithFeedback(task, contextStr, acceptanceResult)

		// Apply hints if available and we've failed multiple times
		if turn >= 3 && len(task.Hints) > 0 {
			hintIndex := (turn - 3) % len(task.Hints)
			prompt += fmt.Sprintf("\n\nHint: %s", task.Hints[hintIndex])
		}
	}

	result.Completed = true
	result.Duration = time.Since(startTime)

	if e.verbose {
		status := "incomplete"
		if result.CompletedSuccessfully {
			status = "success"
		}
		fmt.Printf("  Task %s after %d turns (%v)\n", status, result.Turns, result.Duration)
	}

	return result, nil
}

// checkAcceptance verifies task acceptance criteria.
func (e *JourneyEvaluator) checkAcceptance(ctx context.Context, task *E2ETask, generatedCode map[string]string) (*AcceptanceResult, error) {
	result := &AcceptanceResult{
		BuildsOK: true,
		LintsOK:  true,
	}

	// Get list of modified/created files
	var files []string
	files = append(files, task.FilesToModify...)
	files = append(files, task.FilesToCreate...)

	// Run build check if required
	buildMust := task.Acceptance.BuildsMust
	if buildMust == nil || *buildMust {
		if err := e.runBuild(); err != nil {
			result.BuildsOK = false
			result.buildError = err.Error()
		}
	}

	// Run lint check if required
	lintMust := task.Acceptance.LintsMust
	if lintMust == nil || *lintMust {
		lintErrors := e.runLint()
		if len(lintErrors) > 0 {
			result.LintsOK = false
			result.lintErrors = lintErrors
		}
	}

	// Run tests
	if len(task.Acceptance.TestsPass) > 0 {
		passed, failed, details := e.runTests(task.Acceptance.TestsPass)
		result.TestsPassed = passed
		result.TestsFailed = failed
		result.TestDetails = details
	} else if task.Acceptance.TestsFile != "" {
		passed, failed, details := e.runTestFile(task.Acceptance.TestsFile)
		result.TestsPassed = passed
		result.TestsFailed = failed
		result.TestDetails = details
	}

	// Check patterns
	if len(files) > 0 {
		found, missing := e.checkPatterns(files, task.Acceptance.PatternsRequired, nil)
		result.PatternsFound = found
		result.PatternsMissing = missing
		result.patternMatches = make([]PatternMatch, 0)
		for _, p := range task.Acceptance.PatternsRequired {
			match := PatternMatch{Pattern: p}
			for _, f := range found {
				if f == p {
					match.Found = true
					break
				}
			}
			result.patternMatches = append(result.patternMatches, match)
		}

		_, violations := e.checkPatterns(files, nil, task.Acceptance.PatternsForbidden)
		result.Violations = violations
		result.patternViolations = make([]PatternMatch, 0)
		for _, v := range violations {
			result.patternViolations = append(result.patternViolations, PatternMatch{
				Pattern: v,
				Found:   true,
			})
		}
	}

	// Run custom checks
	for _, check := range task.Acceptance.CustomChecks {
		if err := e.runCustomCheck(check); err != nil {
			// Custom check failed - record but don't stop
			result.customCheckErrors = append(result.customCheckErrors, err.Error())
		}
	}

	// Run LLM-as-judge code review if criteria are specified and judge is available
	if len(task.Acceptance.CodeReview) > 0 && e.judgeProvider != nil {
		judge := NewCodeReviewJudge(e.judgeProvider)
		reviewResults, err := judge.EvaluateCode(ctx, generatedCode, task.Acceptance.CodeReview)
		if err != nil {
			// Log error but don't fail the acceptance check
			if e.verbose {
				fmt.Printf("  Code review judge error: %v\n", err)
			}
		} else {
			result.CodeReviewResults = reviewResults

			// Calculate pass rate
			passed := 0
			for _, r := range reviewResults {
				if r.Passed {
					passed++
				}
			}
			result.CodeReviewPass = passed == len(reviewResults)

			// Print verbose output for code review
			if e.verbose {
				fmt.Println("  Code Review Results:")
				for _, r := range reviewResults {
					status := "X"
					if r.Passed {
						status = "OK"
					}
					fmt.Printf("    [%s] %s (confidence: %.2f)\n", status, r.Criterion, r.Confidence)
					if !r.Passed && r.Reasoning != "" {
						fmt.Printf("        Reason: %s\n", r.Reasoning)
					}
				}
			}
		}
	}

	return result, nil
}

// AcceptanceResult contains the result of checking task acceptance criteria.
type AcceptanceResult struct {
	TestsPassed       int
	TestsFailed       int
	TestDetails       []TestResult
	PatternsFound     []string           // Required patterns found
	PatternsMissing   []string           // Required patterns NOT found
	Violations        []string           // Forbidden patterns found
	CodeReviewPass    bool               // LLM-as-judge result (optional)
	CodeReviewResults []CodeReviewResult // Detailed results per criterion
	BuildsOK          bool
	LintsOK           bool

	// Internal fields for result aggregation
	patternMatches    []PatternMatch
	patternViolations []PatternMatch
	buildError        string
	lintErrors        []string
	customCheckErrors []string
}

// runTests runs Go tests and returns pass/fail status.
func (e *JourneyEvaluator) runTests(testNames []string) (passed int, failed int, details []TestResult) {
	for _, testName := range testNames {
		result := TestResult{Name: testName}
		start := time.Now()

		// Run test
		cmd := exec.Command("go", "test", "-v", "-run", testName, "./...")
		cmd.Dir = e.workDir

		output, err := cmd.CombinedOutput()
		result.Duration = time.Since(start)
		result.Output = string(output)

		if err == nil {
			result.Passed = true
			passed++
		} else {
			result.Passed = false
			failed++
		}

		details = append(details, result)
	}

	return passed, failed, details
}

// runTestFile runs all tests in a specific test file.
func (e *JourneyEvaluator) runTestFile(testFile string) (passed int, failed int, details []TestResult) {
	testPath := filepath.Join(e.workDir, testFile)

	cmd := exec.Command("go", "test", "-v", testPath)
	cmd.Dir = e.workDir

	output, err := cmd.CombinedOutput()

	// Parse test output to count passes/failures
	passed, failed = parseTestOutput(string(output))

	result := TestResult{
		Name:   testFile,
		Output: string(output),
		Passed: err == nil,
	}
	details = append(details, result)

	return passed, failed, details
}

// checkPatterns greps for required/forbidden patterns in modified files.
func (e *JourneyEvaluator) checkPatterns(files []string, required, forbidden []string) (found []string, violations []string) {
	for _, file := range files {
		filePath := filepath.Join(e.workDir, file)
		content, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		contentStr := string(content)

		// Check required patterns
		for _, pattern := range required {
			if strings.Contains(contentStr, pattern) {
				// Add to found if not already present
				if !containsString(found, pattern) {
					found = append(found, pattern)
				}
			}
		}

		// Check forbidden patterns
		for _, pattern := range forbidden {
			if strings.Contains(contentStr, pattern) {
				// Add to violations if not already present
				if !containsString(violations, pattern) {
					violations = append(violations, pattern)
				}
			}
		}
	}

	return found, violations
}

// storeEvent stores a decision/pattern/correction in Cortex.
func (e *JourneyEvaluator) storeEvent(ctx context.Context, event *E2EEvent) error {
	// Create a storage event from the E2E event
	storageEvent := &events.Event{
		ID:        event.ID,
		Source:    events.SourceGeneric,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "cortex-eval",
		ToolResult: formatEventContent(event),
		Context: events.EventContext{
			SessionID: "e2e-eval",
		},
		Metadata: map[string]interface{}{
			"type":       string(event.Type),
			"tags":       event.Tags,
			"importance": event.Importance,
			"supersedes": event.Supersedes,
			"scope":      event.Scope,
		},
	}

	// Store in our local tracking map
	e.storedEvents[event.ID] = storageEvent

	// Integrate with Cortex for retrieval
	// Try CLICortex first (preferred for E2E testing)
	if cliCortex, ok := e.cortex.(*CLICortex); ok {
		if err := cliCortex.StoreEvent(string(event.Type), formatEventContent(event), event.Tags); err != nil {
			return fmt.Errorf("failed to store event via CLI: %w", err)
		}
		return nil
	}

	// Fallback to MockCortex (for dry-run mode)
	if mockCortex, ok := e.cortex.(*MockCortex); ok {
		mockCortex.AddEventToCorpus(
			event.ID,
			formatEventContent(event),
			string(event.Type),
			event.Tags,
			event.Importance,
		)
	}

	return nil
}

// queryContext retrieves context from Cortex for a query.
func (e *JourneyEvaluator) queryContext(ctx context.Context, queryText string) ([]cognition.Result, error) {
	query := cognition.Query{
		Text:  queryText,
		Limit: 10,
	}

	// Use Fast mode for mid-session retrieval
	result, err := e.cortex.Retrieve(ctx, query, cognition.Fast)
	if err != nil {
		return nil, err
	}

	return result.Results, nil
}

// runQuery executes a recall query and checks expectations.
func (e *JourneyEvaluator) runQuery(ctx context.Context, query *E2EQuery, cortexEnabled bool) (*E2EQueryResult, error) {
	result := &E2EQueryResult{
		QueryID:   query.ID,
		QueryText: query.Text,
	}

	if !cortexEnabled {
		// No context available in baseline
		result.Pass = false
		result.RecallScore = 0
		return result, nil
	}

	// Query Cortex
	results, err := e.queryContext(ctx, query.Text)
	if err != nil {
		return nil, err
	}

	// Extract recalled event IDs
	for _, r := range results {
		result.RecalledEventIDs = append(result.RecalledEventIDs, r.ID)
	}

	// Check expected recalls
	for _, expectedID := range query.ExpectedRecall {
		if containsString(result.RecalledEventIDs, expectedID) {
			result.ExpectedRecallHits++
		} else {
			result.ExpectedRecallMisses++
		}
	}

	// Check unexpected recalls
	for _, notExpectedID := range query.ExpectedNotRecall {
		if containsString(result.RecalledEventIDs, notExpectedID) {
			result.UnexpectedRecalls = append(result.UnexpectedRecalls, notExpectedID)
		}
	}

	// Calculate metrics
	if len(query.ExpectedRecall) > 0 {
		result.RecallScore = float64(result.ExpectedRecallHits) / float64(len(query.ExpectedRecall))
	}
	if result.ExpectedRecallHits+len(result.UnexpectedRecalls) > 0 {
		result.RecallPrecision = float64(result.ExpectedRecallHits) /
			float64(result.ExpectedRecallHits+len(result.UnexpectedRecalls))
	}

	// Determine pass/fail
	result.Pass = result.RecallScore >= 0.5 && len(result.UnexpectedRecalls) == 0

	return result, nil
}

// Helper methods

func (e *JourneyEvaluator) copyScaffoldProject(scaffoldPath string) error {
	// Resolve scaffold path relative to project dir if needed
	if !filepath.IsAbs(scaffoldPath) {
		scaffoldPath = filepath.Join(e.projectDir, scaffoldPath)
	}

	// Walk the scaffold and copy all files
	return filepath.Walk(scaffoldPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate relative path and target path
		relPath, err := filepath.Rel(scaffoldPath, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(e.workDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		// Copy file
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		dst, err := os.Create(targetPath)
		if err != nil {
			return err
		}
		defer dst.Close()

		_, err = io.Copy(dst, src)
		return err
	})
}

func (e *JourneyEvaluator) formatContextForTask(results []cognition.Result) string {
	var sb strings.Builder
	sb.WriteString("Relevant context from previous sessions:\n\n")

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("- [%s] %s\n", r.Category, r.Content))
	}

	return sb.String()
}

func (e *JourneyEvaluator) buildTaskPrompt(task *E2ETask, contextStr string) string {
	var sb strings.Builder

	sb.WriteString("You are implementing a feature in a Go codebase.\n\n")

	if contextStr != "" {
		sb.WriteString(contextStr)
		sb.WriteString("\n")
	}

	sb.WriteString("## Task\n")
	sb.WriteString(task.Description)
	sb.WriteString("\n\n")

	// Include current file contents for files to modify
	if len(task.FilesToModify) > 0 {
		sb.WriteString("## Current File Contents\n\n")
		for _, f := range task.FilesToModify {
			filePath := filepath.Join(e.workDir, f)
			content, err := os.ReadFile(filePath)
			if err == nil {
				sb.WriteString(fmt.Sprintf("### %s\n```go\n%s\n```\n\n", f, string(content)))
			} else {
				sb.WriteString(fmt.Sprintf("### %s\n(file not found)\n\n", f))
			}
		}
	}

	if len(task.FilesToCreate) > 0 {
		sb.WriteString("## Files to create\n")
		for _, f := range task.FilesToCreate {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Instructions\n")
	sb.WriteString("Provide the COMPLETE modified file with your implementation.\n")
	sb.WriteString("You MUST use this EXACT format:\n\n")
	sb.WriteString("```go\n// FILE: path/to/file.go\n<complete file contents here>\n```\n\n")
	sb.WriteString("Important: Include the entire file, not just the changed parts.\n")

	return sb.String()
}

func (e *JourneyEvaluator) buildRetryPrompt(task *E2ETask, contextStr, errorMsg string) string {
	return e.buildTaskPrompt(task, contextStr) +
		fmt.Sprintf("\n\n## Previous attempt failed\nError: %s\n\nPlease fix the issue and try again.", errorMsg)
}

func (e *JourneyEvaluator) buildRetryPromptWithFeedback(task *E2ETask, contextStr string, acceptance *AcceptanceResult) string {
	var sb strings.Builder
	sb.WriteString(e.buildTaskPrompt(task, contextStr))
	sb.WriteString("\n\n## Previous attempt feedback\n")

	if !acceptance.BuildsOK {
		sb.WriteString(fmt.Sprintf("Build failed: %s\n", acceptance.buildError))
	}

	if !acceptance.LintsOK {
		sb.WriteString(fmt.Sprintf("Lint errors: %v\n", acceptance.lintErrors))
	}

	if acceptance.TestsFailed > 0 {
		sb.WriteString(fmt.Sprintf("Tests: %d passed, %d failed\n", acceptance.TestsPassed, acceptance.TestsFailed))
		for _, t := range acceptance.TestDetails {
			if !t.Passed {
				sb.WriteString(fmt.Sprintf("  - %s: FAILED\n", t.Name))
			}
		}
	}

	if len(acceptance.PatternsMissing) > 0 {
		sb.WriteString(fmt.Sprintf("Missing required patterns: %v\n", acceptance.PatternsMissing))
	}

	if len(acceptance.Violations) > 0 {
		sb.WriteString(fmt.Sprintf("Forbidden patterns found: %v\n", acceptance.Violations))
	}

	// Add code review feedback if available
	if len(acceptance.CodeReviewResults) > 0 {
		hasFailures := false
		for _, r := range acceptance.CodeReviewResults {
			if !r.Passed {
				hasFailures = true
				break
			}
		}
		if hasFailures {
			sb.WriteString("\nCode Review Feedback:\n")
			for _, r := range acceptance.CodeReviewResults {
				if !r.Passed {
					sb.WriteString(fmt.Sprintf("  - FAILED: %s\n", r.Criterion))
					if r.Reasoning != "" {
						sb.WriteString(fmt.Sprintf("    Reason: %s\n", r.Reasoning))
					}
				}
			}
		}
	}

	sb.WriteString("\nPlease fix these issues and try again.")
	return sb.String()
}

func (e *JourneyEvaluator) applyGeneratedCode(task *E2ETask, response string, result *E2ETaskResult) error {
	// Parse response for file blocks
	files := parseFileBlocks(response)

	// If no files with explicit paths, try to extract any code block and apply to first file to modify
	if len(files) == 0 {
		// Try to extract a code block and apply it to the first file in FilesToModify
		content := extractFirstCodeBlock(response)
		if content != "" && len(task.FilesToModify) > 0 {
			files[task.FilesToModify[0]] = content
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("no file blocks found in response")
	}

	for path, content := range files {
		targetPath := filepath.Join(e.workDir, path)

		// Create parent directories if needed
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", path, err)
		}

		// Write file
		if err := os.WriteFile(targetPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", path, err)
		}

		result.GeneratedCode[path] = content
	}

	return nil
}

// extractFirstCodeBlock extracts the content of the first code block in the response.
func extractFirstCodeBlock(response string) string {
	lines := strings.Split(response, "\n")
	var content strings.Builder
	inBlock := false

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inBlock {
				// End of first block
				return strings.TrimSpace(content.String())
			}
			inBlock = true
			continue
		}
		if inBlock {
			content.WriteString(line)
			content.WriteString("\n")
		}
	}
	return ""
}

func (e *JourneyEvaluator) isTaskComplete(acceptance *AcceptanceResult, task *E2ETask) bool {
	// Build must pass (if required)
	buildMust := task.Acceptance.BuildsMust
	if buildMust == nil || *buildMust {
		if !acceptance.BuildsOK {
			return false
		}
	}

	// Lint must pass (if required)
	lintMust := task.Acceptance.LintsMust
	if lintMust == nil || *lintMust {
		if !acceptance.LintsOK {
			return false
		}
	}

	// All specified tests must pass
	if len(task.Acceptance.TestsPass) > 0 && acceptance.TestsFailed > 0 {
		return false
	}

	// All required patterns must be found
	if len(task.Acceptance.PatternsRequired) > 0 && len(acceptance.PatternsMissing) > 0 {
		return false
	}

	// No forbidden patterns should be found
	if len(acceptance.Violations) > 0 {
		return false
	}

	// Code review must pass if criteria are specified and results are available
	if len(task.Acceptance.CodeReview) > 0 && len(acceptance.CodeReviewResults) > 0 {
		if !acceptance.CodeReviewPass {
			return false
		}
	}

	return true
}

func (e *JourneyEvaluator) runBuild() error {
	// First run go mod tidy to resolve any new dependencies the LLM added
	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = e.workDir
	tidyCmd.CombinedOutput() // Ignore errors - tidy may fail but build might still work

	// Then run go build
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = e.workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %s", string(output))
	}
	return nil
}

func (e *JourneyEvaluator) runLint() []string {
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = e.workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Parse output for individual errors
		var errors []string
		scanner := bufio.NewScanner(bytes.NewReader(output))
		for scanner.Scan() {
			errors = append(errors, scanner.Text())
		}
		return errors
	}
	return nil
}

func (e *JourneyEvaluator) runCustomCheck(check string) error {
	// Split check into command and args
	parts := strings.Fields(check)
	if len(parts) == 0 {
		return fmt.Errorf("empty check command")
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = e.workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check failed: %s - %s", check, string(output))
	}
	return nil
}

func (e *JourneyEvaluator) compareResults(treatment, baseline *E2ERunResults) *E2EComparison {
	comp := &E2EComparison{}

	// Task completion lift
	comp.TaskCompletionLift = treatment.TaskCompletionRate - baseline.TaskCompletionRate

	// Test pass lift
	comp.TestPassLift = treatment.TestPassRate - baseline.TestPassRate

	// Turn reduction
	if baseline.TotalTurns > 0 {
		comp.TurnReduction = float64(baseline.TotalTurns-treatment.TotalTurns) / float64(baseline.TotalTurns)
	}

	// Token reduction
	if baseline.TotalTokens > 0 {
		comp.TokenReduction = float64(baseline.TotalTokens-treatment.TotalTokens) / float64(baseline.TotalTokens)
	}

	// Violation reduction
	comp.ViolationReduction = baseline.PatternViolations - treatment.PatternViolations

	// Correction reduction
	comp.CorrectionReduction = baseline.CorrectionsNeeded - treatment.CorrectionsNeeded

	// Overall lift (weighted average)
	comp.OverallLift = (comp.TaskCompletionLift*0.3 +
		comp.TestPassLift*0.3 +
		comp.TurnReduction*0.2 +
		float64(comp.ViolationReduction)*0.1 +
		float64(comp.CorrectionReduction)*0.1)

	// Check for regression
	if comp.TaskCompletionLift < 0 || comp.TestPassLift < 0 {
		comp.Regression = true
		comp.RegressionDetails = "Treatment performed worse than baseline"
	}

	return comp
}

func (e *JourneyEvaluator) determineJourneyPass(result *E2EJourneyResult) (bool, string) {
	// Check treatment results
	if result.TreatmentResults.TaskCompletionRate < 0.5 {
		return false, fmt.Sprintf("task completion rate too low: %.2f", result.TreatmentResults.TaskCompletionRate)
	}

	// Check for regression
	if result.Comparison.Regression {
		return false, result.Comparison.RegressionDetails
	}

	// Check overall lift is positive
	if result.Comparison.OverallLift < 0 {
		return false, fmt.Sprintf("negative overall lift: %.2f", result.Comparison.OverallLift)
	}

	return true, ""
}

// Utility functions

func generateRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UnixNano())
}

func formatEventContent(event *E2EEvent) string {
	var sb strings.Builder
	sb.WriteString(event.Content)
	if event.Rationale != "" {
		sb.WriteString(fmt.Sprintf(" (Rationale: %s)", event.Rationale))
	}
	return sb.String()
}

func parseFileBlocks(response string) map[string]string {
	files := make(map[string]string)

	// Look for blocks like:
	// ```go
	// // FILE: path/to/file.go
	// <code>
	// ```
	// Also supports: ```go:path/to/file.go
	lines := strings.Split(response, "\n")
	var currentFile string
	var currentContent strings.Builder
	inBlock := false

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inBlock {
				// End of block
				if currentFile != "" {
					files[currentFile] = strings.TrimSpace(currentContent.String())
				}
				currentFile = ""
				currentContent.Reset()
				inBlock = false
			} else {
				// Start of block - check for filename in block opener
				// e.g., ```go:greeter.go or ```go greeter.go
				inBlock = true
				rest := strings.TrimPrefix(line, "```")
				// Try format: ```go:filename.go or ```go filename.go
				if idx := strings.Index(rest, ":"); idx > 0 {
					currentFile = strings.TrimSpace(rest[idx+1:])
				} else if parts := strings.Fields(rest); len(parts) >= 2 {
					currentFile = parts[1]
				}
			}
			continue
		}

		if inBlock {
			if strings.HasPrefix(line, "// FILE:") {
				currentFile = strings.TrimSpace(strings.TrimPrefix(line, "// FILE:"))
			} else if currentFile != "" {
				currentContent.WriteString(line)
				currentContent.WriteString("\n")
			} else if currentFile == "" {
				// No file specified yet, accumulate content anyway
				// We'll assign to default file later
				currentContent.WriteString(line)
				currentContent.WriteString("\n")
			}
		}
	}

	return files
}

func parseTestOutput(output string) (passed, failed int) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "--- PASS:") {
			passed++
		} else if strings.HasPrefix(line, "--- FAIL:") {
			failed++
		}
	}
	return passed, failed
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
