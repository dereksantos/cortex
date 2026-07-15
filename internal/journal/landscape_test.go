package journal

import (
	"io"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/userhome"
)

// TestAppendLandscapeScanWritesEntryToUserLevelJournal pins M2.7's journal
// leg: AppendLandscapeScan resolves its class dir through internal/userhome
// (not any project's .cortex/journal) and the written entry round-trips via
// the ordinary Reader.
func TestAppendLandscapeScanWritesEntryToUserLevelJournal(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	payload := LandscapeScanPayload{
		Roots:        []string{"/home/derek/eng"},
		ToolCount:    2,
		RuntimeCount: 1,
		ProjectCount: 3,
		Truncated:    true,
	}
	if err := AppendLandscapeScan(payload); err != nil {
		t.Fatalf("AppendLandscapeScan: %v", err)
	}

	dir, err := userhome.Path("journal", "landscape")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	e, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if e.Type != TypeLandscapeScan {
		t.Errorf("Type = %q, want %q", e.Type, TypeLandscapeScan)
	}
	got, err := ParseLandscapeScan(e)
	if err != nil {
		t.Fatalf("ParseLandscapeScan: %v", err)
	}
	if got.ToolCount != 2 || got.RuntimeCount != 1 || got.ProjectCount != 3 || !got.Truncated {
		t.Errorf("payload = %+v, want %+v", *got, payload)
	}
	if len(got.Roots) != 1 || got.Roots[0] != "/home/derek/eng" {
		t.Errorf("payload.Roots = %v, want [/home/derek/eng]", got.Roots)
	}

	if _, err := r.Next(); err != io.EOF {
		t.Errorf("second Next() error = %v, want io.EOF (exactly one entry)", err)
	}
}

// TestAppendLandscapeScanIsolatedByCortexHome proves the "user-level, not
// project-level" half of M2.7: two different CORTEX_HOME values produce two
// independent journals — the event never lands under a project's own
// .cortex/journal.
func TestAppendLandscapeScanIsolatedByCortexHome(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv("CORTEX_HOME", homeA)
	if err := AppendLandscapeScan(LandscapeScanPayload{ToolCount: 1}); err != nil {
		t.Fatalf("AppendLandscapeScan (homeA): %v", err)
	}

	t.Setenv("CORTEX_HOME", homeB)
	dirB, err := userhome.Path("journal", "landscape")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	if _, err := NewReader(dirB); err != nil {
		t.Fatalf("NewReader(dirB): %v", err)
	}
	rB, err := NewReader(dirB)
	if err != nil {
		t.Fatalf("NewReader(dirB) second open: %v", err)
	}
	defer rB.Close()
	if _, err := rB.Next(); err != io.EOF {
		t.Errorf("homeB journal Next() error = %v, want io.EOF (homeA's write must not leak into homeB)", err)
	}
}

// TestLatestLandscapeScanNoJournalReturnsNotFound pins the "never scanned"
// case: a CORTEX_HOME with no landscape journal at all yields found=false
// and a nil error, not a failure GET /api/landscape's fallback would have
// to distinguish from "found nothing".
func TestLatestLandscapeScanNoJournalReturnsNotFound(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	_, _, found, err := LatestLandscapeScan()
	if err != nil {
		t.Fatalf("LatestLandscapeScan: %v", err)
	}
	if found {
		t.Error("found = true with no journal written, want false")
	}
}

// TestLatestLandscapeScanReturnsMostRecentEntry pins the "keep the latest
// by timestamp" behavior: two appended events, the newer one (later
// Entry.TS, regardless of append order into a fresh writer per event) wins.
func TestLatestLandscapeScanReturnsMostRecentEntry(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	if err := AppendLandscapeScan(LandscapeScanPayload{Roots: []string{"/old/root"}, ToolCount: 1}); err != nil {
		t.Fatalf("AppendLandscapeScan (first): %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure a distinct, later Entry.TS
	if err := AppendLandscapeScan(LandscapeScanPayload{Roots: []string{"/new/root"}, ToolCount: 2}); err != nil {
		t.Fatalf("AppendLandscapeScan (second): %v", err)
	}

	payload, ts, found, err := LatestLandscapeScan()
	if err != nil {
		t.Fatalf("LatestLandscapeScan: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if ts.IsZero() {
		t.Error("ts is zero, want the event's timestamp")
	}
	if len(payload.Roots) != 1 || payload.Roots[0] != "/new/root" || payload.ToolCount != 2 {
		t.Errorf("payload = %+v, want the second (more recent) event", payload)
	}
}
