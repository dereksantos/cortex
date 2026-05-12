package journal

import "testing"

func TestFeedback_RoundTrip(t *testing.T) {
	for _, typ := range []string{
		TypeFeedbackCorrection,
		TypeFeedbackConfirmation,
		TypeFeedbackRetraction,
	} {
		p := FeedbackPayload{
			GradedOffset: 42,
			GradedID:     "dream-insight-1",
			Note:         "user said this is wrong",
			Replacement:  "use PostgreSQL not SQLite",
			Reason:       "supersedes earlier note",
			SessionID:    "sess-1",
		}
		e, err := NewFeedbackEntry(typ, p)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if e.Type != typ {
			t.Errorf("Type = %s, want %s", e.Type, typ)
		}
		if len(e.Sources) != 1 || e.Sources[0] != 42 {
			t.Errorf("Sources = %v, want [42]", e.Sources)
		}
		got, err := ParseFeedback(e)
		if err != nil {
			t.Fatalf("ParseFeedback: %v", err)
		}
		if got.GradedID != "dream-insight-1" {
			t.Errorf("GradedID = %s", got.GradedID)
		}
	}
}

func TestFeedback_RequiresGradedTarget(t *testing.T) {
	if _, err := NewFeedbackEntry(TypeFeedbackRetraction, FeedbackPayload{Note: "x"}); err == nil {
		t.Error("expected error when both GradedOffset and GradedID are empty")
	}
}

func TestFeedback_RejectsNonFeedbackType(t *testing.T) {
	if _, err := NewFeedbackEntry("capture.event", FeedbackPayload{GradedID: "x"}); err == nil {
		t.Error("expected error for non-feedback type")
	}
}

func TestParseFeedback_RejectsNonFeedbackEntry(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseFeedback(e); err == nil {
		t.Error("expected error parsing capture.event as feedback")
	}
}
