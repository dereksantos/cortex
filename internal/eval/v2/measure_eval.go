package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/measure"
	"github.com/dereksantos/cortex/pkg/llm"
	"gopkg.in/yaml.v3"
)

// MeasureScenario defines a scenario for testing Promptability correlation.
type MeasureScenario struct {
	ID      string       `yaml:"id"`
	Name    string       `yaml:"name"`
	Measure MeasureBlock `yaml:"measure"`
}

// MeasureBlock holds the measure-specific fields.
type MeasureBlock struct {
	Task          string           `yaml:"task"`
	JudgeCriteria string           `yaml:"judge_criteria"`
	Variants      []MeasureVariant `yaml:"variants"`
}

// MeasureVariant is one prompt formulation of the same task.
type MeasureVariant struct {
	ID         string   `yaml:"id"`
	Prompt     string   `yaml:"prompt,omitempty"`
	SubPrompts []string `yaml:"sub_prompts,omitempty"`
}

// MeasureVariantResult holds results for a single variant.
type MeasureVariantResult struct {
	VariantID        string  `json:"variant_id"`
	Prompt           string  `json:"prompt"`
	Promptability    float64 `json:"promptability"`
	Grade            string  `json:"grade"`
	Correctness      float64 `json:"correctness"`
	Understanding    float64 `json:"understanding"`
	Hallucination    float64 `json:"hallucination"`
	CompositeQuality float64 `json:"composite_quality"`
	TotalTokens      int     `json:"total_tokens"`
	QualityPerToken  float64 `json:"quality_per_token"`
	IsDecomposed     bool    `json:"is_decomposed"`
	SubCount         int     `json:"sub_count,omitempty"`
}

// MeasureScenarioResult holds results for all variants of a task.
type MeasureScenarioResult struct {
	ScenarioID  string                 `json:"scenario_id"`
	Name        string                 `json:"name"`
	Task        string                 `json:"task"`
	Variants    []MeasureVariantResult `json:"variants"`
	Correlation float64                `json:"correlation"`
}

// MeasureResults holds aggregate results across scenarios.
type MeasureResults struct {
	Provider           string                  `json:"provider"`
	Model              string                  `json:"model"`
	Scenarios          []MeasureScenarioResult `json:"scenarios"`
	OverallCorrelation float64                 `json:"overall_correlation"`
	DecompositionLift  float64                 `json:"decomposition_lift"`
	Pass               bool                    `json:"pass"`
}

// MeasureEvaluator runs measure scenarios.
type MeasureEvaluator struct {
	provider llm.Provider
	judge    llm.Provider
	measurer *measure.Measurer
	model    string
	verbose  bool
}

// NewMeasureEvaluator creates a new MeasureEvaluator.
func NewMeasureEvaluator(provider, judge llm.Provider) *MeasureEvaluator {
	return &MeasureEvaluator{
		provider: provider,
		judge:    judge,
		measurer: measure.New(nil), // Mechanical scoring only (no LLM needed)
	}
}

// SetVerbose enables verbose output.
func (e *MeasureEvaluator) SetVerbose(v bool) { e.verbose = v }

// SetModel sets the model name for tracking.
func (e *MeasureEvaluator) SetModel(m string) { e.model = m }

// LoadMeasureScenario reads a measure scenario from YAML.
func LoadMeasureScenario(path string) (*MeasureScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}

	var s MeasureScenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse scenario: %w", err)
	}

	if s.ID == "" {
		return nil, fmt.Errorf("scenario missing id")
	}
	if s.Measure.Task == "" {
		return nil, fmt.Errorf("scenario %s: measure.task is required", s.ID)
	}
	if len(s.Measure.Variants) < 2 {
		return nil, fmt.Errorf("scenario %s: need at least 2 variants", s.ID)
	}

	return &s, nil
}

// LoadAllMeasureScenarios reads all measure scenarios from a directory.
func LoadAllMeasureScenarios(dir string) ([]*MeasureScenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scenario dir: %w", err)
	}

	var scenarios []*MeasureScenario
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		s, err := LoadMeasureScenario(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		scenarios = append(scenarios, s)
	}

	return scenarios, nil
}

// Run executes all measure scenarios in a directory and returns results.
func (e *MeasureEvaluator) Run(dir string) (*MeasureResults, error) {
	scenarios, err := LoadAllMeasureScenarios(dir)
	if err != nil {
		return nil, err
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no measure scenarios found in %s", dir)
	}

	var results []MeasureScenarioResult
	for _, s := range scenarios {
		result, err := e.RunScenario(s)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] %s: %v\n", s.ID, err)
			}
			continue
		}
		results = append(results, *result)
	}

	return e.aggregate(results), nil
}

// RunScenario executes a single measure scenario.
func (e *MeasureEvaluator) RunScenario(s *MeasureScenario) (*MeasureScenarioResult, error) {
	if e.verbose {
		fmt.Printf("Running measure scenario: %s\n", s.ID)
	}

	ctx := context.Background()
	var variantResults []MeasureVariantResult

	for _, v := range s.Measure.Variants {
		result, err := e.runVariant(ctx, v, s.Measure.Task, s.Measure.JudgeCriteria)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] variant %s: %v\n", v.ID, err)
			}
			continue
		}
		variantResults = append(variantResults, *result)

		if e.verbose {
			fmt.Printf("  %-20s Promptability=%.2f  Quality=%.2f  Tokens=%d\n",
				v.ID, result.Promptability, result.CompositeQuality, result.TotalTokens)
		}
	}

	if len(variantResults) < 2 {
		return nil, fmt.Errorf("need at least 2 variant results for correlation")
	}

	// Compute correlation
	var promptScores, qualityScores []float64
	for _, vr := range variantResults {
		promptScores = append(promptScores, vr.Promptability)
		qualityScores = append(qualityScores, vr.CompositeQuality)
	}
	correlation := PearsonCorrelation(promptScores, qualityScores)

	if e.verbose {
		fmt.Printf("  Correlation: %.2f\n\n", correlation)
	}

	return &MeasureScenarioResult{
		ScenarioID:  s.ID,
		Name:        s.Name,
		Task:        s.Measure.Task,
		Variants:    variantResults,
		Correlation: correlation,
	}, nil
}

// runVariant measures and evaluates a single variant.
func (e *MeasureEvaluator) runVariant(ctx context.Context, v MeasureVariant, task, criteria string) (*MeasureVariantResult, error) {
	isDecomposed := len(v.SubPrompts) > 0

	var promptability float64
	var response string
	var totalTokens int
	var promptDisplay string

	if isDecomposed {
		// Measure and generate each sub-prompt independently
		var totalP float64
		var responses []string

		for _, sp := range v.SubPrompts {
			// Measure
			mResult := e.measurer.MeasureMechanical(sp)
			totalP += measure.Promptability(mResult, nil)

			// Generate
			resp, stats, err := e.provider.GenerateWithStats(ctx, sp)
			if err != nil {
				return nil, fmt.Errorf("generate sub-prompt: %w", err)
			}
			responses = append(responses, resp)
			totalTokens += stats.TotalTokens()
		}

		promptability = totalP / float64(len(v.SubPrompts))
		response = strings.Join(responses, "\n\n---\n\n")
		promptDisplay = fmt.Sprintf("[%d sub-prompts]", len(v.SubPrompts))
	} else {
		// Single prompt
		mResult := e.measurer.MeasureMechanical(v.Prompt)
		promptability = measure.Promptability(mResult, nil)

		resp, stats, err := e.provider.GenerateWithStats(ctx, v.Prompt)
		if err != nil {
			return nil, fmt.Errorf("generate: %w", err)
		}
		response = resp
		totalTokens = stats.TotalTokens()
		promptDisplay = v.Prompt
	}

	// Judge quality
	var correctness, understanding, hallucination, quality float64
	if e.judge != nil {
		judgeResult, err := ScoreWithJudgeCriteria(ctx, response, task, criteria, e.judge)
		if err != nil {
			if e.verbose {
				fmt.Printf("    [judge] error: %v\n", err)
			}
		} else {
			correctness = judgeResult.Correctness
			understanding = judgeResult.Understanding
			hallucination = judgeResult.Hallucination
			quality = CompositeQuality(judgeResult)
		}
	}

	var qualityPerToken float64
	if totalTokens > 0 {
		qualityPerToken = quality / float64(totalTokens) * 1000
	}

	return &MeasureVariantResult{
		VariantID:        v.ID,
		Prompt:           promptDisplay,
		Promptability:    promptability,
		Grade:            measure.Grade(promptability),
		Correctness:      correctness,
		Understanding:    understanding,
		Hallucination:    hallucination,
		CompositeQuality: quality,
		TotalTokens:      totalTokens,
		QualityPerToken:  qualityPerToken,
		IsDecomposed:     isDecomposed,
		SubCount:         len(v.SubPrompts),
	}, nil
}

// aggregate computes overall results across scenarios.
func (e *MeasureEvaluator) aggregate(scenarios []MeasureScenarioResult) *MeasureResults {
	if len(scenarios) == 0 {
		return &MeasureResults{
			Provider: e.provider.Name(),
			Model:    e.model,
		}
	}

	// Overall correlation across ALL data points
	var allPrompt, allQuality []float64
	var decompLiftSum float64
	decompCount := 0

	for _, s := range scenarios {
		var lowestQuality float64
		lowestSet := false
		var decompQuality float64
		hasDecomp := false

		for _, v := range s.Variants {
			allPrompt = append(allPrompt, v.Promptability)
			allQuality = append(allQuality, v.CompositeQuality)

			if v.IsDecomposed {
				decompQuality = v.CompositeQuality
				hasDecomp = true
			} else if !lowestSet || v.Promptability < lowestQuality {
				lowestQuality = v.CompositeQuality
				lowestSet = true
			}
		}

		if hasDecomp && lowestSet && lowestQuality > 0 {
			decompLiftSum += (decompQuality - lowestQuality) / lowestQuality
			decompCount++
		}
	}

	overallCorr := PearsonCorrelation(allPrompt, allQuality)

	var decompLift float64
	if decompCount > 0 {
		decompLift = decompLiftSum / float64(decompCount)
	}

	return &MeasureResults{
		Provider:           e.provider.Name(),
		Model:              e.model,
		Scenarios:          scenarios,
		OverallCorrelation: overallCorr,
		DecompositionLift:  decompLift,
		Pass:               overallCorr >= 0.7,
	}
}
