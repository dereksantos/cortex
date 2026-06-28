package journal

import (
	"encoding/json"
	"fmt"
)

// TypeEvalCellResult is the entry type for one cell result produced by
// the eval grid (one row per scenario × harness × provider × model ×
// context_strategy × cell).
const TypeEvalCellResult = "eval.cell_result"

// EvalCellResultPayload is the canonical structured shape of an eval
// result, written as an eval.cell_result entry into
// .cortex/journal/eval/. It originally mirrored the internal/eval/v2
// grid framework's CellResult; that framework has since been removed,
// so this journal struct is now the de-facto canonical eval-result
// shape. cmd/loop's emitSessionMetrics writes one per REPL session, and
// the study eval emits an aligned study.result sharing this field
// vocabulary (see docs/study-subagent.md §5).
type EvalCellResultPayload struct {
	SchemaVersion string `json:"schema_version"`

	RunID        string `json:"run_id"`
	Timestamp    string `json:"timestamp"`
	GitCommitSHA string `json:"git_commit_sha,omitempty"`
	GitBranch    string `json:"git_branch,omitempty"`

	ScenarioID      string `json:"scenario_id"`
	SessionID       string `json:"session_id,omitempty"`
	Benchmark       string `json:"benchmark,omitempty"`
	Harness         string `json:"harness"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Backend         string `json:"backend,omitempty"`
	ContextStrategy string `json:"context_strategy"`
	CortexVersion   string `json:"cortex_version,omitempty"`

	Seed        *int64  `json:"seed,omitempty"`
	Temperature float64 `json:"temperature"`

	TokensIn              int     `json:"tokens_in"`
	TokensOut             int     `json:"tokens_out"`
	InjectedContextTokens int     `json:"injected_context_tokens"`
	CostUSD               float64 `json:"cost_usd"`
	LatencyMs             int64   `json:"latency_ms"`

	AgentTurnsTotal      int    `json:"agent_turns_total"`
	CorrectionTurns      int    `json:"correction_turns"`
	TestsPassed          int    `json:"tests_passed"`
	TestsFailed          int    `json:"tests_failed"`
	TaskSuccess          bool   `json:"task_success"`
	TaskSuccessCriterion string `json:"task_success_criterion"`

	Notes string `json:"notes,omitempty"`
}

// NewEvalCellResultEntry builds a journal entry for one cell result. The
// caller is responsible for populating a coherent payload (RunID and
// ScenarioID are the only hard requirements); the journal does not
// re-validate beyond those to keep the package dependency-light.
func NewEvalCellResultEntry(p EvalCellResultPayload) (*Entry, error) {
	if p.RunID == "" {
		return nil, fmt.Errorf("journal: eval.cell_result requires RunID")
	}
	if p.ScenarioID == "" {
		return nil, fmt.Errorf("journal: eval.cell_result requires ScenarioID")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal eval.cell_result: %w", err)
	}
	return &Entry{Type: TypeEvalCellResult, V: 1, Payload: data}, nil
}

// ParseEvalCellResult decodes an eval.cell_result entry's payload.
func ParseEvalCellResult(e *Entry) (*EvalCellResultPayload, error) {
	if e.Type != TypeEvalCellResult {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeEvalCellResult)
	}
	var p EvalCellResultPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse eval.cell_result: %w", err)
	}
	return &p, nil
}
