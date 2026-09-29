package journal

import (
	"testing"
)

// TestReasoningFallback_RoundTrip: a recovery.reasoning_fallback entry
// round-trips through New/Parse with the field the issue #149 telemetry
// asks for (Model — which model kept needing the fallback) and the
// per-path / clamp-state attribution the receipt carries.
func TestReasoningFallback_RoundTrip(t *testing.T) {
	in := ReasoningFallbackPayload{
		Model:             "qwen3-32b:free",
		Role:              "code",
		Path:              ReasoningFallbackPathNatural,
		Outcome:           OutcomeToolCalls,
		StopReason:        "salvaged-finalize",
		MaxTokensClamped:  false,
		SalvagedUnclamped: true,
	}
	e, err := NewReasoningFallbackEntry(in)
	if err != nil {
		t.Fatalf("NewReasoningFallbackEntry: %v", err)
	}
	if e.Type != TypeReasoningFallback {
		t.Errorf("entry type = %q, want %q", e.Type, TypeReasoningFallback)
	}
	if e.V != 1 {
		t.Errorf("entry V = %d, want 1", e.V)
	}
	got, err := ParseReasoningFallback(e)
	if err != nil {
		t.Fatalf("ParseReasoningFallback: %v", err)
	}
	if got.Model != in.Model || got.Role != in.Role || got.Path != in.Path ||
		got.Outcome != in.Outcome || got.StopReason != in.StopReason ||
		got.MaxTokensClamped != in.MaxTokensClamped || got.SalvagedUnclamped != in.SalvagedUnclamped {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, in)
	}
}

// TestReasoningFallback_RequiresModelRolePath: the three identity fields
// (Model, Role, Path) are required — a receipt without them can't answer
// "which model kept needing it" and can't be attributed to a role or a
// path.
func TestReasoningFallback_RequiresModelRolePath(t *testing.T) {
	tests := []struct {
		name string
		p    ReasoningFallbackPayload
	}{
		{name: "empty model", p: ReasoningFallbackPayload{Role: "code", Path: ReasoningFallbackPathNatural}},
		{name: "empty role", p: ReasoningFallbackPayload{Model: "m", Path: ReasoningFallbackPathNatural}},
		{name: "empty path", p: ReasoningFallbackPayload{Model: "m", Role: "code"}},
		{name: "bad path", p: ReasoningFallbackPayload{Model: "m", Role: "code", Path: "bogus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewReasoningFallbackEntry(tt.p); err == nil {
				t.Errorf("NewReasoningFallbackEntry(%+v) = nil error, want error", tt.p)
			}
		})
	}
}

// TestParseReasoningFallback_RejectsWrongType: a non-recovery entry is
// rejected by ParseReasoningFallback — the same posture the other typed
// entries (model.substitution, loop.run, study.result) take.
func TestParseReasoningFallback_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: TypeModelSubstitution, V: 1, Payload: []byte(`{}`)}
	if _, err := ParseReasoningFallback(e); err == nil {
		t.Error("ParseReasoningFallback on a model.substitution entry = nil error, want error")
	}
}
