package measure

import (
	"testing"
	"time"
)

func TestRecordAndLoadInjections(t *testing.T) {
	dir := t.TempDir()

	// Record several injections
	records := []InjectionRecord{
		{Timestamp: time.Now(), Query: "how does auth work", ContentID: "insight-1", Worth: 0.8, Decision: "inject", SessionID: "sess-1"},
		{Timestamp: time.Now(), Query: "database patterns", ContentID: "insight-2", Worth: 0.3, Decision: "discard", SessionID: "sess-1"},
		{Timestamp: time.Now(), Query: "error handling", ContentID: "insight-3", Worth: 0.6, Decision: "inject", SessionID: "sess-2"},
	}

	for _, r := range records {
		if err := RecordInjection(dir, r); err != nil {
			t.Fatalf("RecordInjection() error: %v", err)
		}
	}

	// Load them back
	loaded, err := LoadRecords(dir)
	if err != nil {
		t.Fatalf("LoadRecords() error: %v", err)
	}

	if len(loaded) != 3 {
		t.Fatalf("LoadRecords() returned %d records, want 3", len(loaded))
	}

	if loaded[0].ContentID != "insight-1" {
		t.Errorf("loaded[0].ContentID = %q, want %q", loaded[0].ContentID, "insight-1")
	}
	if loaded[1].Decision != "discard" {
		t.Errorf("loaded[1].Decision = %q, want %q", loaded[1].Decision, "discard")
	}
	if loaded[2].Worth != 0.6 {
		t.Errorf("loaded[2].Worth = %.2f, want 0.6", loaded[2].Worth)
	}
}

func TestLoadRecordsMissing(t *testing.T) {
	records, err := LoadRecords("/nonexistent/path")
	if err != nil {
		t.Fatalf("LoadRecords() should return nil for missing file, got error: %v", err)
	}
	if records != nil {
		t.Errorf("LoadRecords() should return nil for missing file, got %d records", len(records))
	}
}

func TestRecordAppends(t *testing.T) {
	dir := t.TempDir()

	// Write one record
	RecordInjection(dir, InjectionRecord{ContentID: "a", Worth: 0.5, Decision: "inject"})

	// Write another
	RecordInjection(dir, InjectionRecord{ContentID: "b", Worth: 0.3, Decision: "discard"})

	loaded, _ := LoadRecords(dir)
	if len(loaded) != 2 {
		t.Fatalf("Expected 2 records after append, got %d", len(loaded))
	}
}
