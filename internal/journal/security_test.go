package journal

import (
	"encoding/json"
	"testing"
)

func TestSecurityTaint_RoundTrip(t *testing.T) {
	p := SecurityTaintPayload{
		Source: TaintSourceWebFetch,
		Reason: "untrusted-content marker in observation",
		TurnNo: 3,
	}
	e, err := NewSecurityTaintEntry(p)
	if err != nil {
		t.Fatalf("NewSecurityTaintEntry: %v", err)
	}
	if e.Type != TypeSecurityTaint {
		t.Errorf("Type = %s, want %s", e.Type, TypeSecurityTaint)
	}
	got, err := ParseSecurityTaint(e)
	if err != nil {
		t.Fatalf("ParseSecurityTaint: %v", err)
	}
	if *got != p {
		t.Errorf("round trip = %+v, want %+v", *got, p)
	}
}

// TestSecurityTaint_RoundTripRiskyGated pins the follow-up shape: the
// receipt that says a Risky command was gated under the taint round-trips
// with its flag set, so a reader can count "taint arrived" versus "taint
// engaged the raised bar" from the same class dir.
func TestSecurityTaint_RoundTripRiskyGated(t *testing.T) {
	p := SecurityTaintPayload{
		Source:     TaintSourceWebSearch,
		Reason:     "untrusted-content marker in observation",
		TurnNo:     9,
		RiskyGated: true,
	}
	e, err := NewSecurityTaintEntry(p)
	if err != nil {
		t.Fatalf("NewSecurityTaintEntry: %v", err)
	}
	got, err := ParseSecurityTaint(e)
	if err != nil {
		t.Fatalf("ParseSecurityTaint: %v", err)
	}
	if *got != p {
		t.Errorf("round trip = %+v, want %+v", *got, p)
	}
}

func TestSecurityTaint_RequiresSourceTurnNo(t *testing.T) {
	cases := []SecurityTaintPayload{
		{TurnNo: 1},           // missing Source
		{Source: "fetch_url"}, // TurnNo 0: outside a turn there is nothing to taint
		{Source: "web_search", TurnNo: -2},
		{},
	}
	for _, p := range cases {
		if _, err := NewSecurityTaintEntry(p); err == nil {
			t.Errorf("NewSecurityTaintEntry(%+v) = nil error, want an error", p)
		}
	}
}

func TestParseSecurityTaint_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: TypeModelFailure, Payload: json.RawMessage(`{"source":"fetch_url","turn_no":1}`)}
	if _, err := ParseSecurityTaint(e); err == nil {
		t.Error("expected error parsing model.failure as security.taint")
	}
}
