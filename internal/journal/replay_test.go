package journal

import (
	"reflect"
	"testing"
)

func TestReplayCounterfactual_RoundTrip(t *testing.T) {
	temp := 0.0
	p := ReplayCounterfactualPayload{
		SourceOffset: 42,
		SourceClass:  "reflect",
		SourceType:   TypeReflectRerank,
		Overrides: CounterfactualOverrides{
			Model:       "claude-haiku-4.5",
			Temperature: &temp,
		},
		Status:                  ReplayStatusExecuted,
		OriginalRankedIDs:       []string{"a", "b", "c"},
		CounterfactualRankedIDs: []string{"b", "a", "c"},
		JaccardTopK:             1.0,
		JaccardK:                3,
	}
	e, err := NewReplayCounterfactualEntry(p)
	if err != nil {
		t.Fatalf("NewReplayCounterfactualEntry: %v", err)
	}
	if e.Type != TypeReplayCounterfactual {
		t.Errorf("Type=%s want %s", e.Type, TypeReplayCounterfactual)
	}
	if len(e.Sources) != 1 || e.Sources[0] != 42 {
		t.Errorf("Sources=%v want [42]", e.Sources)
	}
	got, err := ParseReplayCounterfactual(e)
	if err != nil {
		t.Fatalf("ParseReplayCounterfactual: %v", err)
	}
	if !reflect.DeepEqual(got, &p) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, &p)
	}
}

func TestReplayCounterfactual_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		p    ReplayCounterfactualPayload
		want string
	}{
		{"zero source offset", ReplayCounterfactualPayload{SourceClass: "reflect", Status: ReplayStatusPlanned}, "SourceOffset"},
		{"empty source class", ReplayCounterfactualPayload{SourceOffset: 1, Status: ReplayStatusPlanned}, "SourceClass"},
		{"unknown status", ReplayCounterfactualPayload{SourceOffset: 1, SourceClass: "x", Status: "wat"}, "Status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewReplayCounterfactualEntry(tc.p); err == nil {
				t.Fatalf("want error containing %q", tc.want)
			}
		})
	}
}

func TestParseReplayCounterfactual_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseReplayCounterfactual(e); err == nil {
		t.Error("want error parsing capture.event as replay.counterfactual")
	}
}
