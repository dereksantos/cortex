package journal

import "testing"

func TestEvalCellResult_RoundTrip(t *testing.T) {
	seed := int64(42)
	p := EvalCellResultPayload{
		SchemaVersion:        "1.0",
		RunID:                "run-abc-123",
		Timestamp:            "2026-05-12T01:23:45Z",
		ScenarioID:           "auth-service",
		Harness:              "aider",
		Provider:             "anthropic",
		Model:                "claude-haiku-4.5",
		ContextStrategy:      "cortex",
		CortexVersion:        "feat/journal@e0966d3",
		Seed:                 &seed,
		Temperature:          0.0,
		TokensIn:             1200,
		TokensOut:            450,
		LatencyMs:            8500,
		TestsPassed:          3,
		TestsFailed:          0,
		TaskSuccess:          true,
		TaskSuccessCriterion: "all_tests_pass",
	}
	e, err := NewEvalCellResultEntry(p)
	if err != nil {
		t.Fatalf("NewEvalCellResultEntry: %v", err)
	}
	if e.Type != TypeEvalCellResult {
		t.Errorf("Type = %s, want %s", e.Type, TypeEvalCellResult)
	}
	got, err := ParseEvalCellResult(e)
	if err != nil {
		t.Fatalf("ParseEvalCellResult: %v", err)
	}
	if got.RunID != "run-abc-123" {
		t.Errorf("RunID = %s", got.RunID)
	}
	if got.Seed == nil || *got.Seed != 42 {
		t.Errorf("Seed = %v, want pointer to 42", got.Seed)
	}
	if !got.TaskSuccess {
		t.Errorf("TaskSuccess = false, want true")
	}
}

func TestEvalCellResult_RequiresRunIDAndScenarioID(t *testing.T) {
	if _, err := NewEvalCellResultEntry(EvalCellResultPayload{ScenarioID: "x"}); err == nil {
		t.Error("expected error when RunID empty")
	}
	if _, err := NewEvalCellResultEntry(EvalCellResultPayload{RunID: "x"}); err == nil {
		t.Error("expected error when ScenarioID empty")
	}
}

func TestParseEvalCellResult_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseEvalCellResult(e); err == nil {
		t.Error("expected error parsing capture.event as eval.cell_result")
	}
}
