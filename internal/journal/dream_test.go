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
