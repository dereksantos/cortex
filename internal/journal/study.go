package journal

import (
	"encoding/json"
	"fmt"
)

// TypeStudyResult is the entry type for one study-eval probe rep, written
// as a study.result entry into .cortex/journal/study/. See
// docs/study-subagent.md §5's alignment plan: the shared eval vocabulary
// (model, tokens, latency, task_success, …) lives in the embedded
// EvalCellResultPayload so one query schema reads study + session rows;
// the fields with no home in that grid struct (stop-reason, per-tool
// counts, bytes, the fractional goal-hit, …) are the typed extension block
// below — never flattened into Notes.
const TypeStudyResult = "study.result"

// StudyResultPayload is one study probe rep's result. EvalCellResultPayload
// carries the shared vocabulary (Model, LatencyMs, TokensIn/TokensOut,
// AgentTurnsTotal for Iterations, ScenarioID for the probe path,
// TaskSuccess/TaskSuccessCriterion for the pass verdict, Thinking/
// ReasoningTokens, …); it's embedded (not referenced) so its json tags
// promote into one flat object — a study.result row and an eval.cell_result
// row read under the same field names wherever they overlap.
type StudyResultPayload struct {
	EvalCellResultPayload

	Rep int `json:"rep"`

	// GoalHit is the fractional gold-fact coverage (0..1) countGoalHits
	// produced for this rep; TaskSuccess is the derived pass/fail bool
	// (goal-hit AND bounded AND clean-finalize-or-salvaged) and belongs on
	// the embedded payload above.
	GoalHit float64 `json:"goal_hit"`

	// Termination — which bound (if any) forced the run to stop.
	StopReason     string `json:"stop_reason"`
	FinalizeForced bool   `json:"finalize_forced"`

	// Per-tool counts — the locate-then-read discriminator.
	Outlines int `json:"outlines"`
	Greps    int `json:"greps"`
	Reads    int `json:"reads"`
	ToolErrs int `json:"tool_errs"`

	ReadBytes int  `json:"read_bytes"`
	Bounded   bool `json:"bounded"`

	PeakOutputTokens int  `json:"peak_output_tokens"`
	MaxTokensClamped bool `json:"max_tokens_clamped"`
	Salvaged         bool `json:"salvaged,omitempty"`

	// Error carries the run error (if any) for a failed rep; empty on a
	// normal completion, scored or not.
	Error string `json:"error,omitempty"`
}

// NewStudyResultEntry builds a journal entry for one study.result rep.
// Reuses EvalCellResultPayload's own RunID/ScenarioID requirement so a
// study.result row can't land without the identifiers the eval.cell_result
// reader already depends on.
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
