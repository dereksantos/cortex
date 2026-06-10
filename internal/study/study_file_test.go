package study

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeBytesFile writes exactly nBytes to a temp file, placing a newline
// every 50 bytes so RefineChunk has line boundaries to snap to.
func writeBytesFile(t *testing.T, nBytes int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.txt")
	b := make([]byte, nBytes)
	for i := range b {
		if (i+1)%50 == 0 {
			b[i] = '\n'
		} else {
			b[i] = 'a'
		}
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func TestStudyFile_SubThreshold_ReadMode(t *testing.T) {
	path := writeBytesFile(t, 1000) // est_tokens = 250, well under window/2
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if resp.Mode != "read" {
		t.Fatalf("Mode = %q, want read", resp.Mode)
	}
	want, _ := os.ReadFile(path)
	if resp.ReadContent != string(want) {
		t.Errorf("ReadContent does not match file (%d vs %d bytes)", len(resp.ReadContent), len(want))
	}
	if len(resp.Sampled) != 0 {
		t.Errorf("read mode should not sample, got %d sampled chunks", len(resp.Sampled))
	}
}

func TestStudyFile_OverThreshold_StudyMode(t *testing.T) {
	path := writeBytesFile(t, 60000) // est_tokens = 15000 >> window/2
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192, Density: "sparse"})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if resp.Mode != "study" {
		t.Fatalf("Mode = %q, want study", resp.Mode)
	}
	if len(resp.Sampled) != 4 { // sparse → k=4
		t.Fatalf("sampled %d chunks, want 4 (sparse)", len(resp.Sampled))
	}
	for i, s := range resp.Sampled {
		if s.LineStart <= 0 || s.LineEnd < s.LineStart {
			t.Errorf("sampled[%d] has unrefined line bounds: %d-%d", i, s.LineStart, s.LineEnd)
		}
		if s.Snippet == "" {
			t.Errorf("sampled[%d] has empty snippet", i)
		}
	}
}

func TestStudyFile_CoverageMath(t *testing.T) {
	path := writeBytesFile(t, 60000)
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192, Density: "normal"})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if resp.Coverage.EffLinesTotal <= 0 {
		t.Fatalf("EffLinesTotal = %d, want > 0", resp.Coverage.EffLinesTotal)
	}
	want := float64(resp.Coverage.EffLinesSeen) / float64(resp.Coverage.EffLinesTotal)
	if resp.Coverage.Pct != want {
		t.Errorf("Coverage.Pct = %f, want %f", resp.Coverage.Pct, want)
	}
	if resp.Coverage.Pct < 0 || resp.Coverage.Pct > 1 {
		t.Errorf("Coverage.Pct = %f, out of [0,1]", resp.Coverage.Pct)
	}
}

func TestStudyFile_DeterministicGivenSession(t *testing.T) {
	path := writeBytesFile(t, 80000)
	req := StudyRequest{Path: path, Window: 8192, Density: "normal", Session: "study-x"}
	a, err := StudyFile(context.Background(), req)
	if err != nil {
		t.Fatalf("StudyFile a: %v", err)
	}
	b, err := StudyFile(context.Background(), req)
	if err != nil {
		t.Fatalf("StudyFile b: %v", err)
	}
	if len(a.Sampled) != len(b.Sampled) {
		t.Fatalf("sample counts differ: %d vs %d", len(a.Sampled), len(b.Sampled))
	}
	for i := range a.Sampled {
		if a.Sampled[i].ByteOffset != b.Sampled[i].ByteOffset {
			t.Errorf("sampled[%d] differs: %d vs %d", i, a.Sampled[i].ByteOffset, b.Sampled[i].ByteOffset)
		}
	}
}

func TestStudyFile_ThresholdBoundary(t *testing.T) {
	const window = 8192 // window/2 = 4096; est_tokens = size/4
	cases := []struct {
		name     string
		size     int
		wantMode string
	}{
		{"just-under", 16380, "read"},   // est 4095 < 4096
		{"at-boundary", 16384, "study"}, // est 4096, not < 4096
		{"over", 40000, "study"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeBytesFile(t, c.size)
			resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: window, Density: "sparse"})
			if err != nil {
				t.Fatalf("StudyFile: %v", err)
			}
			if resp.Mode != c.wantMode {
				t.Errorf("size %d: Mode = %q, want %q", c.size, resp.Mode, c.wantMode)
			}
		})
	}
}

func TestStudyFile_DirIsError(t *testing.T) {
	if _, err := StudyFile(context.Background(), StudyRequest{Path: t.TempDir(), Window: 8192}); err == nil {
		t.Error("expected error studying a directory")
	}
}

// Fill trades chunk size for chunk count at the same total sample: at
// window 8192, fill 1/8 targets 4096-byte chunks and fill 1/16 targets
// 2048-byte chunks, so the same data arrives as twice the chunks at
// half the size.
func TestStudyFile_FillShrinksChunks(t *testing.T) {
	path := writeBytesFile(t, 64*1024) // well over threshold at window 8192

	sample := func(fill float64, k int) []SampledChunk {
		t.Helper()
		resp, err := StudyFile(context.Background(), StudyRequest{
			Path: path, Window: 8192, Density: k, Fill: fill,
		})
		if err != nil {
			t.Fatalf("StudyFile(fill=%v): %v", fill, err)
		}
		if resp.Mode != "study" {
			t.Fatalf("Mode = %q, want study", resp.Mode)
		}
		return resp.Sampled
	}

	// RefineChunk snaps bounds to line starts, so sizes land within one
	// line width (50B here) of the grid target rather than exactly on it.
	near := func(got, want int) bool { return got >= want-100 && got <= want+100 }
	for _, c := range sample(1.0/8, 4) {
		if !near(c.ByteLength, 4096) {
			t.Errorf("fill 1/8 chunk = %d bytes, want ~4096", c.ByteLength)
		}
	}
	small := sample(1.0/16, 8) // targets 2048-byte chunks, same 16KB total
	if len(small) != 8 {
		t.Fatalf("fill 1/16 k=8: sampled %d chunks, want 8", len(small))
	}
	for _, c := range small {
		if !near(c.ByteLength, 2048) {
			t.Errorf("fill 1/16 chunk = %d bytes, want ~2048", c.ByteLength)
		}
	}
}

// Nil Density is the zero-knob path: chunk size comes from the format's
// coherence unit (txt → 1024B) and k from the budget (window×4 / unit),
// so one pass samples the full window at unit granularity. Explicit
// densities stay pinned (the "sparse"→4 contract above).
func TestStudyFile_AutoDensityFillsBudget(t *testing.T) {
	path := writeBytesFile(t, 64*1024)
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if resp.Mode != "study" {
		t.Fatalf("Mode = %q, want study", resp.Mode)
	}
	// window 8192 → 32768 budget bytes / 1024B prose unit = 32 chunks.
	if len(resp.Sampled) != 32 {
		t.Fatalf("auto density sampled %d chunks, want 32 (budget/unit)", len(resp.Sampled))
	}
	var total int
	for _, s := range resp.Sampled {
		total += s.ByteLength
	}
	// The sample should land near the full budget (line-snapping trims
	// up to a line width per chunk).
	if budget := 8192 * 4; total < budget*3/4 || total > budget {
		t.Errorf("auto density sampled %d bytes, want ~%d (full budget)", total, budget)
	}
	// Densify on the auto path re-runs at the same k — the budget is
	// already full; novelty comes from the covered set, not more chunks.
	if d := ResolveDensity(resp.Deepen.Densify.Density); d != 32 {
		t.Errorf("auto Densify density = %d, want 32 (same-k repass)", d)
	}
}
