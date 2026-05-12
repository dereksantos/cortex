package journal

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHashContent_DeterministicAndCollisionFree(t *testing.T) {
	a := HashContent([]byte("hello"))
	b := HashContent([]byte("hello"))
	if a != b {
		t.Errorf("hash not deterministic: %s vs %s", a, b)
	}
	c := HashContent([]byte("world"))
	if a == c {
		t.Errorf("different inputs produced same hash")
	}
	// Sha256 hex length is 64.
	if len(a) != 64 {
		t.Errorf("hash length = %d, want 64", len(a))
	}
}

func TestNewObservationEntry_RoundTrip(t *testing.T) {
	mod := time.Date(2026, 5, 12, 1, 0, 0, 0, time.UTC)
	content := []byte("the quick brown fox")

	for _, typ := range []string{
		TypeObservationClaudeTranscript,
		TypeObservationGitCommit,
		TypeObservationMemoryFile,
	} {
		e, err := NewObservationEntry(typ, "test-src", "test://uri", content, int64(len(content)), mod)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if e.Type != typ {
			t.Errorf("Type = %s, want %s", e.Type, typ)
		}
		if e.V != 1 {
			t.Errorf("V = %d, want 1", e.V)
		}
		p, err := ParseObservation(e)
		if err != nil {
			t.Fatalf("ParseObservation: %v", err)
		}
		if p.SourceName != "test-src" {
			t.Errorf("SourceName = %s, want test-src", p.SourceName)
		}
		if p.URI != "test://uri" {
			t.Errorf("URI = %s, want test://uri", p.URI)
		}
		if p.ContentHash != HashContent(content) {
			t.Errorf("ContentHash mismatch")
		}
		if p.Size != int64(len(content)) {
			t.Errorf("Size = %d, want %d", p.Size, len(content))
		}
		if !p.Modified.Equal(mod) {
			t.Errorf("Modified = %v, want %v", p.Modified, mod)
		}
	}
}

func TestNewObservationEntry_RejectsNonObservationType(t *testing.T) {
	if _, err := NewObservationEntry("capture.event", "x", "y", []byte("z"), 0, time.Time{}); err == nil {
		t.Error("expected error for non-observation type")
	}
}

func TestParseObservation_RejectsNonObservationEntry(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: json.RawMessage(`{}`)}
	if _, err := ParseObservation(e); err == nil {
		t.Error("expected error parsing capture.event as observation")
	}
}

func TestObservation_PayloadShapeIsStable(t *testing.T) {
	// Snapshot the payload JSON shape so a downstream consumer (the
	// projection in slice O3) can rely on field names + order.
	e, err := NewObservationEntry(TypeObservationGitCommit, "git", "git://repo/abc", []byte("data"), 4,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewObservationEntry: %v", err)
	}
	want := `{"source_name":"git","uri":"git://repo/abc","content_hash":"3a6eb0790f39ac87c94f3856b2dd2c5d110e6811602261a9a923d3bb23adc8b7","size":4,"modified":"2026-01-01T00:00:00Z"}`
	if string(e.Payload) != want {
		t.Errorf("payload =\n  %s\nwant\n  %s", string(e.Payload), want)
	}
}
