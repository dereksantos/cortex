package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSegment crafts a synthetic segment file with one or more
// pre-timestamped entries. classDir/segNum.jsonl is written directly
// — we don't go through Writer because the test wants control over
// per-entry TS to drive the age-based selection.
func writeSegment(t *testing.T, classDir string, segNum int, entries []Entry) {
	t.Helper()
	if err := os.MkdirAll(classDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(segmentPath(classDir, segNum))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if e.Type == "" {
			e.Type = "test.event"
		}
		if e.V == 0 {
			e.V = 1
		}
		if e.Payload == nil {
			e.Payload = json.RawMessage(`{}`)
		}
		if err := enc.Encode(&e); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

func mkEntry(off int64, ts time.Time) Entry {
	return Entry{Offset: Offset(off), TS: ts}
}

func TestPrune_AgeBased_DropsOldClosedSegments(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	// Segment 1: very old (newest entry 200 days ago) — drop
	// Segment 2: recent (newest entry 10 days ago) — keep
	// Segment 3: ACTIVE — never touched
	writeSegment(t, dir, 1, []Entry{
		mkEntry(1, now.AddDate(0, 0, -210)),
		mkEntry(2, now.AddDate(0, 0, -200)),
	})
	writeSegment(t, dir, 2, []Entry{
		mkEntry(3, now.AddDate(0, 0, -20)),
		mkEntry(4, now.AddDate(0, 0, -10)),
	})
	writeSegment(t, dir, 3, []Entry{
		mkEntry(5, now.Add(-time.Hour)),
	})

	report, err := Prune(dir, PruneOptions{MaxAge: 90 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != 1 {
		t.Fatalf("Removed=%v; want [1]", report.Removed)
	}
	// Segment 1 must be gone from disk.
	if _, err := os.Stat(segmentPath(dir, 1)); !os.IsNotExist(err) {
		t.Errorf("segment 1 still on disk: %v", err)
	}
	// Segment 2 and 3 must remain.
	if _, err := os.Stat(segmentPath(dir, 2)); err != nil {
		t.Errorf("segment 2 missing: %v", err)
	}
	if _, err := os.Stat(segmentPath(dir, 3)); err != nil {
		t.Errorf("segment 3 (active) missing: %v", err)
	}
}

func TestPrune_ActiveSegmentNeverPruned(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	// Single segment, ancient — but it IS the active segment because
	// it's the highest-numbered, so Prune must leave it alone.
	writeSegment(t, dir, 1, []Entry{mkEntry(1, now.AddDate(-3, 0, 0))})

	report, err := Prune(dir, PruneOptions{MaxAge: 1 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Errorf("Removed=%v; want []", report.Removed)
	}
	if _, err := os.Stat(segmentPath(dir, 1)); err != nil {
		t.Errorf("active segment removed: %v", err)
	}
}

func TestPrune_DryRunReportsButDoesntDelete(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	writeSegment(t, dir, 1, []Entry{mkEntry(1, now.AddDate(0, 0, -200))})
	writeSegment(t, dir, 2, []Entry{mkEntry(2, now)})

	report, err := Prune(dir, PruneOptions{MaxAge: 30 * 24 * time.Hour, Now: now, DryRun: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(report.Removed) != 1 {
		t.Errorf("Removed count = %d; want 1", len(report.Removed))
	}
	// Segment 1 should still exist on disk.
	if _, err := os.Stat(segmentPath(dir, 1)); err != nil {
		t.Errorf("dry-run deleted segment 1: %v", err)
	}
	if !report.DryRun {
		t.Errorf("report.DryRun=false; want true")
	}
}

func TestPrune_ByteBudget_EvictsOldestUntilUnderLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	// Three closed segments + one active. Each closed is ~400 bytes
	// of padding. With MaxBytes set tight, the byte pass should evict
	// the oldest two.
	mkBigEntry := func(off int64, ts time.Time) Entry {
		// Inflate payload so each segment is comfortably over 100 bytes.
		filler := make([]byte, 200)
		for i := range filler {
			filler[i] = 'x'
		}
		payload, _ := json.Marshal(map[string]string{"pad": string(filler)})
		return Entry{Offset: Offset(off), TS: ts, Type: "test.event", V: 1, Payload: payload}
	}
	writeSegment(t, dir, 1, []Entry{mkBigEntry(1, now.Add(-3*time.Hour))})
	writeSegment(t, dir, 2, []Entry{mkBigEntry(2, now.Add(-2*time.Hour))})
	writeSegment(t, dir, 3, []Entry{mkBigEntry(3, now.Add(-time.Hour))})
	writeSegment(t, dir, 4, []Entry{mkBigEntry(4, now)}) // active

	// Sum of segments 1+2+3 will be > 600. MaxBytes=300 forces 1+2 out;
	// 3 alone fits under the budget.
	report, err := Prune(dir, PruneOptions{MaxBytes: 300, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(report.Removed) < 2 {
		t.Fatalf("Removed=%v; want at least 2 evictions to drop under 300 bytes", report.Removed)
	}
	if _, err := os.Stat(segmentPath(dir, 4)); err != nil {
		t.Errorf("active segment removed: %v", err)
	}
	for _, n := range report.Removed {
		if n == 4 {
			t.Errorf("active segment 4 was removed")
		}
	}
}

func TestPrune_OnlyActiveSegment_NoOp(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, 1, []Entry{mkEntry(1, time.Now())})
	report, err := Prune(dir, PruneOptions{MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Errorf("Removed=%v; want []", report.Removed)
	}
}

func TestPruneAll_MultipleClasses(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	for _, class := range []string{"capture", "dream", "think"} {
		cd := filepath.Join(root, class)
		writeSegment(t, cd, 1, []Entry{mkEntry(1, now.AddDate(0, 0, -180))}) // old
		writeSegment(t, cd, 2, []Entry{mkEntry(2, now)})                     // active
	}

	reports, err := PruneAll(root, PruneOptions{MaxAge: 30 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("PruneAll: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports=%d; want 3", len(reports))
	}
	for _, r := range reports {
		if len(r.Removed) != 1 || r.Removed[0] != 1 {
			t.Errorf("class %s: Removed=%v, want [1]", filepath.Base(r.ClassDir), r.Removed)
		}
	}
}

func TestPrune_GzippedSegment_AlsoPruned(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	// Write an old closed segment, then compact it to .gz to mirror
	// the real on-disk state for retained-but-old data.
	writeSegment(t, dir, 1, []Entry{mkEntry(1, now.AddDate(0, 0, -100))})
	writeSegment(t, dir, 2, []Entry{mkEntry(2, now)}) // active
	if err := CompactSegment(dir, 1); err != nil {
		t.Fatalf("CompactSegment: %v", err)
	}
	// Sanity check: gz exists, plain doesn't.
	if _, err := os.Stat(segmentPathGZ(dir, 1)); err != nil {
		t.Fatalf("gz segment 1 missing: %v", err)
	}

	report, err := Prune(dir, PruneOptions{MaxAge: 30 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != 1 {
		t.Errorf("Removed=%v; want [1]", report.Removed)
	}
	if _, err := os.Stat(segmentPathGZ(dir, 1)); !os.IsNotExist(err) {
		t.Errorf("gzipped segment 1 still on disk: %v", err)
	}
}

// silence unused-fmt warning when the test file is built standalone.
var _ = fmt.Sprintf
