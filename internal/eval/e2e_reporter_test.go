package eval

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestE2EReporter_FormatJourneyResult(t *testing.T) {
	reporter := NewE2EReporter(false)

	result := &E2EJourneyResult{
		JourneyID: "api-service-evolution",
		RunID:     "run-001",
		StartTime: time.Now().Add(-10 * time.Minute),
		EndTime:   time.Now(),
		Duration:  10 * time.Minute,
		TreatmentResults: &E2ERunResults{
			SessionResults: []E2ESessionResult{
				{
					SessionID:    "08",
					Phase:        PhaseFeature,
					EventsStored: 5,
					TaskResult: &E2ETaskResult{
						TaskDescription:       "Add caching",
						Completed:             true,
						CompletedSuccessfully: true,
						Turns:                 4,
						TokensUsed:            15000,
						TestsPassed:           3,
						TestsFailed:           0,
						PatternMatches: []PatternMatch{
							{Pattern: "redis.Client", Found: true},
							{Pattern: "cache.Get", Found: true},
							{Pattern: "cache.Set", Found: true},
							{Pattern: "cache.Delete", Found: true},
						},
						PatternViolations: []PatternMatch{},
						BuildSucceeded:    true,
					},
				},
				{
					SessionID:    "16",
					Phase:        PhaseFeature,
					EventsStored: 3,
					TaskResult: &E2ETaskResult{
						TaskDescription:       "Saga pattern",
						Completed:             true,
						CompletedSuccessfully: true,
						Turns:                 6,
						TokensUsed:            30000,
						TestsPassed:           2,
						TestsFailed:           0,
						PatternMatches: []PatternMatch{
							{Pattern: "saga.Start", Found: true},
							{Pattern: "saga.Compensate", Found: true},
							{Pattern: "saga.Execute", Found: true},
							{Pattern: "saga.Rollback", Found: true},
							{Pattern: "saga.Complete", Found: true},
						},
						PatternViolations: []PatternMatch{},
						BuildSucceeded:    true,
					},
				},
			},
			TasksTotal:         2,
			TasksCompleted:     2,
			TaskCompletionRate: 1.0,
			TestsTotal:         5,
			TestsPassed:        5,
			TestPassRate:       1.0,
			TotalTurns:         10,
			AverageTurns:       5.0,
			TotalTokens:        45000,
			PatternViolations:  0,
			CorrectionsNeeded:  0,
			EventsStored:       51,
		},
		BaselineResults: &E2ERunResults{
			SessionResults: []E2ESessionResult{
				{
					SessionID:    "08",
					Phase:        PhaseFeature,
					EventsStored: 0,
					TaskResult: &E2ETaskResult{
						TaskDescription:       "Add caching",
						Completed:             true,
						CompletedSuccessfully: false,
						Turns:                 9,
						TokensUsed:            35000,
						TestsPassed:           3,
						TestsFailed:           0,
						PatternMatches: []PatternMatch{
							{Pattern: "redis.Client", Found: false},
							{Pattern: "cache.Get", Found: true},
							{Pattern: "cache.Set", Found: true},
							{Pattern: "cache.Delete", Found: true},
						},
						PatternViolations: []PatternMatch{
							{Pattern: "sync.Map", Found: true},
						},
						BuildSucceeded:        true,
						CorrectionsApplied:    []string{"Use Redis, not sync.Map"},
					},
				},
				{
					SessionID:    "16",
					Phase:        PhaseFeature,
					EventsStored: 0,
					TaskResult: &E2ETaskResult{
						TaskDescription:       "Saga pattern",
						Completed:             false,
						CompletedSuccessfully: false,
						Turns:                 15,
						TokensUsed:            63000,
						TestsPassed:           0,
						TestsFailed:           2,
						PatternMatches: []PatternMatch{
							{Pattern: "saga.Start", Found: true},
							{Pattern: "saga.Compensate", Found: false},
							{Pattern: "saga.Execute", Found: true},
							{Pattern: "saga.Rollback", Found: false},
							{Pattern: "saga.Complete", Found: false},
						},
						PatternViolations: []PatternMatch{},
						BuildSucceeded:    true,
						FailureReason:     "max turns reached",
					},
				},
			},
			TasksTotal:         2,
			TasksCompleted:     1,
			TaskCompletionRate: 0.5,
			TestsTotal:         5,
			TestsPassed:        3,
			TestPassRate:       0.6,
			TotalTurns:         24,
			AverageTurns:       12.0,
			TotalTokens:        98000,
			PatternViolations:  1,
			CorrectionsNeeded:  1,
			EventsStored:       0,
		},
		Comparison: &E2EComparison{
			TaskCompletionLift: 0.5,
			TestPassLift:       0.4,
			TurnReduction:      0.58,
			TokenReduction:     0.54,
			ViolationReduction: 1,
			CorrectionReduction: 1,
			OverallLift:        0.5,
			Regression:         false,
		},
		Pass:   true,
		Reason: "Cortex reduced implementation cost by 54% and improved task completion by 50%.",
	}

	output := reporter.FormatJourneyResult(result)

	// Check header
	if !strings.Contains(output, "E2E Journey Eval: api-service-evolution") {
		t.Error("missing journey header")
	}

	// Check sessions count
	if !strings.Contains(output, "Sessions: 2") {
		t.Error("missing sessions count")
	}

	// Check treatment section
	if !strings.Contains(output, "Treatment Run (Cortex-Enabled)") {
		t.Error("missing treatment section")
	}

	// Check baseline section
	if !strings.Contains(output, "Baseline Run (No Memory)") {
		t.Error("missing baseline section")
	}

	// Check comparison section
	if !strings.Contains(output, "Comparison") {
		t.Error("missing comparison section")
	}

	// Check checkmarks
	if !strings.Contains(output, "\u2713") {
		t.Error("missing checkmark for passed tasks")
	}

	// Check verdict
	if !strings.Contains(output, "Verdict:") {
		t.Error("missing verdict")
	}
}

func TestE2EReporter_FormatComparison(t *testing.T) {
	reporter := NewE2EReporter(false)

	comparison := &E2EComparison{
		TaskCompletionLift:  0.50,
		TestPassLift:        0.40,
		TurnReduction:       0.58,
		TokenReduction:      0.54,
		ViolationReduction:  1,
		CorrectionReduction: 1,
		OverallLift:         0.50,
		Regression:          false,
	}

	output := reporter.FormatComparison(comparison)

	// Check header
	if !strings.Contains(output, "Comparison") {
		t.Error("missing comparison header")
	}

	// Check lifts are formatted with +
	if !strings.Contains(output, "+50%") {
		t.Error("missing positive task completion lift")
	}

	// Check reductions are formatted with -
	if !strings.Contains(output, "-58%") || !strings.Contains(output, "-54%") {
		t.Error("missing turn/token reduction percentages")
	}
}

func TestE2EReporter_FormatComparisonTable(t *testing.T) {
	reporter := NewE2EReporter(false)

	treatment := &E2ERunResults{
		TaskCompletionRate: 1.0,
		TestPassRate:       1.0,
		AverageTurns:       5.0,
		TotalTokens:        45000,
		PatternViolations:  0,
		CorrectionsNeeded:  0,
	}

	baseline := &E2ERunResults{
		TaskCompletionRate: 0.5,
		TestPassRate:       0.6,
		AverageTurns:       12.0,
		TotalTokens:        98000,
		PatternViolations:  1,
		CorrectionsNeeded:  1,
	}

	comparison := &E2EComparison{
		TaskCompletionLift:  0.50,
		TestPassLift:        0.40,
		TurnReduction:       0.58,
		TokenReduction:      0.54,
		ViolationReduction:  1,
		CorrectionReduction: 1,
	}

	output := reporter.FormatComparisonTable(treatment, baseline, comparison)

	// Check column headers
	if !strings.Contains(output, "Cortex") {
		t.Error("missing Cortex column header")
	}
	if !strings.Contains(output, "Baseline") {
		t.Error("missing Baseline column header")
	}
	if !strings.Contains(output, "Lift") {
		t.Error("missing Lift column header")
	}

	// Check values
	if !strings.Contains(output, "100%") {
		t.Error("missing treatment completion rate")
	}
	if !strings.Contains(output, "50%") {
		t.Error("missing baseline completion rate")
	}
	if !strings.Contains(output, "45K") {
		t.Error("missing treatment tokens")
	}
	if !strings.Contains(output, "98K") {
		t.Error("missing baseline tokens")
	}
}

func TestE2EReporter_FormatSummary(t *testing.T) {
	reporter := NewE2EReporter(false)

	tests := []struct {
		name     string
		result   *E2EJourneyResult
		contains []string
	}{
		{
			name: "passing journey",
			result: &E2EJourneyResult{
				JourneyID: "test-journey",
				Pass:      true,
				TreatmentResults: &E2ERunResults{
					TaskCompletionRate: 1.0,
				},
				BaselineResults: &E2ERunResults{
					TaskCompletionRate: 0.5,
				},
				Comparison: &E2EComparison{
					TaskCompletionLift: 0.5,
					TurnReduction:      0.4,
					ViolationReduction: 2,
				},
			},
			contains: []string{"PASS", "test-journey", "+50%", "-40%", "-2"},
		},
		{
			name: "failing journey",
			result: &E2EJourneyResult{
				JourneyID: "failing-journey",
				Pass:      false,
				TreatmentResults: &E2ERunResults{
					TaskCompletionRate: 0.5,
				},
				BaselineResults: &E2ERunResults{
					TaskCompletionRate: 0.6,
				},
				Comparison: &E2EComparison{
					TaskCompletionLift: -0.1,
					TurnReduction:      -0.2,
					ViolationReduction: -1,
				},
			},
			contains: []string{"FAIL", "failing-journey"},
		},
		{
			name: "incomplete results",
			result: &E2EJourneyResult{
				JourneyID: "incomplete",
			},
			contains: []string{"incomplete", "incomplete results"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := reporter.FormatSummary(tt.result)
			for _, want := range tt.contains {
				if !strings.Contains(output, want) {
					t.Errorf("summary missing %q, got: %s", want, output)
				}
			}
		})
	}
}

func TestE2EReporter_FormatTaskResult(t *testing.T) {
	reporter := NewE2EReporter(true) // verbose mode

	task := &E2ETaskResult{
		TaskDescription:       "Implement user authentication with OAuth2",
		Completed:             true,
		CompletedSuccessfully: true,
		Turns:                 5,
		TokensUsed:            25000,
		Duration:              2 * time.Minute,
		TestsPassed:           4,
		TestsFailed:           1,
		PatternMatches: []PatternMatch{
			{Pattern: "oauth2.Config", Found: true},
			{Pattern: "token.Valid", Found: true},
			{Pattern: "jwt.Parse", Found: false},
		},
		PatternViolations: []PatternMatch{
			{Pattern: "plain_password", Found: true, Locations: []string{"auth.go:42"}},
		},
		BuildSucceeded: true,
	}

	output := reporter.FormatTaskResult(task)

	// Check task description
	if !strings.Contains(output, "Implement user authentication") {
		t.Error("missing task description")
	}

	// Check completed status
	if !strings.Contains(output, "\u2713") {
		t.Error("missing completed checkmark")
	}

	// Check tests
	if !strings.Contains(output, "4/5 passed") {
		t.Error("missing test results")
	}

	// Check patterns
	if !strings.Contains(output, "2/3 required found") {
		t.Error("missing pattern count")
	}

	// Check violations in verbose mode
	if !strings.Contains(output, "plain_password") {
		t.Error("missing violation details in verbose mode")
	}

	// Check turns
	if !strings.Contains(output, "Turns:") {
		t.Error("missing turns")
	}

	// Check tokens
	if !strings.Contains(output, "25K") {
		t.Error("missing token count")
	}
}

func TestE2EReporter_ReportE2E(t *testing.T) {
	reporter := NewE2EReporter(false)

	result := &E2EJourneyResult{
		JourneyID: "test-journey",
		Pass:      true,
		Reason:    "All tests passed",
		TreatmentResults: &E2ERunResults{
			SessionResults:     []E2ESessionResult{},
			TaskCompletionRate: 1.0,
			TestPassRate:       1.0,
			AverageTurns:       5.0,
			TotalTokens:        45000,
			EventsStored:       10,
		},
		BaselineResults: &E2ERunResults{
			SessionResults:     []E2ESessionResult{},
			TaskCompletionRate: 0.5,
			TestPassRate:       0.6,
			AverageTurns:       12.0,
			TotalTokens:        98000,
		},
		Comparison: &E2EComparison{
			TaskCompletionLift: 0.5,
			TestPassLift:       0.4,
			TurnReduction:      0.58,
			TokenReduction:     0.54,
		},
	}

	var buf bytes.Buffer
	err := reporter.ReportE2E(&buf, result)
	if err != nil {
		t.Fatalf("ReportE2E failed: %v", err)
	}

	output := buf.String()

	// Should contain the comparison table with actual values
	if !strings.Contains(output, "100%") && !strings.Contains(output, "50%") {
		t.Error("should contain actual percentage values from comparison table")
	}

	if !strings.Contains(output, "Verdict:") {
		t.Error("missing verdict")
	}
}

func TestFormatHelpers(t *testing.T) {
	tests := []struct {
		name     string
		fn       func() string
		expected string
	}{
		{"checkmark pass", func() string { return formatCheckmark(true) }, "\u2713"},
		{"checkmark fail", func() string { return formatCheckmark(false) }, "\u2717"},
		{"percent 100", func() string { return formatPercent(1.0) }, "100%"},
		{"percent 50", func() string { return formatPercent(0.5) }, "50%"},
		{"lift positive", func() string { return formatLiftPercent(0.5) }, "+50%"},
		{"lift negative", func() string { return formatLiftPercent(-0.2) }, "-20%"},
		{"lift zero", func() string { return formatLiftPercent(0) }, "0%"},
		{"reduction positive", func() string { return formatReductionPercent(0.5) }, "-50%"},
		{"reduction negative", func() string { return formatReductionPercent(-0.2) }, "+20%"},
		{"reduction zero", func() string { return formatReductionPercent(0) }, "0%"},
		{"reduction int positive", func() string { return formatReductionInt(3) }, "-3"},
		{"reduction int negative", func() string { return formatReductionInt(-2) }, "+2"},
		{"reduction int zero", func() string { return formatReductionInt(0) }, "0"},
		{"tokens small", func() string { return formatTokens(500) }, "500"},
		{"tokens large", func() string { return formatTokens(45000) }, "45K"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn()
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is..."},
		{"with\nnewline", 20, "with newline"},
		{"exact len", 9, "exact len"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateString(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expected)
			}
		})
	}
}

func TestCountPatternsFound(t *testing.T) {
	matches := []PatternMatch{
		{Pattern: "a", Found: true},
		{Pattern: "b", Found: false},
		{Pattern: "c", Found: true},
		{Pattern: "d", Found: true},
	}

	got := countPatternsFound(matches)
	if got != 3 {
		t.Errorf("countPatternsFound = %d, want 3", got)
	}
}
