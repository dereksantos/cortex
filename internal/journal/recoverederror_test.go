package journal

import (
	"encoding/json"
	"testing"
)

func TestModelRecoveredError_RoundTrip(t *testing.T) {
	p := ModelRecoveredErrorPayload{
		Role:   "code",
		Model:  "qwen/qwen3-coder:free",
		Class:  "server",
		Status: 503,
		Detail: "stream (503): server error",
	}
	e, err := NewModelRecoveredErrorEntry(p)
	if err != nil {
		t.Fatalf("NewModelRecoveredErrorEntry: %v", err)
	}
	if e.Type != TypeModelRecoveredError {
		t.Errorf("Type = %s, want %s", e.Type, TypeModelRecoveredError)
	}
	got, err := ParseModelRecoveredError(e)
	if err != nil {
		t.Fatalf("ParseModelRecoveredError: %v", err)
	}
	if *got != p {
		t.Errorf("round trip = %+v, want %+v", *got, p)
	}
}

func TestModelRecoveredError_RequiresRoleModel(t *testing.T) {
	cases := []ModelRecoveredErrorPayload{
		{Model: "m"},          // missing Role
		{Role: "code"},        // missing Model
		{Role: "", Model: ""}, // missing both
	}
	for _, p := range cases {
		if _, err := NewModelRecoveredErrorEntry(p); err == nil {
			t.Errorf("NewModelRecoveredErrorEntry(%+v) = nil error, want an error", p)
		}
	}
}

func TestParseModelRecoveredError_RejectsWrongType(t *testing.T) {
	// The unrecovered-failure sibling is a different type: parsing a
	// model.failure entry as a recovered error must fail, so the two
	// outcomes stay tellable apart in the same class dir.
	e := &Entry{Type: TypeModelFailure, Payload: json.RawMessage(`{"role":"code","model":"m","class":"server"}`)}
	if _, err := ParseModelRecoveredError(e); err == nil {
		t.Error("expected error parsing model.failure as model.recovered_error")
	}
}
