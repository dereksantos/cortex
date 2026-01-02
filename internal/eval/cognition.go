// Cognition evals test the five cognitive modes and their interactions.
//
// # Eval Categories
//
// Mode evals test each cognitive mode in isolation:
//   - Reflex: retrieval quality, latency <10ms
//   - Reflect: rerank quality (NDCG), contradiction detection
//   - Resolve: decision accuracy (inject/wait/queue/discard)
//   - Think: SessionContext quality over time
//   - Dream: source coverage, insight quality
//
// Session evals test accumulation over multiple interactions:
//   - Does SessionContext improve over a session?
//   - Does TopicWeights reflect activity patterns?
//   - Does WarmCache predict next queries?
//
// Benefit evals measure "agentic benefits mechanical":
//   - Agentic Benefit Ratio (ABR) = quality(Fast+Think) / quality(Full)
//   - Goal: ABR → 1.0 as session progresses
//
// Pipeline evals test end-to-end retrieval:
//   - Fast mode vs Full mode quality comparison
//   - Latency budgets respected
//   - Background modes triggered appropriately
package eval

import (
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// CognitionScenarioType categorizes cognition evaluation scenarios
type CognitionScenarioType string

const (
	// CognitionMode tests a single cognitive mode in isolation
	CognitionMode CognitionScenarioType = "mode"

	// CognitionSession tests accumulation over multiple interactions
	CognitionSession CognitionScenarioType = "session"

	// CognitionBenefit measures agentic → mechanical improvement
	CognitionBenefit CognitionScenarioType = "benefit"

	// CognitionPipeline tests end-to-end retrieval
	CognitionPipeline CognitionScenarioType = "pipeline"

	// CognitionDream tests Dream source exploration
	CognitionDream CognitionScenarioType = "dream"

	// CognitionConflict tests pattern conflict detection and resolution
	// When evidence conflicts (code says X, docs say Y), tests whether the system
	// detects the conflict, assesses severity, and takes appropriate action
	CognitionConflict CognitionScenarioType = "conflict"

	// CognitionIdiom tests language/framework idiom adoption
	// Given project conventions in context, tests whether LLM responses follow them
	CognitionIdiom CognitionScenarioType = "idiom"
)

// CognitionScenario represents a cognition eval test case
type CognitionScenario struct {
	ID          string                `yaml:"id"`
	Type        CognitionScenarioType `yaml:"type"`
	Name        string                `yaml:"name"`
	Description string                `yaml:"description,omitempty"`

	// Mode eval fields
	Mode      string     `yaml:"mode,omitempty"` // reflex, reflect, resolve, think, dream
	ModeTests []ModeTest `yaml:"mode_tests,omitempty"`

	// Session eval fields
	SessionSteps []SessionStep `yaml:"session_steps,omitempty"`

	// Benefit eval fields
	BenefitQueries       []cognition.Query `yaml:"benefit_queries,omitempty"`
	ExpectedQualityGap   float64           `yaml:"expected_quality_gap,omitempty"` // Max acceptable gap
	ExpectedABRThreshold float64           `yaml:"expected_abr_threshold,omitempty"`

	// Pipeline eval fields
	PipelineTests []PipelineTest `yaml:"pipeline_tests,omitempty"`

	// Dream eval fields
	DreamSources       []string `yaml:"dream_sources,omitempty"`
	ExpectedInsights   int      `yaml:"expected_insights,omitempty"`
	ExpectedCoverage   float64  `yaml:"expected_coverage,omitempty"`
	BudgetMustRespect  bool     `yaml:"budget_must_respect,omitempty"`

	// Conflict eval fields
	ConflictTopic    string              `yaml:"conflict_topic,omitempty"`    // e.g., "testing", "error-handling"
	Evidence         []PatternEvidence   `yaml:"evidence,omitempty"`          // Evidence from different sources
	ConflictExpected ConflictExpectation `yaml:"conflict_expected,omitempty"` // Expected detection and handling

	// Idiom eval fields
	ContextChain []IdiomContext `yaml:"context_chain,omitempty"` // Context to inject
	TestPrompts  []IdiomPrompt  `yaml:"test_prompts,omitempty"`  // Prompts to test
}

// IdiomContext represents a piece of context for idiom evals
type IdiomContext struct {
	Type    string `yaml:"type"`              // pattern, implementation, code_review, decision
	Content string `yaml:"content"`           // The context content
	File    string `yaml:"file,omitempty"`    // Optional file path for implementations
}

// IdiomPrompt represents a test prompt for idiom evals
type IdiomPrompt struct {
	ID          string           `yaml:"id"`
	Prompt      string           `yaml:"prompt"`
	GroundTruth IdiomGroundTruth `yaml:"ground_truth"`
}

// IdiomGroundTruth defines expected content in LLM response
type IdiomGroundTruth struct {
	MustInclude    []string `yaml:"must_include,omitempty"`
	MustNotInclude []string `yaml:"must_not_include,omitempty"`
}

// ModeTest tests a single cognitive mode
type ModeTest struct {
	ID    string `yaml:"id"`
	Input ModeTestInput `yaml:"input"`
	Expected ModeTestExpected `yaml:"expected"`
}

// ModeTestInput contains input for a mode test
type ModeTestInput struct {
	// For Reflex/Reflect/Resolve
	Query cognition.Query `yaml:"query,omitempty"`

	// For Reflect (candidates to rerank)
	Candidates []cognition.Result `yaml:"candidates,omitempty"`

	// For Resolve (results to make decision on)
	Results []cognition.Result `yaml:"results,omitempty"`
}

// ModeTestExpected contains expected outcomes for a mode test
type ModeTestExpected struct {
	// For Reflex
	ResultIDs   []string      `yaml:"result_ids,omitempty"`   // Expected result IDs in order
	MinResults  int           `yaml:"min_results,omitempty"`  // Minimum results returned
	MaxLatency  time.Duration `yaml:"max_latency,omitempty"`  // e.g., 10ms for Reflex

	// For Reflect
	TopResultIDs        []string `yaml:"top_result_ids,omitempty"`        // Expected top results after rerank
	ContradictionsFound []string `yaml:"contradictions_found,omitempty"`  // IDs of contradicting results

	// For Resolve
	Decision   cognition.Decision `yaml:"decision,omitempty"`   // inject, wait, queue, discard
	MinConfidence float64         `yaml:"min_confidence,omitempty"`

	// For Think
	TopicWeights map[string]float64 `yaml:"topic_weights,omitempty"` // Expected learned topics

	// For Dream
	InsightsMin int `yaml:"insights_min,omitempty"`
}

// SessionStep represents one step in a session eval
type SessionStep struct {
	ID    string          `yaml:"id"`
	Query cognition.Query `yaml:"query"`

	// Expected results for this query
	ExpectedResultIDs []string `yaml:"expected_result_ids,omitempty"`

	// Expected SessionContext state after this step
	ExpectTopicWeights         map[string]float64 `yaml:"expect_topic_weights,omitempty"`
	ExpectCacheHit             bool               `yaml:"expect_cache_hit,omitempty"`
	ExpectContradictionResolved string            `yaml:"expect_contradiction_resolved,omitempty"`

	// Quality expectation relative to Full mode
	ExpectQualityVsFull string `yaml:"expect_quality_vs_full,omitempty"` // ">= 0.9", "== full", etc.
}

// PipelineTest tests end-to-end retrieval
type PipelineTest struct {
	ID    string                   `yaml:"id"`
	Query cognition.Query          `yaml:"query"`
	Mode  cognition.RetrieveMode   `yaml:"mode"` // Fast or Full

	// Expected outcomes
	ExpectedResultIDs []string          `yaml:"expected_result_ids,omitempty"`
	ExpectedDecision  cognition.Decision `yaml:"expected_decision,omitempty"`
	MaxLatency        time.Duration      `yaml:"max_latency,omitempty"`

	// Background mode expectations
	ExpectThinkTriggered bool `yaml:"expect_think_triggered,omitempty"`
	ExpectDreamTriggered bool `yaml:"expect_dream_triggered,omitempty"`
}

// CognitionEvalResult contains results from a cognition eval
type CognitionEvalResult struct {
	ScenarioID string                `json:"scenario_id"`
	Type       CognitionScenarioType `json:"type"`

	// Mode results
	ModeResults []ModeResult `json:"mode_results,omitempty"`

	// Session results
	SessionResults *SessionResult `json:"session_results,omitempty"`

	// Benefit results
	BenefitResults *BenefitResult `json:"benefit_results,omitempty"`

	// Pipeline results
	PipelineResults []PipelineResult `json:"pipeline_results,omitempty"`

	// Dream results
	DreamResults *DreamResult `json:"dream_results,omitempty"`

	// Conflict results
	ConflictResults *ConflictResult `json:"conflict_results,omitempty"`

	// Idiom results
	IdiomResults *IdiomResult `json:"idiom_results,omitempty"`

	// Overall
	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// ModeResult contains results from a single mode test
type ModeResult struct {
	TestID  string        `json:"test_id"`
	Mode    string        `json:"mode"`
	Latency time.Duration `json:"latency"`

	// Actual vs expected
	ActualResultIDs   []string           `json:"actual_result_ids,omitempty"`
	ActualDecision    cognition.Decision `json:"actual_decision,omitempty"`
	ActualConfidence  float64            `json:"actual_confidence,omitempty"`

	// Scores
	PrecisionAtK float64 `json:"precision_at_k,omitempty"` // For Reflex
	NDCG         float64 `json:"ndcg,omitempty"`           // For Reflect
	DecisionMatch bool   `json:"decision_match,omitempty"` // For Resolve
	LatencyPass   bool   `json:"latency_pass"`

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// SessionResult contains results from a session eval
type SessionResult struct {
	Steps []SessionStepResult `json:"steps"`

	// Accumulation metrics
	TopicWeightAccuracy    float64 `json:"topic_weight_accuracy"`    // How well Think learned topics
	CacheHitRate           float64 `json:"cache_hit_rate"`           // WarmCache effectiveness
	QualityImprovementRate float64 `json:"quality_improvement_rate"` // Quality over session

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// SessionStepResult contains results from one session step
type SessionStepResult struct {
	StepID string `json:"step_id"`

	// Quality metrics
	QualityScore    float64 `json:"quality_score"`
	QualityVsFull   float64 `json:"quality_vs_full"` // Ratio to Full mode quality
	CacheHit        bool    `json:"cache_hit"`

	// SessionContext state
	ActualTopicWeights  map[string]float64 `json:"actual_topic_weights,omitempty"`
	TopicWeightAccuracy float64            `json:"topic_weight_accuracy,omitempty"` // How well topics match expected

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// BenefitResult measures the Agentic Benefit Ratio
type BenefitResult struct {
	// Per-query measurements
	QueryResults []BenefitQueryResult `json:"query_results"`

	// Aggregate metrics
	InitialABR float64 `json:"initial_abr"` // ABR at start of session
	FinalABR   float64 `json:"final_abr"`   // ABR at end of session
	ABRGrowth  float64 `json:"abr_growth"`  // How much ABR improved

	// quality(Fast+Think) / quality(Full)
	// Goal: approaches 1.0 as Think runs
	AverageABR float64 `json:"average_abr"`

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// BenefitQueryResult contains ABR measurement for one query
type BenefitQueryResult struct {
	QueryIndex int `json:"query_index"`

	FastOnlyQuality  float64 `json:"fast_only_quality"`  // Fast without Think
	FastThinkQuality float64 `json:"fast_think_quality"` // Fast with Think
	FullQuality      float64 `json:"full_quality"`       // Full mode (gold standard)

	// ABR = FastThinkQuality / FullQuality
	ABR float64 `json:"abr"`
}

// PipelineResult contains results from a pipeline test
type PipelineResult struct {
	TestID  string                 `json:"test_id"`
	Mode    cognition.RetrieveMode `json:"mode"`
	Latency time.Duration          `json:"latency"`

	ActualResultIDs  []string           `json:"actual_result_ids"`
	ActualDecision   cognition.Decision `json:"actual_decision"`
	ThinkTriggered   bool               `json:"think_triggered"`
	DreamTriggered   bool               `json:"dream_triggered"`

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// DreamResult contains results from a Dream eval
type DreamResult struct {
	SourcesCovered   []string `json:"sources_covered"`
	InsightsGenerated int     `json:"insights_generated"`
	CoverageRatio    float64  `json:"coverage_ratio"` // sources explored / total sources
	BudgetRespected  bool     `json:"budget_respected"`

	// Sample insights for inspection
	SampleInsights []cognition.Result `json:"sample_insights,omitempty"`

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// PatternEvidence represents evidence for a pattern from a specific source
type PatternEvidence struct {
	Source  string  `yaml:"source"`  // "code", "claude_md", "git_history"
	Pattern string  `yaml:"pattern"` // "stdlib testing", "testify"
	Count   int     `yaml:"count"`   // How many instances found
	Weight  float64 `yaml:"weight"`  // Authority weight (explicit docs > implicit code)
}

// ConflictSeverity indicates how serious a pattern conflict is
type ConflictSeverity string

const (
	// SeverityHigh is for framework, architecture, and dependency choices
	SeverityHigh ConflictSeverity = "high"

	// SeverityMedium is for style choices with some impact
	SeverityMedium ConflictSeverity = "medium"

	// SeverityLow is for minor style preferences
	SeverityLow ConflictSeverity = "low"
)

// ConflictExpectation defines expected behavior for a conflict scenario
type ConflictExpectation struct {
	ConflictDetected bool             `yaml:"conflict_detected"` // Should conflict be detected?
	Severity         ConflictSeverity `yaml:"severity"`          // Expected severity assessment
	MustSurface      bool             `yaml:"must_surface"`      // Should ask user to decide?
	AllowedPatterns  []string         `yaml:"allowed_patterns"`  // Valid choices if resolving silently
}

// ConflictResult contains results from a conflict detection eval
type ConflictResult struct {
	// Detection
	ConflictDetected bool             `json:"conflict_detected"`
	DetectedSeverity ConflictSeverity `json:"detected_severity"`

	// Resolution
	Surfaced      bool   `json:"surfaced"`       // Did it ask user?
	ChosenPattern string `json:"chosen_pattern"` // If resolved silently, which pattern?

	// What was injected
	InjectedContent string `json:"injected_content,omitempty"`

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// IdiomResult contains results from an idiom eval
type IdiomResult struct {
	PromptResults []IdiomPromptResult `json:"prompt_results"`
	PassRate      float64             `json:"pass_rate"`

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// IdiomPromptResult contains results from a single idiom prompt test
type IdiomPromptResult struct {
	PromptID string `json:"prompt_id"`

	// What was injected as context
	ContextInjected int `json:"context_injected"` // Number of context items

	// LLM response analysis
	Response         string   `json:"response,omitempty"` // First 500 chars
	FoundIncludes    []string `json:"found_includes"`     // must_include items found
	MissingIncludes  []string `json:"missing_includes"`   // must_include items missing
	FoundExcludes    []string `json:"found_excludes"`     // must_not_include items found (bad)

	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// CognitionRunSummary aggregates cognition eval results
type CognitionRunSummary struct {
	TotalScenarios int `json:"total_scenarios"`
	PassCount      int `json:"pass_count"`
	FailCount      int `json:"fail_count"`

	// By type
	ModePassRate     float64 `json:"mode_pass_rate"`
	SessionPassRate  float64 `json:"session_pass_rate"`
	BenefitPassRate  float64 `json:"benefit_pass_rate"`
	PipelinePassRate float64 `json:"pipeline_pass_rate"`
	DreamPassRate    float64 `json:"dream_pass_rate"`
	ConflictPassRate float64 `json:"conflict_pass_rate"`
	IdiomPassRate    float64 `json:"idiom_pass_rate"`

	// Key metrics
	AverageABR           float64       `json:"average_abr"`            // Agentic Benefit Ratio
	AverageReflexLatency time.Duration `json:"average_reflex_latency"` // Should be <10ms
	ThinkEffectiveness   float64       `json:"think_effectiveness"`    // Cache hit rate
	DreamCoverage        float64       `json:"dream_coverage"`         // Source coverage
}
