package journal

import (
	"encoding/json"
	"fmt"
)

// TypeEvalCellResult is the entry type for one cell result produced by
// the eval grid (one row per scenario × harness × provider × model ×
// context_strategy × cell).
const TypeEvalCellResult = "eval.cell_result"

// EvalCellResultPayload is the journal-native shape of an eval cell
// result. The fields mirror internal/eval/v2.CellResult exactly so a
// downstream consumer (the eval projector, future eval tooling) can read
// the payload as-is without translation.
//
// Future direction: internal/eval/v2/persist*.go currently writes both
// SQLite (fast queries) and a companion <dbDir>/cell_results.jsonl
// (portable analysis). E2 lands the journal entry + projector
// alongside; a follow-up unifies the storage layer once eval tooling
// is updated to read from journal/eval/cell_result/.
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
// caller is responsible for ensuring the payload validates against the
// eval-harness contract (internal/eval/v2.CellResult.Validate); the
// journal does not re-validate to keep the package dependency-light.
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
