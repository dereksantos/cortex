package journal

import (
	"strings"
	"testing"
)

func TestNewThinkAccumulatorUpdateEntry_RoundTrip(t *testing.T) {
	in := ThinkAccumulatorUpdatePayload{
		SessionID:         "sess1",
		Step:              3,
		PrevSnapshotID:    "abc",
		Snapshot:          "fact A; fact B; …",
		SourceObservation: "step 3 raw observation here",
		SnapshotTokens:    120,
		MaxTokens:         200,
		SourceNodeIDs:     []string{"n-12", "n-13"},
		CompressorOp:      "attend.accumulate",
	}
	e, err := NewThinkAccumulatorUpdateEntry(in)
	if err != nil {
		t.Fatalf("NewThinkAccumulatorUpdateEntry: %v", err)
	}
	if e.Type != TypeThinkAccumulatorUpdate {
		t.Errorf("Type = %q, want %q", e.Type, TypeThinkAccumulatorUpdate)
	}
	if e.V != 1 {
		t.Errorf("V = %d, want 1", e.V)
	}

	out, err := ParseThinkAccumulatorUpdate(e)
	if err != nil {
		t.Fatalf("ParseThinkAccumulatorUpdate: %v", err)
	}
	if out.SessionID != in.SessionID || out.Step != in.Step {
		t.Errorf("session/step round-trip: got (%q,%d) want (%q,%d)", out.SessionID, out.Step, in.SessionID, in.Step)
	}
	if out.Snapshot != in.Snapshot {
		t.Errorf("snapshot round-trip: got %q want %q", out.Snapshot, in.Snapshot)
	}
	if len(out.SourceNodeIDs) != 2 || out.SourceNodeIDs[0] != "n-12" {
		t.Errorf("source node ids round-trip: %+v", out.SourceNodeIDs)
	}
}

func TestNewThinkAccumulatorUpdateEntry_RequiresSessionAndSnapshot(t *testing.T) {
	if _, err := NewThinkAccumulatorUpdateEntry(ThinkAccumulatorUpdatePayload{Snapshot: "x"}); err == nil {
		t.Error("expected error for missing SessionID")
	} else if !strings.Contains(err.Error(), "SessionID") {
		t.Errorf("error message should name SessionID: %v", err)
	}
	if _, err := NewThinkAccumulatorUpdateEntry(ThinkAccumulatorUpdatePayload{SessionID: "s"}); err == nil {
		t.Error("expected error for missing Snapshot")
	} else if !strings.Contains(err.Error(), "Snapshot") {
		t.Errorf("error message should name Snapshot: %v", err)
	}
}

func TestParseThinkAccumulatorUpdate_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: "think.session_summary", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseThinkAccumulatorUpdate(e); err == nil {
		t.Fatal("expected type-mismatch error")
	}
}
