package journal

import (
	"testing"
)

func TestDreamInsight_NewAndParse(t *testing.T) {
	p := DreamInsightPayload{
		InsightID:    "memory:MEMORY.md:Direction",
		Category:     "decision",
		Content:      "Cortex is a learning harness",
		Importance:   8,
		Tags:         []string{"direction", "core"},
		SessionID:    "sess-1",
		SourceItemID: "memory:MEMORY.md:Direction",
		SourceName:   "memory-md",
	}
	e, err := NewDreamInsightEntry(p)
	if err != nil {
		t.Fatalf("NewDreamInsightEntry: %v", err)
	}
	if e.Type != TypeDreamInsight {
		t.Errorf("Type = %s, want %s", e.Type, TypeDreamInsight)
	}
	if e.V != 1 {
		t.Errorf("V = %d, want 1", e.V)
	}
	got, err := ParseDreamInsight(e)
	if err != nil {
		t.Fatalf("ParseDreamInsight: %v", err)
	}
	if got.InsightID != p.InsightID || got.Category != p.Category || got.Content != p.Content {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, p)
	}
	if got.Importance != 8 {
		t.Errorf("Importance = %d, want 8", got.Importance)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "direction" {
		t.Errorf("Tags = %v, want [direction core]", got.Tags)
	}
}

func TestDreamInsight_RejectsEmptyRequiredFields(t *testing.T) {
	if _, err := NewDreamInsightEntry(DreamInsightPayload{Content: "x"}); err == nil {
		t.Error("expected error when InsightID empty")
	}
	if _, err := NewDreamInsightEntry(DreamInsightPayload{InsightID: "x"}); err == nil {
		t.Error("expected error when Content empty")
	}
}

func TestParseDreamInsight_RejectsNonDreamEntry(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseDreamInsight(e); err == nil {
		t.Error("expected error parsing capture.event as dream.insight")
	}
}

func TestDreamSessionDigest_RoundTrip(t *testing.T) {
	p := DreamSessionDigestPayload{
		Narrative:        "Across the last 20 turns, the user established a Go DAG-based learning harness with intent ingestion, per-intent budgets, and a feedback writer-class.",
		SummaryCountIn:   20,
		CoversSessionIDs: []string{"sess-1", "sess-2", "sess-3"},
		OrigTokens:       4800,
		KeptTokens:       420,
		CompressOp:       "dream.session_digest",
	}
	e, err := NewDreamSessionDigestEntry(p)
	if err != nil {
		t.Fatalf("NewDreamSessionDigestEntry: %v", err)
	}
	if e.Type != TypeDreamSessionDigest {
		t.Errorf("Type = %s, want %s", e.Type, TypeDreamSessionDigest)
	}
	got, err := ParseDreamSessionDigest(e)
	if err != nil {
		t.Fatalf("ParseDreamSessionDigest: %v", err)
	}
	if got.Narrative != p.Narrative {
		t.Errorf("narrative lost: %q", got.Narrative)
	}
	if got.SummaryCountIn != 20 {
		t.Errorf("summary count lost: %d", got.SummaryCountIn)
	}
	if len(got.CoversSessionIDs) != 3 || got.CoversSessionIDs[0] != "sess-1" {
		t.Errorf("covers_session_ids lost: %v", got.CoversSessionIDs)
	}
	if got.KeptTokens != 420 || got.CompressOp != "dream.session_digest" {
		t.Errorf("compression metadata lost: %+v", got)
	}
}

func TestDreamSessionDigest_RejectsBadPayloads(t *testing.T) {
	if _, err := NewDreamSessionDigestEntry(DreamSessionDigestPayload{SummaryCountIn: 5}); err == nil {
		t.Error("expected error when Narrative empty")
	}
	if _, err := NewDreamSessionDigestEntry(DreamSessionDigestPayload{Narrative: "x"}); err == nil {
		t.Error("expected error when SummaryCountIn is 0")
	}
}

func TestParseDreamSessionDigest_RejectsNonDigestEntry(t *testing.T) {
	e := &Entry{Type: "dream.insight", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseDreamSessionDigest(e); err == nil {
		t.Error("expected error parsing dream.insight as dream.session_digest")
	}
}
