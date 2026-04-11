package measure

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds tunable parameters for prompt measurement.
// Loaded from .cortex/measure.json if present, otherwise defaults.
// Dream mode can write this file to auto-tune based on observed prompt→output pairs.
type Config struct {
	// Word lists (merged with defaults, not replaced)
	ExtraActionVerbs     []string `json:"extra_action_verbs,omitempty"`
	ExtraConditionals    []string `json:"extra_conditionals,omitempty"`
	ExtraConstraints     []string `json:"extra_constraints,omitempty"`
	ExtraVaguePatterns   []string `json:"extra_vague_patterns,omitempty"`
	ExtraConcernSeps     []string `json:"extra_concern_separators,omitempty"`

	// Composite score weights (must sum to 1.0)
	Weights *Weights `json:"weights,omitempty"`

	// Scope formula parameters
	Scope *ScopeParams `json:"scope,omitempty"`

	// Clarity formula parameters
	Clarity *ClarityParams `json:"clarity,omitempty"`

	// Output token estimation multipliers
	TokenEstimation *TokenEstimationParams `json:"token_estimation,omitempty"`

	// Calibration data — accumulated prompt→output observations.
	// Dream mode appends to this; enough samples trigger auto-tuning.
	Calibrations []CalibrationPoint `json:"calibrations,omitempty"`
}

// Weights controls composite Promptability score blending.
type Weights struct {
	Decomposition float64 `json:"decomposition"` // default 0.40
	Clarity       float64 `json:"clarity"`       // default 0.35
	InverseScope  float64 `json:"inverse_scope"` // default 0.25
}

// ScopeParams controls the scope score formula.
type ScopeParams struct {
	VerbWeight        float64 `json:"verb_weight"`        // default 1.0
	FileRefWeight     float64 `json:"file_ref_weight"`    // default 0.5
	ConditionalWeight float64 `json:"conditional_weight"` // default 0.3
	ConcernWeight     float64 `json:"concern_weight"`     // default 1.0
	Denominator       float64 `json:"denominator"`        // default 8.0
}

// ClarityParams controls the clarity score formula.
type ClarityParams struct {
	SpecificityWeight float64 `json:"specificity_weight"` // default 0.40
	ConstraintWeight  float64 `json:"constraint_weight"`  // default 0.25
	ExampleWeight     float64 `json:"example_weight"`     // default 0.20
	QuestionWeight    float64 `json:"question_weight"`    // default 0.15
	ConstraintCap     int     `json:"constraint_cap"`     // default 3
	QuestionCap       int     `json:"question_cap"`       // default 3
}

// TokenEstimationParams controls output token estimation.
type TokenEstimationParams struct {
	Base            int `json:"base"`             // default 100
	VerbMultiplier  int `json:"verb_multiplier"`  // default 200
	FileMultiplier  int `json:"file_multiplier"`  // default 150
	ConcernMultiplier int `json:"concern_multiplier"` // default 100
	ExampleBonus    int `json:"example_bonus"`    // default 200
}

// CalibrationPoint records an observed prompt→output pair for tuning.
type CalibrationPoint struct {
	PromptTokens       int `json:"prompt_tokens"`
	ActualOutputTokens int `json:"actual_output_tokens"`
	ActionVerbs        int `json:"action_verbs"`
	FileReferences     int `json:"file_references"`
	Concerns           int `json:"concerns"`
}

// DefaultConfig returns the default measurement config.
func DefaultConfig() *Config {
	return &Config{
		Weights: &Weights{
			Decomposition: 0.40,
			Clarity:       0.35,
			InverseScope:  0.25,
		},
		Scope: &ScopeParams{
			VerbWeight:        1.0,
			FileRefWeight:     0.5,
			ConditionalWeight: 0.3,
			ConcernWeight:     1.0,
			Denominator:       8.0,
		},
		Clarity: &ClarityParams{
			SpecificityWeight: 0.40,
			ConstraintWeight:  0.25,
			ExampleWeight:     0.20,
			QuestionWeight:    0.15,
			ConstraintCap:     3,
			QuestionCap:       3,
		},
		TokenEstimation: &TokenEstimationParams{
			Base:              100,
			VerbMultiplier:    200,
			FileMultiplier:    150,
			ConcernMultiplier: 100,
			ExampleBonus:      200,
		},
	}
}

// LoadConfig loads measurement config from .cortex/measure.json.
// Returns defaults if file doesn't exist.
func LoadConfig(contextDir string) *Config {
	cfg := DefaultConfig()

	path := filepath.Join(contextDir, "measure.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		return cfg
	}

	cfg.merge(&loaded)
	return cfg
}

// Save persists the config to .cortex/measure.json.
func (c *Config) Save(contextDir string) error {
	path := filepath.Join(contextDir, "measure.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Tune adjusts token estimation multipliers based on calibration data.
// Requires at least minSamples points. Returns true if tuning was applied.
func (c *Config) Tune(minSamples int) bool {
	if len(c.Calibrations) < minSamples {
		return false
	}

	// Simple approach: compute average actual/estimated ratio per signal
	// and scale the multipliers accordingly.
	var totalActual, totalEstimated float64
	for _, p := range c.Calibrations {
		actual := float64(p.ActualOutputTokens)
		estimated := float64(c.TokenEstimation.Base +
			p.ActionVerbs*c.TokenEstimation.VerbMultiplier +
			p.FileReferences*c.TokenEstimation.FileMultiplier +
			p.Concerns*c.TokenEstimation.ConcernMultiplier)

		totalActual += actual
		totalEstimated += estimated
	}

	if totalEstimated == 0 {
		return false
	}

	// Scale all multipliers by the ratio
	ratio := totalActual / totalEstimated
	if ratio < 0.5 || ratio > 2.0 {
		// Clamp to avoid wild swings
		ratio = clamp(ratio, 0.5, 2.0)
	}

	c.TokenEstimation.VerbMultiplier = int(float64(c.TokenEstimation.VerbMultiplier) * ratio)
	c.TokenEstimation.FileMultiplier = int(float64(c.TokenEstimation.FileMultiplier) * ratio)
	c.TokenEstimation.ConcernMultiplier = int(float64(c.TokenEstimation.ConcernMultiplier) * ratio)
	c.TokenEstimation.Base = int(float64(c.TokenEstimation.Base) * ratio)

	return true
}

// merge applies non-nil fields from other onto c.
func (c *Config) merge(other *Config) {
	// Merge extra word lists (append, don't replace)
	c.ExtraActionVerbs = append(c.ExtraActionVerbs, other.ExtraActionVerbs...)
	c.ExtraConditionals = append(c.ExtraConditionals, other.ExtraConditionals...)
	c.ExtraConstraints = append(c.ExtraConstraints, other.ExtraConstraints...)
	c.ExtraVaguePatterns = append(c.ExtraVaguePatterns, other.ExtraVaguePatterns...)
	c.ExtraConcernSeps = append(c.ExtraConcernSeps, other.ExtraConcernSeps...)

	// Override weights if provided
	if other.Weights != nil {
		c.Weights = other.Weights
	}
	if other.Scope != nil {
		c.Scope = other.Scope
	}
	if other.Clarity != nil {
		c.Clarity = other.Clarity
	}
	if other.TokenEstimation != nil {
		c.TokenEstimation = other.TokenEstimation
	}

	// Append calibrations
	c.Calibrations = append(c.Calibrations, other.Calibrations...)
}

// effectiveActionVerbs returns the default verbs plus any extras from config.
func (c *Config) effectiveActionVerbs() []string {
	if len(c.ExtraActionVerbs) == 0 {
		return actionVerbs
	}
	result := make([]string, len(actionVerbs), len(actionVerbs)+len(c.ExtraActionVerbs))
	copy(result, actionVerbs)
	return append(result, c.ExtraActionVerbs...)
}

// effectiveConditionals returns the default conditionals plus extras.
func (c *Config) effectiveConditionals() []string {
	if len(c.ExtraConditionals) == 0 {
		return conditionalWords
	}
	result := make([]string, len(conditionalWords), len(conditionalWords)+len(c.ExtraConditionals))
	copy(result, conditionalWords)
	return append(result, c.ExtraConditionals...)
}

// effectiveConstraints returns the default constraints plus extras.
func (c *Config) effectiveConstraints() []string {
	if len(c.ExtraConstraints) == 0 {
		return constraintWords
	}
	result := make([]string, len(constraintWords), len(constraintWords)+len(c.ExtraConstraints))
	copy(result, constraintWords)
	return append(result, c.ExtraConstraints...)
}

// effectiveVaguePatterns returns the default vague patterns plus extras.
func (c *Config) effectiveVaguePatterns() []string {
	if len(c.ExtraVaguePatterns) == 0 {
		return vaguePatterns
	}
	result := make([]string, len(vaguePatterns), len(vaguePatterns)+len(c.ExtraVaguePatterns))
	copy(result, vaguePatterns)
	return append(result, c.ExtraVaguePatterns...)
}

// effectiveConcernSeparators returns the default concern separators plus extras.
func (c *Config) effectiveConcernSeparators() []string {
	if len(c.ExtraConcernSeps) == 0 {
		return concernSeparators
	}
	result := make([]string, len(concernSeparators), len(concernSeparators)+len(c.ExtraConcernSeps))
	copy(result, concernSeparators)
	return append(result, c.ExtraConcernSeps...)
}
