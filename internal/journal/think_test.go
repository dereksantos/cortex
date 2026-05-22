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
	if _, err := NewThinkSessionSummaryEntry(ThinkSessionSummaryPayload{}); err == nil {
		t.Error("expected error when session_summary SessionID empty")
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
	if _, err := ParseThinkSessionSummary(e); err == nil {
		t.Error("expected error parsing capture.event as think.session_summary")
	}
}

// TestThinkSessionSummary_RoundTrip pins the per-turn rolling summary
// shape — what the REPL emits at finalize and what priorMessagesForHarness
// reads back on the next turn. Fields must survive a marshal/parse cycle
// without loss.
func TestThinkSessionSummary_RoundTrip(t *testing.T) {
	p := ThinkSessionSummaryPayload{
		SessionID:        "20260521T123456Z",
		Turn:             3,
		UserPrompt:       "tell me about this codebase",
		Summary:          "User asked for codebase overview. Listed root, read README + main.go. Answer: Go DAG-based learning harness.",
		FilesChanged:     nil,
		VerifyKind:       "none",
		VerifyOK:         true,
		OrigTokens:       2400,
		KeptTokens:       240,
		CompressOp:       "attend.compress",
		Intent:           "review",
		IntentConfidence: 0.88,
	}
	e, err := NewThinkSessionSummaryEntry(p)
	if err != nil {
		t.Fatalf("NewThinkSessionSummaryEntry: %v", err)
	}
	if e.Type != TypeThinkSessionSummary {
		t.Errorf("Type = %s, want %s", e.Type, TypeThinkSessionSummary)
	}
	got, err := ParseThinkSessionSummary(e)
	if err != nil {
		t.Fatalf("ParseThinkSessionSummary: %v", err)
	}
	if got.SessionID != p.SessionID || got.Turn != p.Turn {
		t.Errorf("identity lost: %+v", got)
	}
	if got.Summary != p.Summary {
		t.Errorf("summary text lost: %q", got.Summary)
	}
	if got.KeptTokens != 240 || got.CompressOp != "attend.compress" {
		t.Errorf("compression metadata lost: %+v", got)
	}
	if got.Intent != "review" {
		t.Errorf("intent lost: %q", got.Intent)
	}
	if got.IntentConfidence != 0.88 {
		t.Errorf("intent_confidence lost: %v", got.IntentConfidence)
	}
}

// TestThinkSessionSummary_LegacyPayloadParses guarantees backward
// compatibility — a payload written before Intent / IntentConfidence
// existed must still parse cleanly, with the new fields defaulting to
// their zero values. The journal is append-only and replayed from
// disk, so old entries will be around for the life of every project.
func TestThinkSessionSummary_LegacyPayloadParses(t *testing.T) {
	// Hand-rolled JSON without intent / intent_confidence — exactly
	// what a pre-Slice-3 writer emitted.
	legacy := []byte(`{
		"session_id": "20260101T000000Z",
		"turn": 1,
		"user_prompt": "add a print to main.go",
		"summary": "Added fmt.Println to main; build succeeded.",
		"files_changed": ["main.go"],
		"verify_kind": "go build",
		"verify_ok": true,
		"orig_tokens": 1200,
		"kept_tokens": 180,
		"compress_op": "attend.compress"
	}`)
	e := &Entry{Type: TypeThinkSessionSummary, V: 1, Payload: legacy}
	got, err := ParseThinkSessionSummary(e)
	if err != nil {
		t.Fatalf("legacy payload must parse: %v", err)
	}
	if got.Intent != "" {
		t.Errorf("legacy entry must yield empty Intent, got %q", got.Intent)
	}
	if got.IntentConfidence != 0 {
		t.Errorf("legacy entry must yield zero IntentConfidence, got %v", got.IntentConfidence)
	}
	if got.Summary == "" {
		t.Error("legacy summary content lost during parse")
	}
}

// TestThinkSessionSummary_EmptyIntentOmittedFromJSON pins the
// omitempty contract — when Intent isn't set, the marshalled JSON
// omits the field entirely so older readers see exactly the shape
// they did before this slice landed.
func TestThinkSessionSummary_EmptyIntentOmittedFromJSON(t *testing.T) {
	p := ThinkSessionSummaryPayload{
		SessionID:  "s",
		Turn:       1,
		UserPrompt: "x",
		Summary:    "y",
	}
	e, err := NewThinkSessionSummaryEntry(p)
	if err != nil {
		t.Fatalf("NewThinkSessionSummaryEntry: %v", err)
	}
	if got := string(e.Payload); !contains(got, `"summary":"y"`) {
		t.Errorf("payload missing summary: %s", got)
	}
	if contains(string(e.Payload), `"intent"`) {
		t.Errorf("empty Intent must be omitted from JSON, got: %s", string(e.Payload))
	}
	if contains(string(e.Payload), `"intent_confidence"`) {
		t.Errorf("zero IntentConfidence must be omitted from JSON, got: %s", string(e.Payload))
	}
}

// contains is a tiny strings.Contains alias kept local to avoid the
// strings import for one call site.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
