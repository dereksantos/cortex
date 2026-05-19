// CellResult is the per-cell output schema for the eval grid:
// {cortex (+ claude_cli reference)} × {small | medium | large via
// OpenRouter / Ollama / Anthropic} × {baseline | cortex | frontier}.
//
// One CellResult is emitted per (Scenario × Session × Harness × Provider ×
// Model × ContextStrategy × Seed) cell. Aggregations (ABR, lift, win-rate,
// cost-per-success) are computed downstream from streams of CellResult.
//
// This struct is the contract the eval-harness loop iterates against:
// schema changes require explicit user signoff before they land. Adding an
// optional field with `omitempty` is non-breaking and keeps SchemaVersion
// at "1"; renaming or removing a field requires bumping it.
package eval

import (
	"errors"
	"fmt"
)

// CellResultSchemaVersion is bumped on breaking changes to CellResult's
// JSON shape. Persisters and downstream rollups branch on this.
const CellResultSchemaVersion = "1"

// Harness names. Cortex is its own harness; claude_cli is retained as a
// frontier-reference name used by the agentic comparator (audit B will fold
// it into a --model knob).
const (
	HarnessClaudeCLI = "claude_cli"
	HarnessCortex    = "cortex" // Cortex's own LLM-driven agent loop (internal/harness)
)

// Provider names. Align with pkg/llm provider IDs where possible.
const (
	ProviderOpenRouter = "openrouter"
	ProviderOllama     = "ollama"
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderLocal      = "local"
)

// ContextStrategy values. Maps 1:1 to v2 LibraryServiceCondition.
//
// `cortex` is the strategy when the harness only exposes Fast mode (the
// historical default — what every Cortex cell wrote before ABR session
// runs existed). `cortex-fast` and `cortex-full` are the explicit split
// used by ABR runs where the *same* prompt sequence is replayed under
// each retrieval mode so the score ratio can be computed. Downstream
// analytics that want "any cortex cell" should match all three.
const (
	StrategyBaseline   = "baseline"
	StrategyCortex     = "cortex"
	StrategyCortexFast = "cortex-fast"
	StrategyCortexFull = "cortex-full"
	StrategyFrontier   = "frontier"
)

// IsCortexStrategy reports whether the strategy is any of the cortex
// variants. Use this in validation / analytics rather than equality
// against StrategyCortex alone, which would miss the ABR-split rows.
func IsCortexStrategy(s string) bool {
	return s == StrategyCortex || s == StrategyCortexFast || s == StrategyCortexFull
}

// TaskSuccessCriterion qualifies what TaskSuccess actually means for a row.
// A row's bool is meaningless without it — different harnesses + scenarios
// disagree on what "success" implies.
const (
	CriterionTestsPassAll      = "tests_pass_all"
	CriterionScenarioAssertion = "scenario_assertion"
	CriterionJudgeLLM          = "judge_llm"
	CriterionMixed             = "mixed"
)

// CellResult is one row of the eval grid.
type CellResult struct {
	SchemaVersion string `json:"schema_version"`

	// Identity + audit trail.
	RunID        string `json:"run_id"`    // unique per cell run (ULID/UUID)
	Timestamp    string `json:"timestamp"` // RFC3339
	GitCommitSHA string `json:"git_commit_sha,omitempty"`
	GitBranch    string `json:"git_branch,omitempty"`

	// Grid dimensions.
	ScenarioID      string `json:"scenario_id"`
	SessionID       string `json:"session_id,omitempty"` // for multi-session scenarios (library-service) and ABR multi-turn sessions
	TurnIndex       *int   `json:"turn_index,omitempty"` // 0-based position within a multi-turn session; nil for single-shot cells. Pointer to distinguish "turn 0" from "not a session".
	Benchmark       string `json:"benchmark,omitempty"`  // dataset-driven eval family: longmemeval | mteb | swebench | niah; empty for hand-authored scenarios
	Harness         string `json:"harness"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`             // provider-qualified, e.g. "openrouter/anthropic/claude-3-5-haiku"
	Backend         string `json:"backend,omitempty"` // local-only: cuda | vulkan | metal | cpu
	ContextStrategy string `json:"context_strategy"`
	CortexVersion   string `json:"cortex_version,omitempty"` // required when ContextStrategy is any cortex variant (StrategyCortex / StrategyCortexFast / StrategyCortexFull)

	// Determinism.
	Seed        *int64  `json:"seed,omitempty"` // pointer to distinguish unset from 0
	Temperature float64 `json:"temperature"`

	// Resource consumption.
	TokensIn              int     `json:"tokens_in"`
	TokensOut             int     `json:"tokens_out"`
	InjectedContextTokens int     `json:"injected_context_tokens"` // subset of TokensIn attributable to cortex injection; must be 0 unless ContextStrategy == cortex
	CostUSD               float64 `json:"cost_usd"`
	LatencyMs             int64   `json:"latency_ms"`

	// Behavioral.
	AgentTurnsTotal      int    `json:"agent_turns_total"`
	CorrectionTurns      int    `json:"correction_turns"`
	TestsPassed          int    `json:"tests_passed"`
	TestsFailed          int    `json:"tests_failed"`
	TaskSuccess          bool   `json:"task_success"`
	TaskSuccessCriterion string `json:"task_success_criterion"`

	Notes string `json:"notes,omitempty"`

	// Deliberately deferred until a deterministic detector exists:
	//   Hallucinations int `json:"hallucinations"`
}

// Validate enforces required fields and enum membership. Persistence
// callers should fail-closed: never insert a row that does not validate.
func (r *CellResult) Validate() error {
	if r == nil {
		return errors.New("nil CellResult")
	}
	if r.SchemaVersion != CellResultSchemaVersion {
		return fmt.Errorf("schema_version: want %q, got %q", CellResultSchemaVersion, r.SchemaVersion)
	}
	if r.RunID == "" {
		return errors.New("run_id is required")
	}
	if r.Timestamp == "" {
		return errors.New("timestamp is required")
	}
	if r.ScenarioID == "" {
		return errors.New("scenario_id is required")
	}
	if r.Model == "" {
		return errors.New("model is required")
	}

	switch r.Harness {
	case HarnessClaudeCLI, HarnessCortex:
	default:
		return fmt.Errorf("unknown harness: %q", r.Harness)
	}
	switch r.Provider {
	case ProviderOpenRouter, ProviderOllama, ProviderAnthropic, ProviderOpenAI, ProviderLocal:
	default:
		return fmt.Errorf("unknown provider: %q", r.Provider)
	}
	switch r.ContextStrategy {
	case StrategyBaseline, StrategyCortex, StrategyCortexFast, StrategyCortexFull, StrategyFrontier:
	default:
		return fmt.Errorf("unknown context_strategy: %q", r.ContextStrategy)
	}
	if IsCortexStrategy(r.ContextStrategy) && r.CortexVersion == "" {
		return fmt.Errorf("cortex_version required when context_strategy=%q", r.ContextStrategy)
	}
	if r.InjectedContextTokens > 0 && !IsCortexStrategy(r.ContextStrategy) {
		return fmt.Errorf("injected_context_tokens=%d but context_strategy=%q (only cortex strategies may inject)", r.InjectedContextTokens, r.ContextStrategy)
	}
	if r.TurnIndex != nil {
		if *r.TurnIndex < 0 {
			return fmt.Errorf("turn_index must be non-negative, got %d", *r.TurnIndex)
		}
		if r.SessionID == "" {
			return errors.New("turn_index requires session_id (a turn without a session is unscored)")
		}
	}
	if r.InjectedContextTokens > r.TokensIn {
		return fmt.Errorf("injected_context_tokens=%d exceeds tokens_in=%d", r.InjectedContextTokens, r.TokensIn)
	}
	switch r.TaskSuccessCriterion {
	case CriterionTestsPassAll, CriterionScenarioAssertion, CriterionJudgeLLM, CriterionMixed:
	default:
		return fmt.Errorf("unknown task_success_criterion: %q", r.TaskSuccessCriterion)
	}
	if r.LatencyMs < 0 {
		return fmt.Errorf("latency_ms must be non-negative, got %d", r.LatencyMs)
	}
	if r.CostUSD < 0 {
		return fmt.Errorf("cost_usd must be non-negative, got %f", r.CostUSD)
	}
	return nil
}
