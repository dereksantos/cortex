package measure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Weights == nil {
		t.Fatal("Weights should not be nil")
	}
	sum := cfg.Weights.Decomposition + cfg.Weights.Clarity + cfg.Weights.InverseScope
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("Weights sum = %.2f, want 1.0", sum)
	}

	if cfg.Scope == nil {
		t.Fatal("Scope should not be nil")
	}
	if cfg.Clarity == nil {
		t.Fatal("Clarity should not be nil")
	}
	if cfg.TokenEstimation == nil {
		t.Fatal("TokenEstimation should not be nil")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	cfg := LoadConfig("/nonexistent/path")
	if cfg == nil {
		t.Fatal("LoadConfig should return defaults for missing file")
	}
	if cfg.Weights.Decomposition != 0.35 {
		t.Errorf("Expected default decomposition weight 0.35, got %.2f", cfg.Weights.Decomposition)
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()

	// Write a custom config
	configJSON := `{
		"extra_action_verbs": ["deploy", "provision"],
		"weights": {"decomposition": 0.50, "clarity": 0.30, "inverse_scope": 0.20},
		"scope": {"verb_weight": 1.5, "file_ref_weight": 0.5, "conditional_weight": 0.3, "concern_weight": 1.0, "denominator": 10.0}
	}`
	if err := os.WriteFile(filepath.Join(dir, "measure.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(dir)

	// Check custom weights loaded
	if cfg.Weights.Decomposition != 0.50 {
		t.Errorf("Decomposition = %.2f, want 0.50", cfg.Weights.Decomposition)
	}

	// Check extra verbs merged
	verbs := cfg.effectiveActionVerbs()
	found := false
	for _, v := range verbs {
		if v == "deploy" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'deploy' in effective action verbs")
	}

	// Check scope params overridden
	if cfg.Scope.VerbWeight != 1.5 {
		t.Errorf("VerbWeight = %.2f, want 1.5", cfg.Scope.VerbWeight)
	}
	if cfg.Scope.Denominator != 10.0 {
		t.Errorf("Denominator = %.2f, want 10.0", cfg.Scope.Denominator)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.ExtraActionVerbs = []string{"containerize"}
	cfg.Calibrations = []CalibrationPoint{
		{PromptTokens: 10, ActualOutputTokens: 400, ActionVerbs: 1, FileReferences: 1, Concerns: 1},
	}

	if err := cfg.Save(dir); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded := LoadConfig(dir)
	if len(loaded.ExtraActionVerbs) != 1 || loaded.ExtraActionVerbs[0] != "containerize" {
		t.Errorf("ExtraActionVerbs = %v, want [containerize]", loaded.ExtraActionVerbs)
	}
	if len(loaded.Calibrations) != 1 {
		t.Errorf("Calibrations count = %d, want 1", len(loaded.Calibrations))
	}
}

func TestTune(t *testing.T) {
	cfg := DefaultConfig()

	// Not enough samples
	cfg.Calibrations = []CalibrationPoint{
		{ActualOutputTokens: 200, ActionVerbs: 1, FileReferences: 0, Concerns: 1},
	}
	if cfg.Tune(5) {
		t.Error("Tune() should return false with < 5 samples")
	}

	// Add enough samples where actual is ~half of estimated
	for i := 0; i < 5; i++ {
		cfg.Calibrations = append(cfg.Calibrations, CalibrationPoint{
			ActualOutputTokens: 200,
			ActionVerbs:        1,
			FileReferences:     0,
			Concerns:           1,
		})
	}

	origVerbMult := cfg.TokenEstimation.VerbMultiplier
	if !cfg.Tune(5) {
		t.Fatal("Tune() should return true with >= 5 samples")
	}

	// Multipliers should have changed
	if cfg.TokenEstimation.VerbMultiplier == origVerbMult {
		t.Error("VerbMultiplier should change after tuning")
	}
}

func TestConfigDrivenMeasurer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ExtraActionVerbs = []string{"deploy"}

	m := NewWithConfig(nil, cfg)
	result := m.MeasureMechanical("Deploy the new service")

	if result.ActionVerbCount != 1 {
		t.Errorf("ActionVerbCount = %d, want 1 (deploy is custom verb)", result.ActionVerbCount)
	}
}

func TestCustomWeightsAffectScore(t *testing.T) {
	// Default config
	m1 := New(nil)
	r1, _ := m1.Measure(context.TODO(), "Fix the bug in the code somewhere")

	// Config that heavily penalizes scope
	cfg := DefaultConfig()
	cfg.Weights = &Weights{
		Decomposition: 0.10,
		Clarity:       0.10,
		InverseScope:  0.80, // Heavily weight scope
	}
	m2 := NewWithConfig(nil, cfg)
	r2, _ := m2.Measure(context.TODO(), "Fix the bug in the code somewhere")

	// Scores should differ
	if r1.Promptability == r2.Promptability {
		t.Error("Custom weights should produce different scores")
	}
}
