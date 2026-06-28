package journal

import (
	"encoding/json"
	"fmt"
)

// TypeStudyResult is the entry type for one study run's always-on telemetry —
// emitted every time the study subagent runs (not just under the eval), so the
// eval reads the same record production writes. It shares the field vocabulary of
// EvalCellResultPayload for the parts that map and carries the study-specific
// discriminators as additional structured fields (never flattened into notes).
// See docs/study-subagent.md §5.
const TypeStudyResult = "study.result"

// StudyResultPayload is one study run's stats. The standard-vocabulary fields
// (model/latency_ms/tokens_*/agent_turns_total/scenario_id/task_success) let one
// query read study + session rows; the typed extension block below is the
// locate-then-read discriminator the eval scores on.
type StudyResultPayload struct {
	SchemaVersion string `json:"schema_version"`

	RunID     string `json:"run_id"`
	Timestamp string `json:"timestamp"`
	GitBranch string `json:"git_branch,omitempty"`

	ScenarioID string `json:"scenario_id"` // the probe path
	Harness    string `json:"harness"`     // "study"
	Model      string `json:"model"`
	Backend    string `json:"backend,omitempty"`

	LatencyMs       int64 `json:"latency_ms"`
	TokensIn        int   `json:"tokens_in"`
	TokensOut       int   `json:"tokens_out"`
	AgentTurnsTotal int   `json:"agent_turns_total"` // model rounds (iterations)

	TaskSuccess          bool   `json:"task_success"`
	TaskSuccessCriterion string `json:"task_success_criterion"`

	// study-specific discriminators (no home in the grid struct)
	GoalHit          float64 `json:"goal_hit"` // fraction of gold present
	StopReason       string  `json:"stop_reason"`
	FinalizeForced   bool    `json:"finalize_forced"`
	PeakOutputTokens int     `json:"peak_output_tokens"`
	MaxTokensClamped bool    `json:"max_tokens_clamped"`
	Outlines         int     `json:"outlines"`
	Greps            int     `json:"greps"`
	Reads            int     `json:"reads"`
	ToolErrs         int     `json:"tool_errs"`
	ReadBytes        int     `json:"read_bytes"`
	Bounded          bool    `json:"bounded"`

	Error string `json:"error,omitempty"`
}

// NewStudyResultEntry builds a journal entry for one study run. RunID and
// ScenarioID are the only hard requirements.
func NewStudyResultEntry(p StudyResultPayload) (*Entry, error) {
	if p.RunID == "" {
		return nil, fmt.Errorf("journal: study.result requires RunID")
	}
	if p.ScenarioID == "" {
		return nil, fmt.Errorf("journal: study.result requires ScenarioID")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal study.result: %w", err)
	}
	return &Entry{Type: TypeStudyResult, V: 1, Payload: data}, nil
}

// ParseStudyResult decodes a study.result entry's payload.
func ParseStudyResult(e *Entry) (*StudyResultPayload, error) {
	if e.Type != TypeStudyResult {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeStudyResult)
	}
	var p StudyResultPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse study.result: %w", err)
	}
	return &p, nil
}
