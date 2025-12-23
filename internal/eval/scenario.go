// Package eval provides evaluation framework for testing context injection quality
package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ScenarioType categorizes evaluation scenarios
type ScenarioType string

const (
	ScenarioLinear    ScenarioType = "linear"
	ScenarioIdiom     ScenarioType = "idiom"
	ScenarioMultiPath ScenarioType = "multi-path"
	ScenarioTemporal  ScenarioType = "temporal"
)

// Scenario represents an eval test case
type Scenario struct {
	ID           string            `yaml:"id"`
	Type         ScenarioType      `yaml:"type"`
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description,omitempty"`
	Domain       string            `yaml:"domain,omitempty"`
	ContextChain []ContextEvent    `yaml:"context_chain"`
	TestPrompts  []TestPrompt      `yaml:"test_prompts"`
	Metadata     map[string]string `yaml:"metadata,omitempty"`
}

// ContextEvent represents a captured development event in the context chain
type ContextEvent struct {
	Type      string `yaml:"type"` // decision, implementation, pattern, insight
	Content   string `yaml:"content"`
	File      string `yaml:"file,omitempty"`
	Rationale string `yaml:"rationale,omitempty"`
	Timestamp string `yaml:"timestamp,omitempty"`
}

// TestPrompt defines a test case within a scenario
type TestPrompt struct {
	ID          string      `yaml:"id"`
	Prompt      string      `yaml:"prompt"`
	GroundTruth GroundTruth `yaml:"ground_truth"`
}

// GroundTruth defines expected outcomes for scoring
type GroundTruth struct {
	MustInclude       []string `yaml:"must_include"`
	MustExclude       []string `yaml:"must_exclude"`
	CodeMustMatch     string   `yaml:"code_must_match,omitempty"`
	ConsistencyHints  []string `yaml:"consistency_hints,omitempty"`
	CompletenessHints []string `yaml:"completeness_hints,omitempty"`
}

// LoadScenario loads a single scenario from a YAML file
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario file: %w", err)
	}

	var scenario Scenario
	if err := yaml.Unmarshal(data, &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
	}

	// Validate required fields
	if scenario.ID == "" {
		return nil, fmt.Errorf("scenario missing required field: id")
	}
	if len(scenario.TestPrompts) == 0 {
		return nil, fmt.Errorf("scenario %s has no test prompts", scenario.ID)
	}

	return &scenario, nil
}

// LoadScenarios loads all scenarios from a directory (recursively)
func LoadScenarios(dir string) ([]*Scenario, error) {
	var scenarios []*Scenario

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-YAML files
		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		scenario, err := LoadScenario(path)
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", path, err)
		}

		scenarios = append(scenarios, scenario)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return scenarios, nil
}

// BuildContextString formats the context chain into a string for injection
func (s *Scenario) BuildContextString() string {
	if len(s.ContextChain) == 0 {
		return ""
	}

	var result string
	result = "# Project Context\n\n"

	for _, event := range s.ContextChain {
		result += fmt.Sprintf("## %s", capitalizeFirst(event.Type))
		if event.File != "" {
			result += fmt.Sprintf(" (%s)", event.File)
		}
		result += "\n\n"
		result += event.Content + "\n"
		if event.Rationale != "" {
			result += fmt.Sprintf("\nRationale: %s\n", event.Rationale)
		}
		result += "\n"
	}

	return result
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:] // ASCII uppercase conversion
}

// EvalRun represents a complete evaluation run
type EvalRun struct {
	ID        string       `json:"id"`
	Timestamp time.Time    `json:"timestamp"`
	Provider  string       `json:"provider"`
	Scenarios []string     `json:"scenarios"`
	Results   []EvalResult `json:"results"`
	Summary   RunSummary   `json:"summary"`
}

// EvalResult represents a single prompt evaluation
type EvalResult struct {
	ScenarioID string `json:"scenario_id"`
	PromptID   string `json:"prompt_id"`
	Prompt     string `json:"prompt"`

	// Responses
	WithCortex    Response `json:"with_cortex"`
	WithoutCortex Response `json:"without_cortex"`

	// Scores (0.0 - 1.0)
	Scores ScoreSet `json:"scores"`

	// Assertions
	Assertions []AssertionResult `json:"assertions"`

	// Verdict
	Pass   bool   `json:"pass"`
	Winner string `json:"winner"` // "cortex" | "baseline" | "tie"
}

// Response represents an LLM response
type Response struct {
	Output   string `json:"output"`
	Latency  int64  `json:"latency_ms"`
	Provider string `json:"provider"`
	Error    string `json:"error,omitempty"`
}

// ScoreSet contains all score dimensions
type ScoreSet struct {
	MustInclude float64 `json:"must_include"`
	MustExclude float64 `json:"must_exclude"`
	Overall     float64 `json:"overall"`

	// LLM-judged scores (Phase 2)
	Consistency   *float64 `json:"consistency,omitempty"`
	Completeness  *float64 `json:"completeness,omitempty"`
	Hallucination *float64 `json:"hallucination,omitempty"`
}

// AssertionResult represents a single assertion check
type AssertionResult struct {
	Type     string `json:"type"` // "must_include" | "must_exclude"
	Expected string `json:"expected"`
	Found    bool   `json:"found"`
	Pass     bool   `json:"pass"`
}

// RunSummary aggregates results across all scenarios
type RunSummary struct {
	TotalScenarios int     `json:"total_scenarios"`
	TotalPrompts   int     `json:"total_prompts"`
	PassCount      int     `json:"pass_count"`
	FailCount      int     `json:"fail_count"`
	PassRate       float64 `json:"pass_rate"`

	// A/B comparison stats
	CortexWins   int     `json:"cortex_wins"`
	BaselineWins int     `json:"baseline_wins"`
	Ties         int     `json:"ties"`
	WinRate      float64 `json:"win_rate"`

	// Score deltas (cortex - baseline)
	AvgDelta float64 `json:"avg_delta"`
}
