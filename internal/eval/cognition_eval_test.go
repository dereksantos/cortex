package eval

import (
	"context"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

func TestCognitionEvaluator_ModeTest(t *testing.T) {
	// Create mock Cortex with test data
	mock := NewMockCortex().WithReflexResults([]cognition.Result{
		{ID: "auth_module", Content: "Authentication module", Score: 0.9},
		{ID: "jwt_handler", Content: "JWT handler", Score: 0.8},
	})

	evaluator := NewCognitionEvaluator(mock)
	evaluator.SetVerbose(true)

	scenario := &CognitionScenario{
		ID:   "test-reflex",
		Type: CognitionMode,
		Name: "Test Reflex Mode",
		Mode: "reflex",
		ModeTests: []ModeTest{
			{
				ID: "test-1",
				Input: ModeTestInput{
					Query: cognition.Query{Text: "authentication"},
				},
				Expected: ModeTestExpected{
					MaxLatency: 15 * time.Millisecond,
					ResultIDs:  []string{"auth_module", "jwt_handler"},
				},
			},
		},
	}

	ctx := context.Background()
	result, err := evaluator.RunScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if !result.Pass {
		t.Errorf("Scenario should pass, got reason: %s", result.Reason)
	}

	if len(result.ModeResults) != 1 {
		t.Errorf("Expected 1 mode result, got %d", len(result.ModeResults))
	}

	if !result.ModeResults[0].LatencyPass {
		t.Errorf("Latency should be under threshold, got %v", result.ModeResults[0].Latency)
	}
}

func TestCognitionEvaluator_SessionTest(t *testing.T) {
	mock := NewMockCortex().WithReflexResults([]cognition.Result{
		{ID: "auth_module", Content: "Authentication", Score: 0.9},
	}).WithResolveResult(&cognition.ResolveResult{
		Decision:   cognition.Inject,
		Confidence: 0.9,
		Results:    []cognition.Result{{ID: "auth_module"}},
	})

	evaluator := NewCognitionEvaluator(mock)
	evaluator.SetVerbose(true)

	scenario := &CognitionScenario{
		ID:   "test-session",
		Type: CognitionSession,
		Name: "Test Session Accumulation",
		SessionSteps: []SessionStep{
			{
				ID:                "step-1",
				Query:             cognition.Query{Text: "authentication"},
				ExpectedResultIDs: []string{"auth_module"},
			},
			{
				ID:                  "step-2",
				Query:               cognition.Query{Text: "login flow"},
				ExpectedResultIDs:   []string{"auth_module"},
				ExpectQualityVsFull: ">= 0.8",
			},
		},
	}

	ctx := context.Background()
	result, err := evaluator.RunScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if result.SessionResults == nil {
		t.Fatal("SessionResults should not be nil")
	}

	if len(result.SessionResults.Steps) != 2 {
		t.Errorf("Expected 2 session steps, got %d", len(result.SessionResults.Steps))
	}
}

func TestCognitionEvaluator_BenefitTest(t *testing.T) {
	mock := NewMockCortex().WithResolveResult(&cognition.ResolveResult{
		Decision:   cognition.Inject,
		Confidence: 0.85,
	})

	evaluator := NewCognitionEvaluator(mock)
	evaluator.SetVerbose(true)

	scenario := &CognitionScenario{
		ID:   "test-benefit",
		Type: CognitionBenefit,
		Name: "Test ABR",
		BenefitQueries: []cognition.Query{
			{Text: "query 1"},
			{Text: "query 2"},
			{Text: "query 3"},
		},
		ExpectedABRThreshold: 0.7,
	}

	ctx := context.Background()
	result, err := evaluator.RunScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if result.BenefitResults == nil {
		t.Fatal("BenefitResults should not be nil")
	}

	if len(result.BenefitResults.QueryResults) != 3 {
		t.Errorf("Expected 3 query results, got %d", len(result.BenefitResults.QueryResults))
	}

	t.Logf("Average ABR: %.2f", result.BenefitResults.AverageABR)
}

func TestCognitionEvaluator_DreamTest(t *testing.T) {
	mock := NewMockCortex()

	evaluator := NewCognitionEvaluator(mock)
	evaluator.SetVerbose(true)

	scenario := &CognitionScenario{
		ID:               "test-dream",
		Type:             CognitionDream,
		Name:             "Test Dream",
		DreamSources:     []string{"project", "cortex"},
		ExpectedInsights: 1,
		ExpectedCoverage: 0.0, // Mock doesn't track coverage
	}

	ctx := context.Background()
	result, err := evaluator.RunScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if result.DreamResults == nil {
		t.Fatal("DreamResults should not be nil")
	}

	if result.DreamResults.InsightsGenerated < 1 {
		t.Errorf("Expected at least 1 insight, got %d", result.DreamResults.InsightsGenerated)
	}
}

func TestCognitionEvaluator_ConflictTest_HighSeverity(t *testing.T) {
	mock := NewMockCortex()
	evaluator := NewCognitionEvaluator(mock)
	evaluator.SetVerbose(true)

	scenario := &CognitionScenario{
		ID:            "test-conflict-high",
		Type:          CognitionConflict,
		Name:          "High Severity Conflict",
		ConflictTopic: "testing",
		Evidence: []PatternEvidence{
			{Source: "code", Pattern: "stdlib testing", Count: 4, Weight: 0.8},
			{Source: "claude_md", Pattern: "testify", Count: 1, Weight: 1.0},
		},
		ConflictExpected: ConflictExpectation{
			ConflictDetected: true,
			Severity:         SeverityHigh,
			MustSurface:      true,
		},
	}

	ctx := context.Background()
	result, err := evaluator.RunScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if result.ConflictResults == nil {
		t.Fatal("ConflictResults should not be nil")
	}

	if !result.ConflictResults.ConflictDetected {
		t.Error("Expected conflict to be detected")
	}

	if result.ConflictResults.DetectedSeverity != SeverityHigh {
		t.Errorf("Expected severity high, got %s", result.ConflictResults.DetectedSeverity)
	}

	if !result.ConflictResults.Surfaced {
		t.Error("Expected high severity conflict to be surfaced to user")
	}

	if !result.Pass {
		t.Errorf("Scenario should pass, got reason: %s", result.Reason)
	}
}

func TestCognitionEvaluator_ConflictTest_LowSeverity(t *testing.T) {
	mock := NewMockCortex()
	evaluator := NewCognitionEvaluator(mock)
	evaluator.SetVerbose(true)

	scenario := &CognitionScenario{
		ID:            "test-conflict-low",
		Type:          CognitionConflict,
		Name:          "Low Severity Conflict",
		ConflictTopic: "formatting",
		Evidence: []PatternEvidence{
			{Source: "code", Pattern: "tabs", Count: 10, Weight: 0.8},
			{Source: "code", Pattern: "spaces", Count: 2, Weight: 0.8},
		},
		ConflictExpected: ConflictExpectation{
			ConflictDetected: true,
			Severity:         SeverityLow,
			MustSurface:      false,
			AllowedPatterns:  []string{"tabs"},
		},
	}

	ctx := context.Background()
	result, err := evaluator.RunScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if result.ConflictResults == nil {
		t.Fatal("ConflictResults should not be nil")
	}

	if !result.ConflictResults.ConflictDetected {
		t.Error("Expected conflict to be detected")
	}

	if result.ConflictResults.DetectedSeverity != SeverityLow {
		t.Errorf("Expected severity low, got %s", result.ConflictResults.DetectedSeverity)
	}

	if result.ConflictResults.Surfaced {
		t.Error("Expected low severity conflict to be resolved silently")
	}

	if result.ConflictResults.ChosenPattern != "tabs" {
		t.Errorf("Expected chosen pattern 'tabs', got '%s'", result.ConflictResults.ChosenPattern)
	}

	if !result.Pass {
		t.Errorf("Scenario should pass, got reason: %s", result.Reason)
	}
}

func TestLoadCognitionScenario(t *testing.T) {
	scenario, err := LoadCognitionScenario("../../test/evals/scenarios/cognition/reflex_latency.yaml")
	if err != nil {
		t.Fatalf("LoadCognitionScenario failed: %v", err)
	}

	if scenario.ID != "reflex-latency" {
		t.Errorf("Expected ID 'reflex-latency', got '%s'", scenario.ID)
	}

	if scenario.Type != CognitionMode {
		t.Errorf("Expected type 'mode', got '%s'", scenario.Type)
	}

	if scenario.Mode != "reflex" {
		t.Errorf("Expected mode 'reflex', got '%s'", scenario.Mode)
	}

	if len(scenario.ModeTests) != 2 {
		t.Errorf("Expected 2 mode tests, got %d", len(scenario.ModeTests))
	}
}

func TestLoadConflictScenario(t *testing.T) {
	scenario, err := LoadCognitionScenario("../../test/evals/scenarios/cognition/testing_conflict.yaml")
	if err != nil {
		t.Fatalf("LoadCognitionScenario failed: %v", err)
	}

	if scenario.ID != "testing-pattern-conflict" {
		t.Errorf("Expected ID 'testing-pattern-conflict', got '%s'", scenario.ID)
	}

	if scenario.Type != CognitionConflict {
		t.Errorf("Expected type 'conflict', got '%s'", scenario.Type)
	}

	if scenario.ConflictTopic != "testing" {
		t.Errorf("Expected conflict topic 'testing', got '%s'", scenario.ConflictTopic)
	}

	if len(scenario.Evidence) != 2 {
		t.Errorf("Expected 2 evidence items, got %d", len(scenario.Evidence))
	}

	if !scenario.ConflictExpected.MustSurface {
		t.Error("Expected must_surface to be true")
	}
}
