package eval

import (
	"testing"
)

func TestTokenReduction(t *testing.T) {
	tests := []struct {
		name           string
		baselineTokens int
		cortexTokens   int
		wantReduction  float64
	}{
		{
			name:           "20% reduction",
			baselineTokens: 100,
			cortexTokens:   80,
			wantReduction:  0.2,
		},
		{
			name:           "no reduction",
			baselineTokens: 100,
			cortexTokens:   100,
			wantReduction:  0.0,
		},
		{
			name:           "50% reduction",
			baselineTokens: 200,
			cortexTokens:   100,
			wantReduction:  0.5,
		},
		{
			name:           "cortex uses more (negative reduction)",
			baselineTokens: 100,
			cortexTokens:   120,
			wantReduction:  -0.2,
		},
		{
			name:           "zero baseline",
			baselineTokens: 0,
			cortexTokens:   50,
			wantReduction:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testResults := []TestResult{
				{
					TestID:         "t1",
					BaselineScore:  0.5,
					CortexScore:    0.7,
					Lift:           0.4,
					Winner:         "cortex",
					Pass:           true,
					BaselineTokens: tt.baselineTokens,
					CortexTokens:   tt.cortexTokens,
				},
			}

			result := CalculateScenarioResult("test", "Test", testResults)

			if result.AvgBaselineTokens != tt.baselineTokens {
				t.Errorf("AvgBaselineTokens: expected %d, got %d", tt.baselineTokens, result.AvgBaselineTokens)
			}
			if result.AvgCortexTokens != tt.cortexTokens {
				t.Errorf("AvgCortexTokens: expected %d, got %d", tt.cortexTokens, result.AvgCortexTokens)
			}

			diff := result.TokenReduction - tt.wantReduction
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("TokenReduction: expected %.2f, got %.2f", tt.wantReduction, result.TokenReduction)
			}
		})
	}
}

func TestAvgABR(t *testing.T) {
	tests := []struct {
		name      string
		scenarios []ScenarioResult
		wantABR   float64
	}{
		{
			name: "single scenario with ABR",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "Test 1",
					Tests:            []TestResult{{TestID: "t1"}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					HasRanking:       true,
					AvgABR:           0.85,
					Pass:             true,
				},
			},
			wantABR: 0.85,
		},
		{
			name: "multiple scenarios with ABR",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "Test 1",
					Tests:            []TestResult{{TestID: "t1"}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					HasRanking:       true,
					AvgABR:           0.80,
					Pass:             true,
				},
				{
					ScenarioID:       "s2",
					Name:             "Test 2",
					Tests:            []TestResult{{TestID: "t2"}},
					AvgBaselineScore: 0.6,
					AvgCortexScore:   0.8,
					AvgLift:          0.33,
					HasRanking:       true,
					AvgABR:           0.90,
					Pass:             true,
				},
			},
			wantABR: 0.85,
		},
		{
			name: "mix of scenarios with and without ABR",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "With ABR",
					Tests:            []TestResult{{TestID: "t1"}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					HasRanking:       true,
					AvgABR:           0.90,
					Pass:             true,
				},
				{
					ScenarioID:       "s2",
					Name:             "Without ABR",
					Tests:            []TestResult{{TestID: "t2"}},
					AvgBaselineScore: 0.6,
					AvgCortexScore:   0.8,
					AvgLift:          0.33,
					HasRanking:       false,
					AvgABR:           0,
					Pass:             true,
				},
			},
			wantABR: 0.90, // Only the scenario with ABR counts
		},
		{
			name: "no scenarios with ABR",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "No ABR",
					Tests:            []TestResult{{TestID: "t1"}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					Pass:             true,
				},
			},
			wantABR: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := CalculateResults(tt.scenarios, "mock", "test")

			diff := results.AvgABR - tt.wantABR
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("AvgABR: expected %.2f, got %.2f", tt.wantABR, results.AvgABR)
			}
		})
	}
}

func TestTokenAggregation(t *testing.T) {
	scenarios := []ScenarioResult{
		{
			ScenarioID:        "s1",
			Name:              "Test 1",
			Tests:             []TestResult{{TestID: "t1"}, {TestID: "t2"}},
			AvgBaselineScore:  0.5,
			AvgCortexScore:    0.7,
			AvgLift:           0.4,
			AvgBaselineTokens: 100,
			AvgCortexTokens:   80,
			TokenReduction:    0.2,
			Pass:              true,
		},
		{
			ScenarioID:        "s2",
			Name:              "Test 2",
			Tests:             []TestResult{{TestID: "t3"}},
			AvgBaselineScore:  0.6,
			AvgCortexScore:    0.8,
			AvgLift:           0.33,
			AvgBaselineTokens: 200,
			AvgCortexTokens:   150,
			TokenReduction:    0.25,
			Pass:              true,
		},
	}

	results := CalculateResults(scenarios, "mock", "test")

	// s1: 2 tests * 100 avg = 200 baseline, 2 * 80 = 160 cortex
	// s2: 1 test * 200 avg = 200 baseline, 1 * 150 = 150 cortex
	// Total: 400 baseline, 310 cortex
	expectedBaseline := 400
	expectedCortex := 310

	if results.TotalBaselineTokens != expectedBaseline {
		t.Errorf("TotalBaselineTokens: expected %d, got %d", expectedBaseline, results.TotalBaselineTokens)
	}
	if results.TotalCortexTokens != expectedCortex {
		t.Errorf("TotalCortexTokens: expected %d, got %d", expectedCortex, results.TotalCortexTokens)
	}

	// Token reduction = (400-310)/400 = 0.225
	expectedReduction := 0.225
	diff := results.AvgTokenReduction - expectedReduction
	if diff > 0.01 || diff < -0.01 {
		t.Errorf("AvgTokenReduction: expected %.3f, got %.3f", expectedReduction, results.AvgTokenReduction)
	}
}

func TestCalculateABR(t *testing.T) {
	tests := []struct {
		name     string
		fastNDCG float64
		fullNDCG float64
		want     float64
	}{
		{"equal quality", 0.8, 0.8, 1.0},
		{"fast worse than full", 0.6, 0.8, 0.75},
		{"both zero", 0.0, 0.0, 1.0},
		{"fast better capped at 1.0", 0.9, 0.8, 1.0},
		{"full zero, fast nonzero", 0.5, 0.0, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateABR(tt.fastNDCG, tt.fullNDCG)
			diff := got - tt.want
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("CalculateABR(%.2f, %.2f) = %.2f, want %.2f", tt.fastNDCG, tt.fullNDCG, got, tt.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	tests := []struct {
		name     string
		response string
		expect   Expect
		want     float64
	}{
		{
			name:     "all includes match",
			response: "Use JWT for auth with refresh tokens",
			expect:   Expect{Includes: []string{"jwt", "refresh"}},
			want:     1.0,
		},
		{
			name:     "no matches",
			response: "Use sessions for auth",
			expect:   Expect{Includes: []string{"jwt", "refresh"}},
			want:     0.0,
		},
		{
			name:     "excludes pass",
			response: "Use JWT for auth",
			expect:   Expect{Excludes: []string{"session", "cookie"}},
			want:     1.0,
		},
		{
			name:     "no expectations",
			response: "anything",
			expect:   Expect{},
			want:     1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Score(tt.response, tt.expect)
			diff := got - tt.want
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("Score() = %.2f, want %.2f", got, tt.want)
			}
		})
	}
}

func TestDetermineWinner(t *testing.T) {
	tests := []struct {
		name     string
		cortex   float64
		baseline float64
		want     string
	}{
		{"cortex wins", 0.8, 0.5, "cortex"},
		{"baseline wins", 0.3, 0.8, "baseline"},
		{"tie", 0.5, 0.52, "tie"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineWinner(tt.cortex, tt.baseline)
			if got != tt.want {
				t.Errorf("DetermineWinner(%.2f, %.2f) = %q, want %q", tt.cortex, tt.baseline, got, tt.want)
			}
		})
	}
}

func TestCalculateMPR(t *testing.T) {
	tests := []struct {
		name         string
		cortexScore  float64
		compareScore float64
		want         float64
	}{
		{"equal scores", 0.8, 0.8, 1.0},
		{"cortex worse", 0.6, 0.8, 0.75},
		{"cortex better", 0.9, 0.8, 1.125},
		{"both zero", 0.0, 0.0, 1.0},
		{"compare zero, cortex nonzero", 0.5, 0.0, 1.0},
		{"cortex zero, compare nonzero", 0.0, 0.8, 0.0},
		{"capped at 2.0", 1.0, 0.3, 2.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateMPR(tt.cortexScore, tt.compareScore)
			diff := got - tt.want
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("CalculateMPR(%.2f, %.2f) = %.2f, want %.2f", tt.cortexScore, tt.compareScore, got, tt.want)
			}
		})
	}
}

func TestMPRAggregation(t *testing.T) {
	tests := []struct {
		name       string
		scenarios  []ScenarioResult
		wantMPR    float64
		wantHasMPR bool
	}{
		{
			name: "single scenario with comparison",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "Test 1",
					Tests:            []TestResult{{TestID: "t1", HasCompare: true, CompareTokens: 50}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					HasCompare:       true,
					AvgCompareScore:  0.85,
					AvgMPR:           0.82,
					Pass:             true,
				},
			},
			wantMPR:    0.82,
			wantHasMPR: true,
		},
		{
			name: "mix with and without comparison",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "With MPR",
					Tests:            []TestResult{{TestID: "t1", HasCompare: true, CompareTokens: 50}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					HasCompare:       true,
					AvgCompareScore:  0.85,
					AvgMPR:           0.82,
					Pass:             true,
				},
				{
					ScenarioID:       "s2",
					Name:             "Without MPR",
					Tests:            []TestResult{{TestID: "t2"}},
					AvgBaselineScore: 0.6,
					AvgCortexScore:   0.8,
					AvgLift:          0.33,
					Pass:             true,
				},
			},
			wantMPR:    0.82, // Only the scenario with comparison counts
			wantHasMPR: true,
		},
		{
			name: "no scenarios with comparison",
			scenarios: []ScenarioResult{
				{
					ScenarioID:       "s1",
					Name:             "No comparison",
					Tests:            []TestResult{{TestID: "t1"}},
					AvgBaselineScore: 0.5,
					AvgCortexScore:   0.7,
					AvgLift:          0.4,
					Pass:             true,
				},
			},
			wantMPR:    0.0,
			wantHasMPR: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := CalculateResults(tt.scenarios, "mock", "test")

			diff := results.AvgMPR - tt.wantMPR
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("AvgMPR: expected %.2f, got %.2f", tt.wantMPR, results.AvgMPR)
			}

			hasMPR := results.AvgMPR > 0
			if hasMPR != tt.wantHasMPR {
				t.Errorf("HasMPR: expected %v, got %v", tt.wantHasMPR, hasMPR)
			}
		})
	}
}

func TestScenarioMPRAggregation(t *testing.T) {
	tests := []TestResult{
		{
			TestID:        "t1",
			BaselineScore: 0.4,
			CortexScore:   0.7,
			Lift:          0.75,
			Winner:        "cortex",
			Pass:          true,
			HasCompare:    true,
			CompareScore:  0.8,
			MPR:           0.875,
		},
		{
			TestID:        "t2",
			BaselineScore: 0.5,
			CortexScore:   0.9,
			Lift:          0.8,
			Winner:        "cortex",
			Pass:          true,
			HasCompare:    true,
			CompareScore:  0.85,
			MPR:           1.059,
		},
		{
			TestID:        "t3",
			BaselineScore: 0.6,
			CortexScore:   0.6,
			Lift:          0.0,
			Winner:        "tie",
			Pass:          true,
			// No comparison for this test
		},
	}

	result := CalculateScenarioResult("test", "Test", tests)

	if !result.HasCompare {
		t.Fatal("expected HasCompare to be true")
	}

	// Only t1 and t2 have comparison: avg compare score = (0.8+0.85)/2 = 0.825
	wantCompare := 0.825
	diff := result.AvgCompareScore - wantCompare
	if diff > 0.01 || diff < -0.01 {
		t.Errorf("AvgCompareScore: expected %.3f, got %.3f", wantCompare, result.AvgCompareScore)
	}

	// avg MPR = (0.875 + 1.059) / 2 = 0.967
	wantMPR := 0.967
	diff = result.AvgMPR - wantMPR
	if diff > 0.01 || diff < -0.01 {
		t.Errorf("AvgMPR: expected %.3f, got %.3f", wantMPR, result.AvgMPR)
	}
}

func TestCalculateLift(t *testing.T) {
	tests := []struct {
		name     string
		cortex   float64
		baseline float64
		want     float64
	}{
		{"positive lift", 0.8, 0.5, 0.6},
		{"no lift", 0.5, 0.5, 0.0},
		{"both zero", 0.0, 0.0, 0.0},
		{"baseline zero cortex nonzero", 0.5, 0.0, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateLift(tt.cortex, tt.baseline)
			diff := got - tt.want
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("CalculateLift(%.2f, %.2f) = %.2f, want %.2f", tt.cortex, tt.baseline, got, tt.want)
			}
		})
	}
}
