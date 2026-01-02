package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition"
)

func TestNewJourneyEvaluator(t *testing.T) {
	mockCortex := NewMockCortex()
	mockProvider := &MockProvider{}

	evaluator := NewJourneyEvaluator(mockCortex, mockProvider, "/tmp/project", true)

	if evaluator == nil {
		t.Fatal("NewJourneyEvaluator returned nil")
	}
	if evaluator.cortex != mockCortex {
		t.Error("Cortex not set correctly")
	}
	if evaluator.provider != mockProvider {
		t.Error("Provider not set correctly")
	}
	if evaluator.projectDir != "/tmp/project" {
		t.Errorf("Expected projectDir '/tmp/project', got '%s'", evaluator.projectDir)
	}
	if !evaluator.verbose {
		t.Error("Expected verbose to be true")
	}
}

func TestJourneyEvaluator_LoadJourneyAndSetup(t *testing.T) {
	// Create a minimal journey file
	journeyContent := `
id: test-setup-journey
type: e2e
name: "Test Setup Journey"

project:
  name: "test-api"
  scaffold: "scaffold"

sessions:
  - id: session-01
    phase: foundation
    events:
      - type: decision
        id: use-redis
        content: "Use Redis for caching"
        tags: [cache, redis]

  - id: session-02
    phase: feature
    task:
      description: "Implement caching"
      files_to_modify:
        - "main.go"
      acceptance:
        patterns_required:
          - "redis"
`

	// Create temporary directory structure
	tmpDir := t.TempDir()
	journeyPath := filepath.Join(tmpDir, "journey.yaml")
	if err := os.WriteFile(journeyPath, []byte(journeyContent), 0644); err != nil {
		t.Fatalf("Failed to write journey file: %v", err)
	}

	// Create scaffold directory with a simple Go file
	scaffoldDir := filepath.Join(tmpDir, "scaffold")
	if err := os.MkdirAll(scaffoldDir, 0755); err != nil {
		t.Fatalf("Failed to create scaffold dir: %v", err)
	}

	goModContent := `module test-api

go 1.21
`
	if err := os.WriteFile(filepath.Join(scaffoldDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatalf("Failed to write go.mod: %v", err)
	}

	mainGoContent := `package main

func main() {
	// TODO: implement
}
`
	if err := os.WriteFile(filepath.Join(scaffoldDir, "main.go"), []byte(mainGoContent), 0644); err != nil {
		t.Fatalf("Failed to write main.go: %v", err)
	}

	// Load journey
	journey, err := LoadE2EJourney(journeyPath)
	if err != nil {
		t.Fatalf("Failed to load journey: %v", err)
	}

	// Verify journey loaded correctly
	if journey.ID != "test-setup-journey" {
		t.Errorf("Expected journey ID 'test-setup-journey', got '%s'", journey.ID)
	}
	if len(journey.Sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(journey.Sessions))
	}
	if journey.TotalEvents() != 1 {
		t.Errorf("Expected 1 event, got %d", journey.TotalEvents())
	}
	if journey.TotalTasks() != 1 {
		t.Errorf("Expected 1 task, got %d", journey.TotalTasks())
	}

	// Create evaluator
	mockCortex := NewMockCortex()
	mockProvider := &MockProvider{}
	evaluator := NewJourneyEvaluator(mockCortex, mockProvider, tmpDir, false)

	// Test copyScaffoldProject
	workDir := t.TempDir()
	evaluator.workDir = workDir

	err = evaluator.copyScaffoldProject(scaffoldDir)
	if err != nil {
		t.Fatalf("copyScaffoldProject failed: %v", err)
	}

	// Verify files were copied
	if _, err := os.Stat(filepath.Join(workDir, "go.mod")); os.IsNotExist(err) {
		t.Error("go.mod was not copied")
	}
	if _, err := os.Stat(filepath.Join(workDir, "main.go")); os.IsNotExist(err) {
		t.Error("main.go was not copied")
	}
}

func TestJourneyEvaluator_StoreEvent(t *testing.T) {
	mockCortex := NewMockCortex()
	mockProvider := &MockProvider{}
	evaluator := NewJourneyEvaluator(mockCortex, mockProvider, "/tmp", false)

	event := &E2EEvent{
		Type:       EventDecision,
		ID:         "test-decision",
		Content:    "Use Redis for caching",
		Rationale:  "Shared across instances",
		Tags:       []string{"cache", "redis"},
		Importance: 8,
	}

	ctx := context.Background()
	err := evaluator.storeEvent(ctx, event)
	if err != nil {
		t.Fatalf("storeEvent failed: %v", err)
	}

	// Verify event was stored
	if _, exists := evaluator.storedEvents["test-decision"]; !exists {
		t.Error("Event was not stored")
	}
}

func TestJourneyEvaluator_StoreEventAndQueryIntegration(t *testing.T) {
	// This test verifies that events stored via storeEvent() can be retrieved
	// via queryContext() when using MockCortex (dry-run mode).

	mockCortex := NewMockCortex()
	mockProvider := &MockProvider{}
	evaluator := NewJourneyEvaluator(mockCortex, mockProvider, "/tmp", false)

	ctx := context.Background()

	// Store an event about Redis caching
	event := &E2EEvent{
		Type:       EventDecision,
		ID:         "redis-caching-decision",
		Content:    "Use Redis for distributed caching instead of local memory",
		Rationale:  "Allows horizontal scaling",
		Tags:       []string{"cache", "redis", "architecture"},
		Importance: 8,
	}

	err := evaluator.storeEvent(ctx, event)
	if err != nil {
		t.Fatalf("storeEvent failed: %v", err)
	}

	// Verify event was added to local tracking map
	if _, exists := evaluator.storedEvents["redis-caching-decision"]; !exists {
		t.Fatal("Event was not stored in storedEvents map")
	}

	// Verify event was added to MockCortex corpus
	if _, exists := mockCortex.Corpus["redis-caching-decision"]; !exists {
		t.Fatal("Event was not added to MockCortex corpus")
	}

	// Query for the event using a related query
	results, err := evaluator.queryContext(ctx, "redis caching")
	if err != nil {
		t.Fatalf("queryContext failed: %v", err)
	}

	// Verify the stored event is in the results
	found := false
	for _, r := range results {
		if r.ID == "redis-caching-decision" {
			found = true
			// Verify content matches
			if !stringContains(r.Content, "Redis for distributed caching") {
				t.Errorf("Expected content to contain 'Redis for distributed caching', got: %s", r.Content)
			}
			// Verify category is set correctly
			if r.Category != "decision" {
				t.Errorf("Expected category 'decision', got: %s", r.Category)
			}
			// Verify tags are set
			if len(r.Tags) == 0 {
				t.Error("Expected tags to be set")
			}
			break
		}
	}

	if !found {
		t.Errorf("Expected to find 'redis-caching-decision' in query results, got: %v", results)
	}
}

func TestJourneyEvaluator_StoreMultipleEventsAndQuery(t *testing.T) {
	// Test storing multiple events and querying them

	mockCortex := NewMockCortex()
	mockProvider := &MockProvider{}
	evaluator := NewJourneyEvaluator(mockCortex, mockProvider, "/tmp", false)

	ctx := context.Background()

	// Store multiple events
	events := []*E2EEvent{
		{
			Type:       EventDecision,
			ID:         "jwt-auth-decision",
			Content:    "Use JWT tokens for API authentication",
			Tags:       []string{"auth", "jwt", "security"},
			Importance: 9,
		},
		{
			Type:       EventPattern,
			ID:         "error-handling-pattern",
			Content:    "Always wrap errors with context using fmt.Errorf",
			Tags:       []string{"errors", "patterns"},
			Importance: 7,
		},
		{
			Type:       EventCorrection,
			ID:         "no-global-state",
			Content:    "Do not use global variables for state, use dependency injection",
			Tags:       []string{"architecture", "state"},
			Importance: 8,
		},
	}

	for _, event := range events {
		err := evaluator.storeEvent(ctx, event)
		if err != nil {
			t.Fatalf("storeEvent failed for %s: %v", event.ID, err)
		}
	}

	// Verify all events are in corpus
	if len(mockCortex.Corpus) != 3 {
		t.Errorf("Expected 3 items in corpus, got %d", len(mockCortex.Corpus))
	}

	// Query for authentication-related context
	results, err := evaluator.queryContext(ctx, "authentication JWT tokens")
	if err != nil {
		t.Fatalf("queryContext failed: %v", err)
	}

	// Should find the JWT auth decision
	foundJWT := false
	for _, r := range results {
		if r.ID == "jwt-auth-decision" {
			foundJWT = true
			break
		}
	}

	if !foundJWT {
		t.Error("Expected to find 'jwt-auth-decision' in query results for 'authentication JWT tokens'")
	}

	// Query for error handling
	results, err = evaluator.queryContext(ctx, "error handling wrap context")
	if err != nil {
		t.Fatalf("queryContext failed: %v", err)
	}

	foundError := false
	for _, r := range results {
		if r.ID == "error-handling-pattern" {
			foundError = true
			break
		}
	}

	if !foundError {
		t.Error("Expected to find 'error-handling-pattern' in query results for 'error handling'")
	}
}

func TestJourneyEvaluator_CheckPatterns(t *testing.T) {
	evaluator := &JourneyEvaluator{}
	evaluator.workDir = t.TempDir()

	// Create test file
	testFile := "test.go"
	testContent := `package main

import "github.com/redis/go-redis/v9"

func main() {
	client := redis.NewClient(&redis.Options{})
	_ = client
}
`
	testPath := filepath.Join(evaluator.workDir, testFile)
	if err := os.WriteFile(testPath, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test required patterns
	required := []string{"redis.NewClient", "redis.Options"}
	found, _ := evaluator.checkPatterns([]string{testFile}, required, nil)

	if len(found) != 2 {
		t.Errorf("Expected 2 patterns found, got %d", len(found))
	}
	if !containsString(found, "redis.NewClient") {
		t.Error("Expected to find 'redis.NewClient'")
	}

	// Test forbidden patterns
	forbidden := []string{"sync.Map", "map[string]"}
	_, violations := evaluator.checkPatterns([]string{testFile}, nil, forbidden)

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations, got %d", len(violations))
	}

	// Add forbidden pattern to file
	badContent := testContent + "\nvar cache = make(map[string]string)\n"
	if err := os.WriteFile(testPath, []byte(badContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, violations = evaluator.checkPatterns([]string{testFile}, nil, forbidden)
	if len(violations) != 1 {
		t.Errorf("Expected 1 violation, got %d", len(violations))
	}
	if !containsString(violations, "map[string]") {
		t.Error("Expected violation for 'map[string]'")
	}
}

func TestJourneyEvaluator_ParseFileBlocks(t *testing.T) {
	tests := []struct {
		name     string
		response string
		expected map[string]string
	}{
		{
			name: "single file block",
			response: "Here's the implementation:\n\n```go\n// FILE: main.go\npackage main\n\nfunc main() {}\n```",
			expected: map[string]string{
				"main.go": "package main\n\nfunc main() {}",
			},
		},
		{
			name: "multiple file blocks",
			response: "```go\n// FILE: service.go\npackage service\n\ntype Service struct{}\n```\n\n```go\n// FILE: handler.go\npackage handler\n\nfunc Handle() {}\n```",
			expected: map[string]string{
				"service.go": "package service\n\ntype Service struct{}",
				"handler.go": "package handler\n\nfunc Handle() {}",
			},
		},
		{
			name:     "no file blocks",
			response: "Just some explanation without code.",
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseFileBlocks(tt.response)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d files, got %d", len(tt.expected), len(result))
			}

			for path, expectedContent := range tt.expected {
				if content, ok := result[path]; !ok {
					t.Errorf("Expected file '%s' not found", path)
				} else if content != expectedContent {
					t.Errorf("Content mismatch for %s:\nexpected:\n%s\ngot:\n%s", path, expectedContent, content)
				}
			}
		})
	}
}

func TestJourneyEvaluator_ParseTestOutput(t *testing.T) {
	tests := []struct {
		name           string
		output         string
		expectedPassed int
		expectedFailed int
	}{
		{
			name:           "all pass",
			output:         "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- PASS: TestB (0.00s)\nPASS",
			expectedPassed: 2,
			expectedFailed: 0,
		},
		{
			name:           "mixed results",
			output:         "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\nFAIL",
			expectedPassed: 1,
			expectedFailed: 1,
		},
		{
			name:           "all fail",
			output:         "=== RUN   TestA\n--- FAIL: TestA (0.00s)\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\nFAIL",
			expectedPassed: 0,
			expectedFailed: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed, failed := parseTestOutput(tt.output)
			if passed != tt.expectedPassed {
				t.Errorf("Expected %d passed, got %d", tt.expectedPassed, passed)
			}
			if failed != tt.expectedFailed {
				t.Errorf("Expected %d failed, got %d", tt.expectedFailed, failed)
			}
		})
	}
}

func TestJourneyEvaluator_FormatContextForTask(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	results := []cognition.Result{
		{Category: "decision", Content: "Use Redis for caching"},
		{Category: "pattern", Content: "Always wrap errors with context"},
	}

	formatted := evaluator.formatContextForTask(results)

	if formatted == "" {
		t.Error("Expected non-empty formatted context")
	}
	// Formatter uses markdown headers for categories
	if !stringContains(formatted, "Decision") {
		t.Error("Expected Decision category in formatted output")
	}
	if !stringContains(formatted, "Redis") {
		t.Error("Expected content in formatted output")
	}
}

func TestJourneyEvaluator_CompareResults(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	treatment := &E2ERunResults{
		TasksTotal:         2,
		TasksCompleted:     2,
		TaskCompletionRate: 1.0,
		TestsTotal:         10,
		TestsPassed:        10,
		TestPassRate:       1.0,
		TotalTurns:         5,
		TotalTokens:        1000,
		PatternViolations:  0,
		CorrectionsNeeded:  0,
	}

	baseline := &E2ERunResults{
		TasksTotal:         2,
		TasksCompleted:     1,
		TaskCompletionRate: 0.5,
		TestsTotal:         10,
		TestsPassed:        7,
		TestPassRate:       0.7,
		TotalTurns:         10,
		TotalTokens:        2000,
		PatternViolations:  2,
		CorrectionsNeeded:  3,
	}

	comparison := evaluator.compareResults(treatment, baseline)

	// Task completion lift: 1.0 - 0.5 = 0.5
	if comparison.TaskCompletionLift != 0.5 {
		t.Errorf("Expected TaskCompletionLift 0.5, got %f", comparison.TaskCompletionLift)
	}

	// Test pass lift: 1.0 - 0.7 = 0.3
	if !floatEquals(comparison.TestPassLift, 0.3, 0.001) {
		t.Errorf("Expected TestPassLift ~0.3, got %f", comparison.TestPassLift)
	}

	// Turn reduction: (10-5)/10 = 0.5
	if comparison.TurnReduction != 0.5 {
		t.Errorf("Expected TurnReduction 0.5, got %f", comparison.TurnReduction)
	}

	// Token reduction: (2000-1000)/2000 = 0.5
	if comparison.TokenReduction != 0.5 {
		t.Errorf("Expected TokenReduction 0.5, got %f", comparison.TokenReduction)
	}

	// Violation reduction: 2 - 0 = 2
	if comparison.ViolationReduction != 2 {
		t.Errorf("Expected ViolationReduction 2, got %d", comparison.ViolationReduction)
	}

	// No regression expected
	if comparison.Regression {
		t.Error("Did not expect regression")
	}

	// Overall lift should be positive
	if comparison.OverallLift <= 0 {
		t.Errorf("Expected positive OverallLift, got %f", comparison.OverallLift)
	}
}

func TestJourneyEvaluator_CompareResults_Regression(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	treatment := &E2ERunResults{
		TaskCompletionRate: 0.3,  // Worse than baseline
		TestPassRate:       0.5,
	}

	baseline := &E2ERunResults{
		TaskCompletionRate: 0.8,  // Better than treatment
		TestPassRate:       0.6,
	}

	comparison := evaluator.compareResults(treatment, baseline)

	// Should detect regression
	if !comparison.Regression {
		t.Error("Expected regression to be detected")
	}

	// Task completion lift should be negative
	if comparison.TaskCompletionLift >= 0 {
		t.Errorf("Expected negative TaskCompletionLift, got %f", comparison.TaskCompletionLift)
	}
}

func TestJourneyEvaluator_BuildTaskPrompt(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	task := &E2ETask{
		Description:   "Implement user authentication",
		FilesToModify: []string{"auth/handler.go", "auth/middleware.go"},
		FilesToCreate: []string{"auth/jwt.go"},
	}

	contextStr := "Use JWT tokens for authentication."

	prompt := evaluator.buildTaskPrompt(task, contextStr)

	// Verify prompt contains key elements
	if !stringContains(prompt, "Implement user authentication") {
		t.Error("Expected task description in prompt")
	}
	if !stringContains(prompt, "JWT tokens") {
		t.Error("Expected context in prompt")
	}
	if !stringContains(prompt, "auth/handler.go") {
		t.Error("Expected files to modify in prompt")
	}
	if !stringContains(prompt, "auth/jwt.go") {
		t.Error("Expected files to create in prompt")
	}
	if !stringContains(prompt, "// FILE:") {
		t.Error("Expected file format instructions in prompt")
	}
}

func TestJourneyEvaluator_IsTaskComplete(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	tests := []struct {
		name       string
		acceptance *AcceptanceResult
		task       *E2ETask
		expected   bool
	}{
		{
			name: "all criteria met",
			acceptance: &AcceptanceResult{
				BuildsOK:        true,
				LintsOK:         true,
				TestsPassed:     2,
				TestsFailed:     0,
				PatternsFound:   []string{"pattern1"},
				PatternsMissing: []string{},
				Violations:      []string{},
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{
					TestsPass:         []string{"Test1", "Test2"},
					PatternsRequired:  []string{"pattern1"},
					PatternsForbidden: []string{"forbidden"},
				},
			},
			expected: true,
		},
		{
			name: "build failed",
			acceptance: &AcceptanceResult{
				BuildsOK: false,
			},
			task:     &E2ETask{Acceptance: TaskAcceptance{}},
			expected: false,
		},
		{
			name: "tests failed",
			acceptance: &AcceptanceResult{
				BuildsOK:    true,
				LintsOK:     true,
				TestsFailed: 1,
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{
					TestsPass: []string{"Test1"},
				},
			},
			expected: false,
		},
		{
			name: "pattern missing",
			acceptance: &AcceptanceResult{
				BuildsOK:        true,
				LintsOK:         true,
				PatternsMissing: []string{"required_pattern"},
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{
					PatternsRequired: []string{"required_pattern"},
				},
			},
			expected: false,
		},
		{
			name: "forbidden pattern found",
			acceptance: &AcceptanceResult{
				BuildsOK:   true,
				LintsOK:    true,
				Violations: []string{"forbidden_pattern"},
			},
			task:     &E2ETask{Acceptance: TaskAcceptance{}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluator.isTaskComplete(tt.acceptance, tt.task)
			if result != tt.expected {
				t.Errorf("Expected isTaskComplete=%v, got %v", tt.expected, result)
			}
		})
	}
}

// MockProvider implements llm.Provider for testing
type MockProvider struct {
	responses []string
	callCount int
}

func (m *MockProvider) Generate(ctx context.Context, prompt string) (string, error) {
	if m.callCount < len(m.responses) {
		response := m.responses[m.callCount]
		m.callCount++
		return response, nil
	}
	// Default response with a simple implementation
	return "```go\n// FILE: main.go\npackage main\n\nfunc main() {}\n```", nil
}

func (m *MockProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return m.Generate(ctx, prompt)
}

func (m *MockProvider) IsAvailable() bool {
	return true
}

func (m *MockProvider) Name() string {
	return "mock"
}

// Ensure cognition.Result is used
var _ = cognition.Result{}

// stringContains checks if a string contains a substring
func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || searchSubstring(s, substr))
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// floatEquals checks if two floats are approximately equal
func floatEquals(a, b, tolerance float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

func TestJourneyEvaluator_SetJudgeProvider(t *testing.T) {
	mockCortex := NewMockCortex()
	mockProvider := &MockProvider{}
	judgeProvider := &MockProvider{}

	evaluator := NewJourneyEvaluator(mockCortex, mockProvider, "/tmp/project", true)

	if evaluator.judgeProvider != nil {
		t.Error("Expected judgeProvider to be nil initially")
	}

	evaluator.SetJudgeProvider(judgeProvider)

	if evaluator.judgeProvider != judgeProvider {
		t.Error("Expected judgeProvider to be set")
	}
}

func TestCodeReviewJudge_EvaluateCode(t *testing.T) {
	tests := []struct {
		name           string
		code           map[string]string
		criteria       []string
		mockResponse   string
		expectedPass   bool
		expectedCount  int
	}{
		{
			name: "single criterion passes",
			code: map[string]string{
				"main.go": "package main\n\nimport \"github.com/redis/go-redis/v9\"\n",
			},
			criteria: []string{"Uses Redis client"},
			mockResponse: `{
				"evaluations": [
					{
						"criterion": "Uses Redis client",
						"passed": true,
						"reasoning": "Code imports go-redis v9 package",
						"confidence": 0.95
					}
				]
			}`,
			expectedPass:  true,
			expectedCount: 1,
		},
		{
			name: "single criterion fails",
			code: map[string]string{
				"main.go": "package main\n\nvar cache = make(map[string]string)\n",
			},
			criteria: []string{"Uses Redis client, not in-memory cache"},
			mockResponse: `{
				"evaluations": [
					{
						"criterion": "Uses Redis client, not in-memory cache",
						"passed": false,
						"reasoning": "Code uses in-memory map instead of Redis",
						"confidence": 0.9
					}
				]
			}`,
			expectedPass:  false,
			expectedCount: 1,
		},
		{
			name:          "empty criteria returns nil",
			code:          map[string]string{"main.go": "package main"},
			criteria:      []string{},
			mockResponse:  "",
			expectedPass:  true,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProvider := &MockProvider{
				responses: []string{tt.mockResponse},
			}
			judge := NewCodeReviewJudge(mockProvider)

			results, err := judge.EvaluateCode(context.Background(), tt.code, tt.criteria)
			if err != nil {
				t.Fatalf("EvaluateCode returned error: %v", err)
			}

			if tt.expectedCount == 0 {
				if results != nil {
					t.Errorf("Expected nil results for empty criteria, got %v", results)
				}
				return
			}

			if len(results) != tt.expectedCount {
				t.Errorf("Expected %d results, got %d", tt.expectedCount, len(results))
			}

			if tt.expectedCount > 0 {
				allPassed := true
				for _, r := range results {
					if !r.Passed {
						allPassed = false
						break
					}
				}
				if allPassed != tt.expectedPass {
					t.Errorf("Expected all passed=%v, got %v", tt.expectedPass, allPassed)
				}
			}
		})
	}
}

func TestCodeReviewJudge_ParseResponse(t *testing.T) {
	judge := &CodeReviewJudge{}

	tests := []struct {
		name           string
		response       string
		criteria       []string
		expectedPassed bool
		expectedNil    bool
	}{
		{
			name: "valid JSON response",
			response: `Here is my assessment:
{
	"evaluations": [
		{
			"criterion": "Uses proper error handling",
			"passed": true,
			"reasoning": "All errors are wrapped with context",
			"confidence": 0.85
		}
	]
}`,
			criteria:       []string{"Uses proper error handling"},
			expectedPassed: true,
			expectedNil:    false,
		},
		{
			name:        "non-JSON response returns nil",
			response:    "The criterion is met. The code properly implements caching.",
			criteria:    []string{"Implements caching"},
			expectedNil: true,
		},
		{
			name:        "empty response returns nil",
			response:    "",
			criteria:    []string{"Uses Redis"},
			expectedNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := judge.parseJudgeResponse(tt.response, tt.criteria)

			if tt.expectedNil {
				if results != nil {
					t.Errorf("Expected nil results, got %v", results)
				}
				return
			}

			if len(results) != len(tt.criteria) {
				t.Errorf("Expected %d results, got %d", len(tt.criteria), len(results))
				return
			}

			if results[0].Passed != tt.expectedPassed {
				t.Errorf("Expected passed=%v, got %v", tt.expectedPassed, results[0].Passed)
			}

			if results[0].Criterion != tt.criteria[0] {
				t.Errorf("Expected criterion=%q, got %q", tt.criteria[0], results[0].Criterion)
			}
		})
	}
}

func TestJourneyEvaluator_IsTaskComplete_WithCodeReview(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	tests := []struct {
		name       string
		acceptance *AcceptanceResult
		task       *E2ETask
		expected   bool
	}{
		{
			name: "code review passes",
			acceptance: &AcceptanceResult{
				BuildsOK:       true,
				LintsOK:        true,
				CodeReviewPass: true,
				CodeReviewResults: []CodeReviewResult{
					{Criterion: "Uses Redis", Passed: true},
					{Criterion: "Implements caching", Passed: true},
				},
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{
					CodeReview: []string{"Uses Redis", "Implements caching"},
				},
			},
			expected: true,
		},
		{
			name: "code review fails",
			acceptance: &AcceptanceResult{
				BuildsOK:       true,
				LintsOK:        true,
				CodeReviewPass: false,
				CodeReviewResults: []CodeReviewResult{
					{Criterion: "Uses Redis", Passed: true},
					{Criterion: "Implements caching", Passed: false, Reasoning: "Missing cache-aside pattern"},
				},
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{
					CodeReview: []string{"Uses Redis", "Implements caching"},
				},
			},
			expected: false,
		},
		{
			name: "no code review criteria - passes without results",
			acceptance: &AcceptanceResult{
				BuildsOK: true,
				LintsOK:  true,
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{},
			},
			expected: true,
		},
		{
			name: "code review criteria but no results - passes (judge not available)",
			acceptance: &AcceptanceResult{
				BuildsOK:          true,
				LintsOK:           true,
				CodeReviewPass:    false,
				CodeReviewResults: []CodeReviewResult{},
			},
			task: &E2ETask{
				Acceptance: TaskAcceptance{
					CodeReview: []string{"Uses Redis"},
				},
			},
			expected: true, // Passes because no results means judge wasn't run
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluator.isTaskComplete(tt.acceptance, tt.task)
			if result != tt.expected {
				t.Errorf("Expected isTaskComplete=%v, got %v", tt.expected, result)
			}
		})
	}
}

func TestJourneyEvaluator_BuildRetryPromptWithCodeReviewFeedback(t *testing.T) {
	evaluator := &JourneyEvaluator{}

	task := &E2ETask{
		Description: "Implement caching with Redis",
	}

	acceptance := &AcceptanceResult{
		BuildsOK:       true,
		LintsOK:        true,
		CodeReviewPass: false,
		CodeReviewResults: []CodeReviewResult{
			{Criterion: "Uses Redis client", Passed: true, Reasoning: "Redis client is imported"},
			{Criterion: "Implements cache-aside pattern", Passed: false, Reasoning: "Missing read-through caching logic"},
			{Criterion: "Has proper TTL handling", Passed: false, Reasoning: "No TTL specified for cached values"},
		},
	}

	prompt := evaluator.buildRetryPromptWithFeedback(task, "", acceptance)

	// Verify code review feedback is included
	if !stringContains(prompt, "Code Review Feedback:") {
		t.Error("Expected 'Code Review Feedback:' in prompt")
	}
	if !stringContains(prompt, "FAILED: Implements cache-aside pattern") {
		t.Error("Expected failed criterion in prompt")
	}
	if !stringContains(prompt, "Missing read-through caching logic") {
		t.Error("Expected reasoning for failed criterion in prompt")
	}
	if !stringContains(prompt, "Has proper TTL handling") {
		t.Error("Expected second failed criterion in prompt")
	}
	// Should not include passing criteria
	if stringContains(prompt, "FAILED: Uses Redis client") {
		t.Error("Should not include passing criteria as failed")
	}
}
