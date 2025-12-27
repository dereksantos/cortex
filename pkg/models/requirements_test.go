package models

import (
	"strings"
	"testing"
)

func TestAllModels(t *testing.T) {
	models := AllModels()

	t.Run("returns expected number of models", func(t *testing.T) {
		if len(models) != 6 {
			t.Errorf("expected 6 models, got %d", len(models))
		}
	})

	t.Run("exactly one model is recommended", func(t *testing.T) {
		recommended := 0
		for _, m := range models {
			if m.Recommended {
				recommended++
			}
		}
		if recommended != 1 {
			t.Errorf("expected exactly 1 recommended model, got %d", recommended)
		}
	})

	t.Run("all models have positive RAM requirements", func(t *testing.T) {
		for _, m := range models {
			if m.RAMGB <= 0 {
				t.Errorf("model %s has non-positive RAM requirement: %.1f", m.Name, m.RAMGB)
			}
		}
	})

	t.Run("all models have non-empty names", func(t *testing.T) {
		for _, m := range models {
			if m.Name == "" {
				t.Error("found model with empty name")
			}
		}
	})

	t.Run("all models have non-empty descriptions", func(t *testing.T) {
		for _, m := range models {
			if m.Description == "" {
				t.Errorf("model %s has empty description", m.Name)
			}
		}
	})

	t.Run("all models have non-empty size", func(t *testing.T) {
		for _, m := range models {
			if m.Size == "" {
				t.Errorf("model %s has empty size", m.Name)
			}
		}
	})

	t.Run("recommended model is mistral:7b", func(t *testing.T) {
		for _, m := range models {
			if m.Recommended && m.Name != "mistral:7b" {
				t.Errorf("expected mistral:7b to be recommended, got %s", m.Name)
			}
		}
	})
}

func TestFilterByRAM(t *testing.T) {
	tests := []struct {
		name          string
		availableRAM  float64
		expectedCount int
		shouldContain []string
	}{
		{
			name:          "very low RAM returns no models",
			availableRAM:  1.0,
			expectedCount: 0,
			shouldContain: nil,
		},
		{
			name:          "2.0 GB returns one model",
			availableRAM:  2.0,
			expectedCount: 1,
			shouldContain: []string{"phi3:mini"},
		},
		{
			name:          "2.5 GB returns two models",
			availableRAM:  2.5,
			expectedCount: 2,
			shouldContain: []string{"phi3:mini", "llama3.2:3b"},
		},
		{
			name:          "5.0 GB returns five models",
			availableRAM:  5.0,
			expectedCount: 5,
			shouldContain: []string{"phi3:mini", "llama3.2:3b", "mistral:7b", "codellama:7b", "llama3.1:8b"},
		},
		{
			name:          "50.0 GB returns all models",
			availableRAM:  50.0,
			expectedCount: 6,
			shouldContain: []string{"phi3:mini", "mistral:7b", "llama3.2:3b", "llama3.1:8b", "codellama:7b", "llama3.1:70b"},
		},
		{
			name:          "zero RAM returns no models",
			availableRAM:  0,
			expectedCount: 0,
			shouldContain: nil,
		},
		{
			name:          "negative RAM returns no models",
			availableRAM:  -5.0,
			expectedCount: 0,
			shouldContain: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterByRAM(tt.availableRAM)

			if len(result) != tt.expectedCount {
				t.Errorf("expected %d models, got %d", tt.expectedCount, len(result))
			}

			for _, expectedName := range tt.shouldContain {
				found := false
				for _, m := range result {
					if m.Name == expectedName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected result to contain %s", expectedName)
				}
			}
		})
	}
}

func TestGetRecommended(t *testing.T) {
	tests := []struct {
		name         string
		availableRAM float64
		expectedName string
		expectNil    bool
	}{
		{
			name:         "low RAM returns nil",
			availableRAM: 1.0,
			expectNil:    true,
		},
		{
			name:         "2.0 GB returns phi3:mini (largest compatible)",
			availableRAM: 2.0,
			expectedName: "phi3:mini",
		},
		{
			name:         "2.5 GB returns llama3.2:3b (largest compatible, no recommended fits)",
			availableRAM: 2.5,
			expectedName: "llama3.2:3b",
		},
		{
			name:         "5.0 GB returns mistral:7b (recommended fits)",
			availableRAM: 5.0,
			expectedName: "mistral:7b",
		},
		{
			name:         "50.0 GB returns mistral:7b (recommended)",
			availableRAM: 50.0,
			expectedName: "mistral:7b",
		},
		{
			name:         "zero RAM returns nil",
			availableRAM: 0,
			expectNil:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRecommended(tt.availableRAM)

			if tt.expectNil {
				if result != nil {
					t.Errorf("expected nil, got %s", result.Name)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if result.Name != tt.expectedName {
				t.Errorf("expected %s, got %s", tt.expectedName, result.Name)
			}
		})
	}
}

func TestFormatStatus(t *testing.T) {
	tests := []struct {
		name         string
		model        Model
		availableRAM float64
		expected     string
	}{
		{
			name: "compatible and recommended",
			model: Model{
				Name:        "mistral:7b",
				RAMGB:       4.5,
				Recommended: true,
			},
			availableRAM: 5.0,
			expected:     "✅ ⭐ Recommended",
		},
		{
			name: "compatible but not recommended",
			model: Model{
				Name:        "phi3:mini",
				RAMGB:       2.0,
				Recommended: false,
			},
			availableRAM: 5.0,
			expected:     "✅ Compatible",
		},
		{
			name: "not compatible - needs more RAM",
			model: Model{
				Name:        "mistral:7b",
				RAMGB:       4.5,
				Recommended: true,
			},
			availableRAM: 2.0,
			expected:     "❌ Needs 4.5 GB (have 2.0 GB)",
		},
		{
			name: "not compatible - large model",
			model: Model{
				Name:        "llama3.1:70b",
				RAMGB:       48.0,
				Recommended: false,
			},
			availableRAM: 16.0,
			expected:     "❌ Needs 48.0 GB (have 16.0 GB)",
		},
		{
			name: "exact RAM match is compatible",
			model: Model{
				Name:        "phi3:mini",
				RAMGB:       2.0,
				Recommended: false,
			},
			availableRAM: 2.0,
			expected:     "✅ Compatible",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatStatus(tt.model, tt.availableRAM)

			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFormatStatus_ContainsExpectedParts(t *testing.T) {
	t.Run("incompatible status contains RAM values", func(t *testing.T) {
		model := Model{Name: "test", RAMGB: 10.0}
		result := FormatStatus(model, 5.0)

		if !strings.Contains(result, "10.0") {
			t.Errorf("expected result to contain required RAM '10.0', got %q", result)
		}
		if !strings.Contains(result, "5.0") {
			t.Errorf("expected result to contain available RAM '5.0', got %q", result)
		}
	})
}
