// Package measure provides prompt quality measurement tools for optimizing
// small context window performance. It scores prompts on scope, clarity,
// and decomposition to predict whether a small model will produce quality output.
package measure

import (
	"context"

	"github.com/dereksantos/cortex/pkg/llm"
)

// MechanicalResult holds all fast, no-LLM metrics for a prompt.
type MechanicalResult struct {
	// Token estimation
	InputTokens           int `json:"input_tokens"`
	EstimatedOutputTokens int `json:"estimated_output_tokens"`

	// Scope signals
	FileReferences   int `json:"file_references"`
	ActionVerbCount  int `json:"action_verb_count"`
	ConditionalCount int `json:"conditional_count"`
	ConcernCount     int `json:"concern_count"`

	// Clarity signals
	SpecificityScore float64 `json:"specificity_score"`
	ConstraintCount  int     `json:"constraint_count"`
	HasExamples      bool    `json:"has_examples"`
	QuestionCount    int     `json:"question_count"`

	// Derived scores (all 0-1)
	ScopeScore         float64 `json:"scope_score"`
	ClarityScore       float64 `json:"clarity_score"`
	DecompositionScore float64 `json:"decomposition_score"`
}

// AgenticResult holds LLM-judged metrics for a prompt.
type AgenticResult struct {
	ScopeClassification string   `json:"scope_classification"`
	ScopeExplanation    string   `json:"scope_explanation"`
	ClarityScore        float64  `json:"clarity_score"`
	Ambiguities         []string `json:"ambiguities"`
	MissingConstraints  []string `json:"missing_constraints"`
	Decomposable        bool     `json:"decomposable"`
	SubTasks            []string `json:"sub_tasks"`
	IndependentSubs     int      `json:"independent_subs"`
	ContextWindowFit    float64  `json:"context_window_fit"`
	FitExplanation      string   `json:"fit_explanation"`
}

// Result combines mechanical and agentic results with a composite score.
type Result struct {
	Prompt        string            `json:"prompt"`
	Mechanical    *MechanicalResult `json:"mechanical"`
	Agentic       *AgenticResult    `json:"agentic,omitempty"`
	Promptability float64           `json:"promptability"`
	Grade         string            `json:"grade"`
}

// Measurer performs prompt quality measurement.
type Measurer struct {
	provider      llm.Provider
	contextWindow int
	verbose       bool
	config        *Config
}

// New creates a Measurer with default config. Provider can be nil for mechanical-only mode.
func New(provider llm.Provider) *Measurer {
	return &Measurer{
		provider:      provider,
		contextWindow: 8192,
		config:        DefaultConfig(),
	}
}

// NewWithConfig creates a Measurer with a specific config.
func NewWithConfig(provider llm.Provider, cfg *Config) *Measurer {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &Measurer{
		provider:      provider,
		contextWindow: 8192,
		config:        cfg,
	}
}

// SetContextWindow sets the target context window for fit scoring.
func (m *Measurer) SetContextWindow(tokens int) {
	m.contextWindow = tokens
}

// SetVerbose enables verbose output.
func (m *Measurer) SetVerbose(v bool) {
	m.verbose = v
}

// Measure performs full measurement (mechanical + agentic if provider set).
func (m *Measurer) Measure(ctx context.Context, prompt string) (*Result, error) {
	mech := m.MeasureMechanical(prompt)

	var agent *AgenticResult
	if m.provider != nil && m.provider.IsAvailable() {
		var err error
		agent, err = m.MeasureAgentic(ctx, prompt)
		if err != nil {
			// Graceful degradation: mechanical-only if agentic fails
			agent = nil
		}
	}

	score := PromptabilityWithWeights(mech, agent, m.config.Weights)
	return &Result{
		Prompt:        prompt,
		Mechanical:    mech,
		Agentic:       agent,
		Promptability: score,
		Grade:         Grade(score),
	}, nil
}

// MeasureMechanical performs only mechanical (fast) measurement.
func (m *Measurer) MeasureMechanical(prompt string) *MechanicalResult {
	cfg := m.config

	fileRefs := CountFileReferences(prompt)
	actionVerbs := countWords(prompt, cfg.effectiveActionVerbs())
	conditionals := countWords(prompt, cfg.effectiveConditionals())
	concerns := countConcerns(prompt, cfg.effectiveConcernSeparators())
	specificity := scoreSpecificity(prompt, cfg.effectiveVaguePatterns())
	constraints := countConstraints(prompt, cfg.effectiveConstraints())
	hasExamples := HasExamples(prompt)
	questions := CountQuestions(prompt)

	scope := scopeScore(actionVerbs, fileRefs, conditionals, concerns, cfg.Scope)
	clarity := clarityScore(specificity, constraints, hasExamples, questions, cfg.Clarity)
	decomp := DecompositionScore(concerns, scope, clarity)

	return &MechanicalResult{
		InputTokens:           EstimateTokens(prompt),
		EstimatedOutputTokens: estimateOutputTokens(actionVerbs, fileRefs, concerns, hasExamples, cfg.TokenEstimation),
		FileReferences:        fileRefs,
		ActionVerbCount:       actionVerbs,
		ConditionalCount:      conditionals,
		ConcernCount:          concerns,
		SpecificityScore:      specificity,
		ConstraintCount:       constraints,
		HasExamples:           hasExamples,
		QuestionCount:         questions,
		ScopeScore:            scope,
		ClarityScore:          clarity,
		DecompositionScore:    decomp,
	}
}

// MeasureAgentic performs LLM-judged measurement. Requires provider.
func (m *Measurer) MeasureAgentic(ctx context.Context, prompt string) (*AgenticResult, error) {
	if m.provider == nil {
		return nil, nil
	}
	return measureAgentic(ctx, m.provider, prompt, m.contextWindow)
}

// Promptability computes the composite 0-1 score using default weights.
func Promptability(mech *MechanicalResult, agent *AgenticResult) float64 {
	return PromptabilityWithWeights(mech, agent, DefaultConfig().Weights)
}

// PromptabilityWithWeights computes the composite 0-1 score with custom weights.
func PromptabilityWithWeights(mech *MechanicalResult, agent *AgenticResult, w *Weights) float64 {
	if mech == nil {
		return 0
	}
	if w == nil {
		w = DefaultConfig().Weights
	}

	mechComposite := mech.DecompositionScore*w.Decomposition +
		mech.ClarityScore*w.Clarity +
		(1.0-mech.ScopeScore)*w.InverseScope

	if agent == nil {
		return clamp(mechComposite, 0, 1)
	}

	// Atomicity: 1.0 if already atomic, else ratio of independent sub-tasks
	atomicity := 1.0
	if agent.Decomposable && len(agent.SubTasks) > 0 {
		atomicity = float64(agent.IndependentSubs) / float64(len(agent.SubTasks))
	}

	agenticScore := agent.ClarityScore*0.30 +
		atomicity*0.30 +
		agent.ContextWindowFit*0.40

	blended := mechComposite*0.50 + agenticScore*0.50
	return clamp(blended, 0, 1)
}

// Grade converts a 0-1 promptability score to a letter grade.
func Grade(score float64) string {
	switch {
	case score >= 0.8:
		return "A"
	case score >= 0.6:
		return "B"
	case score >= 0.4:
		return "C"
	case score >= 0.2:
		return "D"
	default:
		return "F"
	}
}

// clamp restricts a value to a range.
func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
