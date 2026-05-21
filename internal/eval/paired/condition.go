//go:build !windows

// Package paired runs the same coding scenario across multiple
// model/Cortex conditions and writes a cost-quality JSONL row per
// condition. It is the Tier 2c "multi-model cost/quality delta"
// substrate from docs/eval-strategy.md: small_alone vs small + Cortex
// vs frontier_alone, paired on a shared corpus.
//
// The package is a thin orchestration layer on top of internal/eval/v2:
// CodingScenario loading, ScoreGoLFrames scoring, and the CortexHarness
// are reused as-is. Only the comparison driver and JSONL output are new.
package paired

import (
	"github.com/dereksantos/cortex/pkg/llm"
)

// Condition names one entry in the paired comparison. Three are
// canonical (small_alone, small_with_cortex, frontier_alone) but the
// shape generalizes — a caller can add or remove rows as the
// substrate ages.
//
// Model is sent verbatim to the chosen backend. When Endpoint is set,
// the harness binds to that OpenAI-compatible endpoint and Model is
// the bare model id ("Qwen3-Coder-30B-A3B-Instruct-GGUF"). When
// Endpoint is nil the harness routes through OpenRouter and Model is
// the provider-prefixed id ("anthropic/claude-3-5-haiku").
//
// UseCortex toggles the cortex_search tool registration. The
// salience-budget / DAG-dispatcher pieces are always on for the
// CortexHarness; the contrast that matters for the multi-model
// leverage claim is cortex_search availability, since that is what
// makes prior-session captures usable.
type Condition struct {
	Name      string
	Model     string
	Endpoint  *llm.EndpointConfig
	UseCortex bool
}

// Result is the JSONL row emitted per (scenario, condition). Numeric
// fields default to zero when the harness can't observe them.
type Result struct {
	SchemaVersion string  `json:"schema_version"`
	Timestamp     string  `json:"timestamp"`
	ScenarioID    string  `json:"scenario_id"`
	Condition     string  `json:"condition"`
	Model         string  `json:"model"`
	UseCortex     bool    `json:"use_cortex"`
	Endpoint      string  `json:"endpoint,omitempty"`
	TokensIn      int     `json:"tokens_in"`
	TokensOut     int     `json:"tokens_out"`
	CostUSD       float64 `json:"cost_usd"`
	LatencyMs     int64   `json:"latency_ms"`
	AgentTurns    int     `json:"agent_turns"`
	FramesPassed  int     `json:"frames_passed"`
	FramesFailed  int     `json:"frames_failed"`
	BuildOK       bool    `json:"build_ok"`
	Pass          bool    `json:"pass"`
	Notes         string  `json:"notes,omitempty"`
	Err           string  `json:"err,omitempty"`
}

// ResultSchemaVersion is bumped when the JSON shape changes
// breakingly. Additive omitempty fields can keep it at "1".
const ResultSchemaVersion = "1"
