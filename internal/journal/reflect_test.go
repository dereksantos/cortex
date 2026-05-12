package journal

import "testing"

func TestReflectRerank_RoundTrip(t *testing.T) {
	p := ReflectRerankPayload{
		QueryText: "auth flow",
		InputIDs:  []string{"a", "b", "c"},
		RankedIDs: []string{"c", "a", "b"},
		Contradictions: []ContradictionRecord{
			{IDs: []string{"a", "b"}, Reason: "use JWT vs sessions"},
		},
		Reasoning: "c is most recent",
		SessionID: "sess-1",
	}
	e, err := NewReflectRerankEntry(p)
	if err != nil {
		t.Fatalf("NewReflectRerankEntry: %v", err)
	}
	if e.Type != TypeReflectRerank {
		t.Errorf("Type = %s, want %s", e.Type, TypeReflectRerank)
	}
	got, err := ParseReflectRerank(e)
	if err != nil {
		t.Fatalf("ParseReflectRerank: %v", err)
	}
	if got.QueryText != "auth flow" {
		t.Errorf("QueryText = %s, want auth flow", got.QueryText)
	}
	if len(got.RankedIDs) != 3 || got.RankedIDs[0] != "c" {
		t.Errorf("RankedIDs = %v, want [c a b]", got.RankedIDs)
	}
	if len(got.Contradictions) != 1 || got.Contradictions[0].Reason != "use JWT vs sessions" {
		t.Errorf("Contradictions = %+v", got.Contradictions)
	}
}

func TestReflectRerank_RejectsEmptyRequired(t *testing.T) {
	if _, err := NewReflectRerankEntry(ReflectRerankPayload{RankedIDs: []string{"x"}}); err == nil {
		t.Error("expected error when QueryText empty")
	}
	if _, err := NewReflectRerankEntry(ReflectRerankPayload{QueryText: "x"}); err == nil {
		t.Error("expected error when RankedIDs empty")
	}
}

func TestParseReflectRerank_RejectsNonReflectEntry(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseReflectRerank(e); err == nil {
		t.Error("expected error parsing capture.event as reflect.rerank")
	}
}
