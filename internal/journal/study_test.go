package journal

import (
	"strings"
	"testing"
)

func TestStudyResult_RoundTrip(t *testing.T) {
	p := StudyResultPayload{
		EvalCellResultPayload: EvalCellResultPayload{
			SchemaVersion:        "1",
			RunID:                "study-20260718-000000",
			Timestamp:            "2026-07-18T00:00:00Z",
			ScenarioID:           "pkg/llm",
			Harness:              "study",
			Model:                "qwen3-coder-next",
			TokensIn:             1200,
			TokensOut:            340,
			LatencyMs:            8500,
			AgentTurnsTotal:      4,
			TaskSuccess:          true,
			TaskSuccessCriterion: "goal_hit_bounded_clean_finalize",
			Thinking:             "off",
			ReasoningTokens:      128,
		},
		Rep:              0,
		GoalHit:          1.0,
		StopReason:       "clean-finalize",
		FinalizeForced:   false,
		Outlines:         1,
		Greps:            2,
		Reads:            1,
		ToolErrs:         0,
		ReadBytes:        4096,
		Bounded:          true,
		PeakOutputTokens: 340,
		MaxTokensClamped: false,
	}
	e, err := NewStudyResultEntry(p)
	if err != nil {
		t.Fatalf("NewStudyResultEntry: %v", err)
	}
	if e.Type != TypeStudyResult {
		t.Errorf("Type = %s, want %s", e.Type, TypeStudyResult)
	}
	got, err := ParseStudyResult(e)
	if err != nil {
		t.Fatalf("ParseStudyResult: %v", err)
	}
	// Shared vocabulary, read straight off the embedded payload.
	if got.RunID != "study-20260718-000000" {
		t.Errorf("RunID = %s", got.RunID)
	}
	if got.ScenarioID != "pkg/llm" {
		t.Errorf("ScenarioID = %s", got.ScenarioID)
	}
	if got.Model != "qwen3-coder-next" {
		t.Errorf("Model = %s", got.Model)
	}
	if !got.TaskSuccess {
		t.Errorf("TaskSuccess = false, want true")
	}
	if got.Thinking != "off" || got.ReasoningTokens != 128 {
		t.Errorf("Thinking/ReasoningTokens = %q/%d, want off/128", got.Thinking, got.ReasoningTokens)
	}
	// Study-specific extension block.
	if got.GoalHit != 1.0 {
		t.Errorf("GoalHit = %v, want 1.0", got.GoalHit)
	}
	if got.StopReason != "clean-finalize" {
		t.Errorf("StopReason = %s", got.StopReason)
	}
	if got.Greps != 2 || got.Reads != 1 || got.Outlines != 1 {
		t.Errorf("tool counts = outlines=%d greps=%d reads=%d", got.Outlines, got.Greps, got.Reads)
	}
	if !got.Bounded {
		t.Errorf("Bounded = false, want true")
	}
}

func TestStudyResult_RequiresRunIDAndScenarioID(t *testing.T) {
	if _, err := NewStudyResultEntry(StudyResultPayload{
		EvalCellResultPayload: EvalCellResultPayload{ScenarioID: "x"},
	}); err == nil {
		t.Error("expected error when RunID empty")
	}
	if _, err := NewStudyResultEntry(StudyResultPayload{
		EvalCellResultPayload: EvalCellResultPayload{RunID: "x"},
	}); err == nil {
		t.Error("expected error when ScenarioID empty")
	}
}

func TestParseStudyResult_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseStudyResult(e); err == nil {
		t.Error("expected error parsing capture.event as study.result")
	}
}

// A study.result row's shared fields must marshal under the SAME json tags
// as an eval.cell_result row — that's the whole point of embedding rather
// than duplicating the vocabulary.
func TestStudyResult_SharesEvalVocabularyTags(t *testing.T) {
	p := StudyResultPayload{EvalCellResultPayload: EvalCellResultPayload{
		RunID: "x", ScenarioID: "y", Model: "m",
	}}
	e, err := NewStudyResultEntry(p)
	if err != nil {
		t.Fatalf("NewStudyResultEntry: %v", err)
	}
	payload := string(e.Payload)
	for _, tag := range []string{`"model":"m"`, `"scenario_id":"y"`, `"run_id":"x"`} {
		if !strings.Contains(payload, tag) {
			t.Errorf("payload %s missing tag %s", payload, tag)
		}
	}
}
