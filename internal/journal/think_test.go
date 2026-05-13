package journal

import "testing"

func TestThinkSessionContext_RoundTrip(t *testing.T) {
	p := ThinkSessionContextPayload{
		TopicWeights:  map[string]float64{"auth": 0.8, "db": 0.3},
		RecentQueries: []string{"how does auth work"},
		CachedQueries: []string{"auth flow"},
		SessionID:     "sess-1",
	}
	e, err := NewThinkSessionContextEntry(p)
	if err != nil {
		t.Fatalf("NewThinkSessionContextEntry: %v", err)
	}
	if e.Type != TypeThinkSessionContext {
		t.Errorf("Type = %s, want %s", e.Type, TypeThinkSessionContext)
	}
	got, err := ParseThinkSessionContext(e)
	if err != nil {
		t.Fatalf("ParseThinkSessionContext: %v", err)
	}
	if got.TopicWeights["auth"] != 0.8 {
		t.Errorf("TopicWeights[auth] = %v, want 0.8", got.TopicWeights["auth"])
	}
}

func TestThinkTopicWeight_RoundTrip(t *testing.T) {
	p := ThinkTopicWeightPayload{Topic: "auth", Weight: 0.9, SessionID: "s"}
	e, err := NewThinkTopicWeightEntry(p)
	if err != nil {
		t.Fatalf("NewThinkTopicWeightEntry: %v", err)
	}
	got, err := ParseThinkTopicWeight(e)
	if err != nil {
		t.Fatalf("ParseThinkTopicWeight: %v", err)
	}
	if got.Topic != "auth" || got.Weight != 0.9 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestThink_RejectsBadPayloads(t *testing.T) {
	if _, err := NewThinkTopicWeightEntry(ThinkTopicWeightPayload{}); err == nil {
		t.Error("expected error when Topic empty")
	}
}

func TestParseThink_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseThinkSessionContext(e); err == nil {
		t.Error("expected error parsing capture.event as think.session_context")
	}
	if _, err := ParseThinkTopicWeight(e); err == nil {
		t.Error("expected error parsing capture.event as think.topic_weight")
	}
}
