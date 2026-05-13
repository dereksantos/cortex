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

// TestReflectRerank_BackwardCompatNoInputContents verifies that
// ReflectRerankPayload remains forward/backward compatible: an entry
// emitted before InputContents existed still parses, and the field is
// nil rather than empty-map (omitempty contract). New entries with
// content round-trip the map.
func TestReflectRerank_BackwardCompatNoInputContents(t *testing.T) {
	t.Run("old entry without input_contents", func(t *testing.T) {
		e := &Entry{
			Type:    TypeReflectRerank,
			V:       1,
			Payload: []byte(`{"query_text":"q","input_ids":["a"],"ranked_ids":["a"]}`),
		}
		got, err := ParseReflectRerank(e)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.InputContents != nil {
			t.Errorf("InputContents = %v, want nil for old entries", got.InputContents)
		}
	})
	t.Run("new entry with input_contents round-trips", func(t *testing.T) {
		p := ReflectRerankPayload{
			QueryText:     "q",
			InputIDs:      []string{"a", "b"},
			InputContents: map[string]string{"a": "alpha", "b": "beta"},
			RankedIDs:     []string{"b", "a"},
		}
		e, err := NewReflectRerankEntry(p)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		got, err := ParseReflectRerank(e)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.InputContents["a"] != "alpha" || got.InputContents["b"] != "beta" {
			t.Errorf("InputContents=%v want {a:alpha b:beta}", got.InputContents)
		}
	})
}

func TestParseReflectRerank_RejectsNonReflectEntry(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseReflectRerank(e); err == nil {
		t.Error("expected error parsing capture.event as reflect.rerank")
	}
}
