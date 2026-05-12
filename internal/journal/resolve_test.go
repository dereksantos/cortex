package journal

import "testing"

func TestResolveRetrieval_RoundTrip(t *testing.T) {
	p := ResolveRetrievalPayload{
		QueryText:   "auth flow",
		Decision:    "inject",
		Confidence:  0.85,
		ResultCount: 5,
		InjectedIDs: []string{"r1", "r2", "r3"},
		AvgScore:    0.7,
		MaxScore:    0.95,
		Reason:      "high relevance",
		SessionID:   "sess-1",
	}
	e, err := NewResolveRetrievalEntry(p)
	if err != nil {
		t.Fatalf("NewResolveRetrievalEntry: %v", err)
	}
	if e.Type != TypeResolveRetrieval {
		t.Errorf("Type = %s, want %s", e.Type, TypeResolveRetrieval)
	}
	got, err := ParseResolveRetrieval(e)
	if err != nil {
		t.Fatalf("ParseResolveRetrieval: %v", err)
	}
	if got.QueryText != "auth flow" || got.Decision != "inject" {
		t.Errorf("payload mismatch: %+v", got)
	}
	if len(got.InjectedIDs) != 3 {
		t.Errorf("InjectedIDs = %v, want 3 entries", got.InjectedIDs)
	}
}

func TestResolveRetrieval_RejectsEmptyDecision(t *testing.T) {
	if _, err := NewResolveRetrievalEntry(ResolveRetrievalPayload{QueryText: "x"}); err == nil {
		t.Error("expected error when Decision empty")
	}
}

func TestParseResolveRetrieval_RejectsNonResolveEntry(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseResolveRetrieval(e); err == nil {
		t.Error("expected error parsing capture.event as resolve.retrieval")
	}
}
