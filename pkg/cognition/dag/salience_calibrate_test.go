package dag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeTraceRows writes a minimal dag_traces.jsonl with the rows
// supplied. Tests reuse this to set up calibration inputs without
// re-implementing the writer's whole schema — the salience calibrator
// only reads a few columns.
func writeTraceRows(t *testing.T, path string, rows []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

// compressRow builds a single attend.compress trace-row map shaped
// the way readSalienceRowsTail expects to consume it.
func compressRow(intent string, keptTokens int, fallback bool, ok bool) map[string]any {
	return map[string]any{
		"qualified_name":  "attend.compress",
		"salience_intent": intent,
		"ok":              ok,
		"out": map[string]any{
			"kept_tokens": keptTokens,
			"fallback":    fallback,
		},
	}
}

func TestCalibrateSalience_FitsPerIntent(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "dag_traces.jsonl")
	out := filepath.Join(dir, "calibration", "salience.json")

	// Two intents, two distinct kept_tokens distributions, one with
	// a tight (50%) fallback rate so the bump multiplier fires.
	rows := []map[string]any{
		// Plus some non-compress noise the calibration should ignore.
		{"qualified_name": "act.read_file", "ok": true, "out": map[string]any{"content": "x"}},

		compressRow("read-file", 120, false, true),
		compressRow("read-file", 160, false, true),
		compressRow("read-file", 200, false, true),
		compressRow("read-file", 220, false, true), // p90 ≈ 220

		compressRow("session-summary", 300, true, true),
		compressRow("session-summary", 320, true, true),
		compressRow("session-summary", 280, false, true),
		compressRow("session-summary", 340, true, true), // fallback rate = 3/4
	}
	writeTraceRows(t, trace, rows)

	snap, err := CalibrateSalience(SalienceCalibrateOptions{
		TracePath:    trace,
		SnapshotPath: out,
	})
	if err != nil {
		t.Fatalf("CalibrateSalience: %v", err)
	}
	if snap == nil {
		t.Fatalf("nil snapshot")
	}
	if snap.Samples != 8 {
		t.Errorf("Samples=%d; want 8 (4 read-file + 4 session-summary)", snap.Samples)
	}
	if len(snap.PerIntent) != 2 {
		t.Fatalf("PerIntent: got %d, want 2", len(snap.PerIntent))
	}

	readFile := snap.PerIntent["read-file"]
	if readFile.Samples != 4 {
		t.Errorf("read-file Samples=%d; want 4", readFile.Samples)
	}
	if readFile.FallbackRate != 0.0 {
		t.Errorf("read-file FallbackRate=%v; want 0", readFile.FallbackRate)
	}
	// p90 = 220, default headroom 1.20 → ceil(220 × 1.20) = 264.
	if readFile.SuggestedCap != 264 {
		t.Errorf("read-file SuggestedCap=%d; want 264 (p90=220 × 1.20)", readFile.SuggestedCap)
	}

	sessSummary := snap.PerIntent["session-summary"]
	if sessSummary.FallbackRate != 0.75 {
		t.Errorf("session-summary FallbackRate=%v; want 0.75 (3 fallbacks of 4)", sessSummary.FallbackRate)
	}
	// p90 = 340 → 340 × 1.20 = 408, then × 1.50 for high fallback = 612.
	if sessSummary.SuggestedCap != 612 {
		t.Errorf("session-summary SuggestedCap=%d; want 612 (p90=340 × 1.20 × 1.50 for high fallback)", sessSummary.SuggestedCap)
	}

	if snap.GlobalCap <= 0 {
		t.Errorf("GlobalCap should be > 0 with compression samples present")
	}

	// Snapshot must be on disk and re-loadable verbatim.
	loaded, err := LoadSalienceCalibration(out)
	if err != nil {
		t.Fatalf("LoadSalienceCalibration: %v", err)
	}
	if loaded == nil {
		t.Fatalf("loaded snapshot is nil after write")
	}
	if loaded.Samples != snap.Samples || len(loaded.PerIntent) != len(snap.PerIntent) {
		t.Errorf("roundtrip mismatch: loaded=%+v written=%+v", loaded, snap)
	}
}

func TestCalibrateSalience_MissingTrace_WritesEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "calibration", "salience.json")

	snap, err := CalibrateSalience(SalienceCalibrateOptions{
		TracePath:    filepath.Join(dir, "nonexistent.jsonl"),
		SnapshotPath: out,
	})
	if err != nil {
		t.Fatalf("CalibrateSalience: %v", err)
	}
	if snap.Samples != 0 {
		t.Errorf("Samples=%d; want 0", snap.Samples)
	}
	if snap.GlobalCap != 0 {
		t.Errorf("GlobalCap=%d; want 0 (no rows)", snap.GlobalCap)
	}
	// An empty snapshot is still written — callers downstream can
	// tell "I ran calibrate and there were no rows" from "I never
	// ran calibrate at all" via mtime + presence.
	if _, statErr := os.Stat(out); statErr != nil {
		t.Errorf("snapshot not written even on empty input: %v", statErr)
	}
}

func TestCalibrateSalience_MinCapFloor(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "dag_traces.jsonl")
	out := filepath.Join(dir, "calibration", "salience.json")

	// Tiny kept_tokens values that, multiplied by headroom, would
	// dip below the operational floor.
	rows := []map[string]any{
		compressRow("blink", 5, false, true),
		compressRow("blink", 8, false, true),
		compressRow("blink", 6, false, true),
	}
	writeTraceRows(t, trace, rows)

	snap, err := CalibrateSalience(SalienceCalibrateOptions{
		TracePath:    trace,
		SnapshotPath: out,
		MinCap:       100,
	})
	if err != nil {
		t.Fatalf("CalibrateSalience: %v", err)
	}
	fit := snap.PerIntent["blink"]
	if fit.SuggestedCap != 100 {
		t.Errorf("SuggestedCap=%d; want 100 (floor)", fit.SuggestedCap)
	}
}

func TestCalibrateSalience_FilterFailedRows(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "dag_traces.jsonl")
	out := filepath.Join(dir, "calibration", "salience.json")

	// 1 failed row should not enter the fit; the calibration should
	// see only the 2 successful rows.
	rows := []map[string]any{
		compressRow("a", 1000, false, false), // ok=false → ignored
		compressRow("a", 100, false, true),
		compressRow("a", 120, false, true),
	}
	writeTraceRows(t, trace, rows)

	snap, err := CalibrateSalience(SalienceCalibrateOptions{
		TracePath:    trace,
		SnapshotPath: out,
	})
	if err != nil {
		t.Fatalf("CalibrateSalience: %v", err)
	}
	fit := snap.PerIntent["a"]
	if fit.Samples != 2 {
		t.Errorf("Samples=%d; want 2 (ok=false row filtered)", fit.Samples)
	}
	if fit.P90KeptTokens > 200 {
		t.Errorf("P90 leaked the failed 1000-token row: %d", fit.P90KeptTokens)
	}
}

func TestLoadSalienceCalibration_MissingFile(t *testing.T) {
	dir := t.TempDir()
	snap, err := LoadSalienceCalibration(filepath.Join(dir, "absent.json"))
	if err != nil {
		t.Errorf("missing file should not error, got %v", err)
	}
	if snap != nil {
		t.Errorf("missing file should yield nil snapshot, got %+v", snap)
	}
}

func TestPercentile_NearestRank(t *testing.T) {
	vals := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	tests := []struct {
		n    int
		want int
	}{
		{50, 5}, // ceil(0.5 * 10) = 5 → vals[4] = 5
		{90, 9},
		{100, 10},
		{0, 1},
	}
	for _, tc := range tests {
		if got := percentile(vals, tc.n); got != tc.want {
			t.Errorf("percentile(_, %d) = %d; want %d", tc.n, got, tc.want)
		}
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty: got %d, want 0", got)
	}
}
