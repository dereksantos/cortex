package study

import (
	"context"
	"math/rand"
	"strings"
	"testing"
)

func focusGrid() *BoundaryOutput {
	return BuildByteGrid("/a", "a", 500000, ByteGridOpts{WindowTokens: 8192, Bands: 16})
}

func TestFocusSampler_BiasesTowardFocusRange(t *testing.T) {
	out := focusGrid()
	focus := Focus{Lines: [2]int{5000, 5200}}
	fs := newFocusSampler(&HierarchicalSampler{}, out, focus)
	if len(fs.inFocus) == 0 {
		t.Fatal("focus matched no chunks")
	}
	rng := rand.New(rand.NewSource(1))
	in, total := 0, 0
	for i := 0; i < 300; i++ {
		for _, id := range fs.Next(out, map[string]bool{}, 1, rng) {
			total++
			if fs.inFocus[id] {
				in++
			}
		}
	}
	frac := float64(in) / float64(total)
	if frac < 0.5 {
		t.Errorf("focus bias too weak: in-focus fraction = %.2f, want >= 0.5", frac)
	}
}

func TestFocusSampler_FallsBackWhenFocusExhausted(t *testing.T) {
	out := focusGrid()
	fs := newFocusSampler(&HierarchicalSampler{}, out, Focus{Lines: [2]int{5000, 5200}})
	covered := map[string]bool{}
	for id := range fs.inFocus {
		covered[id] = true
	}
	rng := rand.New(rand.NewSource(2))
	picks := fs.Next(out, covered, 4, rng)
	if len(picks) != 4 {
		t.Fatalf("expected 4 fallback picks once focus exhausted, got %d", len(picks))
	}
	for _, id := range picks {
		if fs.inFocus[id] {
			t.Errorf("fallback returned a covered in-focus chunk %q", id)
		}
	}
}

func TestFocusSampler_NoFocusEqualsInner(t *testing.T) {
	out := focusGrid()
	// Empty focus → the focus sampler must reproduce the inner sequence.
	fs := newFocusSampler(&HierarchicalSampler{}, out, Focus{})
	hs := &HierarchicalSampler{}

	rng1 := rand.New(rand.NewSource(7))
	rng2 := rand.New(rand.NewSource(7))
	covered := map[string]bool{}
	for i := 0; i < 5; i++ {
		a := fs.Next(out, covered, 4, rng1)
		b := hs.Next(out, covered, 4, rng2)
		if len(a) != len(b) {
			t.Fatalf("iter %d: length differ %d vs %d", i, len(a), len(b))
		}
		for j := range a {
			if a[j] != b[j] {
				t.Errorf("iter %d pick %d differs: %q vs %q", i, j, a[j], b[j])
			}
		}
	}
}

func TestFocusSampler_Name(t *testing.T) {
	fs := newFocusSampler(&HierarchicalSampler{}, focusGrid(), Focus{Lines: [2]int{1, 10}})
	if got := fs.Name(); got != "hierarchical-v1+focus" {
		t.Errorf("Name = %q, want hierarchical-v1+focus", got)
	}
}

func TestResolveFocus_LinesPassthrough(t *testing.T) {
	spec, err := ResolveFocus("", Focus{Lines: [2]int{200, 100}}) // reversed
	if err != nil {
		t.Fatalf("ResolveFocus: %v", err)
	}
	if spec.Lines != [2]int{100, 200} {
		t.Errorf("Lines = %v, want [100 200] (normalized)", spec.Lines)
	}
	empty, _ := ResolveFocus("", Focus{Symbol: "PgStorage"})
	if empty.Lines != [2]int{0, 0} {
		t.Errorf("unresolved symbol should yield empty spec, got %v", empty.Lines)
	}
}

func TestResolveLineRangeToBytes(t *testing.T) {
	path, lb := writeLinesFile(t, 300) // uniform 9 bytes/line
	cases := []struct {
		lo, hi             int
		wantStart, wantEnd int64
	}{
		{1, 10, 0, lb(11)}, // head
		{100, 110, lb(100), lb(111)},
		{295, 300, lb(295), 300 * 9}, // tail clamps to EOF
	}
	for _, c := range cases {
		sb, eb, err := resolveLineRangeToBytes(path, c.lo, c.hi)
		if err != nil {
			t.Fatalf("resolveLineRangeToBytes(%d,%d): %v", c.lo, c.hi, err)
		}
		if sb != c.wantStart || eb != c.wantEnd {
			t.Errorf("lines %d-%d → bytes [%d,%d], want [%d,%d]", c.lo, c.hi, sb, eb, c.wantStart, c.wantEnd)
		}
	}
}

// TestStudyFile_FocusByRealLines guards the bytes-per-line mismatch bug:
// a file at 9 bytes/line is far from the grid's 50-byte estimate, so a
// focus expressed in REAL line numbers must still land on those real
// lines (not on provisional-line-space bytes). The file is big enough
// that the sparse draw can't trivially cover everything.
func TestStudyFile_FocusByRealLines(t *testing.T) {
	path, _ := writeLinesFile(t, 20000) // 180000 bytes, 9 bytes/line, ~44 chunks
	focus := [2]int{5000, 6000}
	intersects := func(s SampledChunk) bool { return s.LineStart <= focus[1] && s.LineEnd >= focus[0] }

	req := StudyRequest{Path: path, Window: 8192, Density: "sparse", Session: "rl"}
	withFocus := req
	withFocus.Focus = &Focus{Lines: focus}

	wf, err := StudyFile(context.Background(), withFocus)
	if err != nil {
		t.Fatalf("focused StudyFile: %v", err)
	}
	nf, err := StudyFile(context.Background(), req)
	if err != nil {
		t.Fatalf("unfocused StudyFile: %v", err)
	}

	fh, nh := 0, 0
	for _, s := range wf.Sampled {
		if intersects(s) {
			fh++
		}
	}
	for _, s := range nf.Sampled {
		if intersects(s) {
			nh++
		}
	}
	if fh == 0 {
		t.Fatalf("focus on real lines %v hit 0 sampled chunks (provisional-line bug)", focus)
	}
	if fh <= nh {
		t.Errorf("real-line focus did not concentrate: in-focus with=%d, without=%d", fh, nh)
	}
}

// corpusGrid builds a real multi-file boundary over a fixture dir so
// focus-by-path membership is exercised against analyzer-shaped chunks.
func corpusGrid(t *testing.T) *BoundaryOutput {
	t.Helper()
	root := writeDirFixture(t, map[string]string{
		"top.txt":     lineBlob(20000),
		"pkg/a.txt":   lineBlob(20000),
		"pkg/b.txt":   lineBlob(20000),
		"other/c.txt": lineBlob(20000),
		"other/d.txt": lineBlob(20000),
	})
	out, err := UniversalAnalyzer{}.Analyze(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return out
}

func TestFocusSampler_PathTargetsSubtree(t *testing.T) {
	out := corpusGrid(t)
	fs := newFocusSampler(&HierarchicalSampler{}, out, Focus{Path: "pkg"})
	if len(fs.inFocus) == 0 {
		t.Fatal("path focus matched no chunks")
	}
	for _, c := range out.Chunks {
		under := c.RelPath == "pkg" || strings.HasPrefix(c.RelPath, "pkg/")
		if fs.inFocus[c.ID] != under {
			t.Errorf("chunk %s in-focus=%t, want %t", c.RelPath, fs.inFocus[c.ID], under)
		}
	}

	// The bias concentrates draws under the path (statistical, fixed seed).
	rng := rand.New(rand.NewSource(3))
	in, total := 0, 0
	for i := 0; i < 300; i++ {
		for _, id := range fs.Next(out, map[string]bool{}, 1, rng) {
			total++
			if fs.inFocus[id] {
				in++
			}
		}
	}
	if frac := float64(in) / float64(total); frac < 0.5 {
		t.Errorf("path bias too weak: in-focus fraction = %.2f, want >= 0.5", frac)
	}
}

func TestFocusSampler_PathTargetsSingleFile(t *testing.T) {
	out := corpusGrid(t)
	fs := newFocusSampler(&HierarchicalSampler{}, out, Focus{Path: "other/c.txt"})
	for _, c := range out.Chunks {
		if fs.inFocus[c.ID] != (c.RelPath == "other/c.txt") {
			t.Errorf("chunk %s in-focus=%t, want %t", c.RelPath, fs.inFocus[c.ID], c.RelPath == "other/c.txt")
		}
	}
}

// A path focus on a single-file grid (every chunk shares the relpath) is
// vacuous as a filter; it must fall through to byte-resolved line
// targeting rather than putting the entire file in focus.
func TestFocusSampler_PathOnSingleFileGridUsesLines(t *testing.T) {
	path := writeBytesFile(t, 500000)
	out := BuildByteGrid(path, "blob.txt", 500000, ByteGridOpts{WindowTokens: 8192, Bands: 16})
	withPath := newFocusSampler(&HierarchicalSampler{}, out, Focus{Path: "blob.txt", Lines: [2]int{5000, 5200}})
	linesOnly := newFocusSampler(&HierarchicalSampler{}, out, Focus{Lines: [2]int{5000, 5200}})
	if len(withPath.inFocus) == 0 {
		t.Fatal("path+lines focus matched no chunks")
	}
	if len(withPath.inFocus) != len(linesOnly.inFocus) {
		t.Errorf("single-file path+lines in-focus = %d chunks, want %d (same as lines-only)",
			len(withPath.inFocus), len(linesOnly.inFocus))
	}
}

func TestStudyFile_FocusConcentratesSample(t *testing.T) {
	path := writeBytesFile(t, 500000) // ~10k lines @ 50 bytes
	focusLines := [2]int{5000, 5200}
	inRange := func(s SampledChunk) bool { return s.LineStart <= focusLines[1] && s.LineEnd >= focusLines[0] }

	withFocus, err := StudyFile(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "dense", Session: "f",
		Focus: &Focus{Lines: focusLines},
	})
	if err != nil {
		t.Fatalf("focused StudyFile: %v", err)
	}
	noFocus, err := StudyFile(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "dense", Session: "f",
	})
	if err != nil {
		t.Fatalf("unfocused StudyFile: %v", err)
	}
	fc, nfc := 0, 0
	for _, s := range withFocus.Sampled {
		if inRange(s) {
			fc++
		}
	}
	for _, s := range noFocus.Sampled {
		if inRange(s) {
			nfc++
		}
	}
	if fc < 1 {
		t.Errorf("focused sample hit the focus range 0 times")
	}
	if fc <= nfc {
		t.Errorf("focus did not concentrate sample: in-focus with=%d, without=%d", fc, nfc)
	}
}
