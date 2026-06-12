package study

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDirFixture materializes a corpus under a temp dir. Keys are
// slash-relative paths; values are file contents. Returns the root.
func writeDirFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// lineBlob returns nBytes of 'a' content with a newline every 50 bytes —
// the same shape writeBytesFile uses, so chunking has real lines.
func lineBlob(nBytes int) string {
	b := make([]byte, nBytes)
	for i := range b {
		if (i+1)%50 == 0 {
			b[i] = '\n'
		} else {
			b[i] = 'a'
		}
	}
	return string(b)
}

func TestStudyDir_SubThreshold_ReadMode(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"b.txt":        "beta content\n",
		"a.txt":        "alpha content\n",
		"sub/c.txt":    "gamma content\n",
		".git/x.txt":   "SECRET\n",
		".cortex/y.db": "DERIVED\n",
	})
	resp, err := StudyFile(context.Background(), StudyRequest{Path: root, Window: 8192})
	if err != nil {
		t.Fatalf("StudyFile(dir): %v", err)
	}
	if resp.Mode != "read" {
		t.Fatalf("Mode = %q, want read", resp.Mode)
	}
	// Every source file appears, labelled, in sorted relpath order.
	for _, want := range []string{"----- a.txt -----", "alpha content", "----- b.txt -----", "beta content", "----- sub/c.txt -----", "gamma content"} {
		if !strings.Contains(resp.ReadContent, want) {
			t.Errorf("ReadContent missing %q", want)
		}
	}
	if ai, bi := strings.Index(resp.ReadContent, "----- a.txt"), strings.Index(resp.ReadContent, "----- b.txt"); ai > bi {
		t.Errorf("files not in sorted relpath order (a at %d, b at %d)", ai, bi)
	}
	// Ignore set holds: .git and .cortex data never inline.
	for _, banned := range []string{"SECRET", "DERIVED"} {
		if strings.Contains(resp.ReadContent, banned) {
			t.Errorf("ReadContent leaked ignored content %q", banned)
		}
	}
	if len(resp.Sampled) != 0 {
		t.Errorf("read mode should not sample, got %d chunks", len(resp.Sampled))
	}
}

func TestStudyDir_ReadMode_PrefixesRelPath(t *testing.T) {
	root := writeDirFixture(t, map[string]string{"a.txt": "alpha\n"})
	resp, err := StudyFile(context.Background(), StudyRequest{Path: root, RelPath: "pkg/stuff", Window: 8192})
	if err != nil {
		t.Fatalf("StudyFile(dir): %v", err)
	}
	if !strings.Contains(resp.ReadContent, "----- pkg/stuff/a.txt -----") {
		t.Errorf("read headers should carry the caller-relative prefix; got:\n%s", resp.ReadContent)
	}
}

func TestStudyDir_EmptyDir_ReadMode(t *testing.T) {
	resp, err := StudyFile(context.Background(), StudyRequest{Path: t.TempDir(), Window: 8192})
	if err != nil {
		t.Fatalf("StudyFile(empty dir): %v", err)
	}
	if resp.Mode != "read" || resp.ReadContent != "" {
		t.Errorf("empty dir: Mode=%q len(content)=%d, want read with empty content", resp.Mode, len(resp.ReadContent))
	}
}

func TestStudyDir_OverThreshold_StudyMode(t *testing.T) {
	// 3 × 20KB = 60KB total → est 15000 tokens ≫ window/2 (4096).
	root := writeDirFixture(t, map[string]string{
		"one.txt":     lineBlob(20000),
		"two.txt":     lineBlob(20000),
		"sub/big.txt": lineBlob(20000),
	})
	resp, err := StudyFile(context.Background(), StudyRequest{
		Path: root, RelPath: "pkg/stuff", Window: 8192, Density: "sparse",
	})
	if err != nil {
		t.Fatalf("StudyFile(dir): %v", err)
	}
	if resp.Mode != "study" {
		t.Fatalf("Mode = %q, want study", resp.Mode)
	}
	if len(resp.Sampled) == 0 {
		t.Fatal("study mode sampled nothing")
	}
	seenFiles := map[string]bool{}
	for i, s := range resp.Sampled {
		if !strings.HasPrefix(s.RelPath, "pkg/stuff/") {
			t.Errorf("sampled[%d].RelPath = %q, want pkg/stuff/ prefix", i, s.RelPath)
		}
		if s.LineStart <= 0 || s.LineEnd < s.LineStart {
			t.Errorf("sampled[%d] bad line bounds %d-%d", i, s.LineStart, s.LineEnd)
		}
		if s.Snippet == "" {
			t.Errorf("sampled[%d] empty snippet", i)
		}
		seenFiles[s.RelPath] = true
	}
	// k=4 > 3 chunks: the whole corpus is drawn, spanning all files.
	if len(seenFiles) != 3 {
		t.Errorf("sampled %d distinct files, want 3", len(seenFiles))
	}
	if resp.Coverage.Pct <= 0 || resp.Coverage.Pct > 1 {
		t.Errorf("Coverage.Pct = %f, out of (0,1]", resp.Coverage.Pct)
	}
	if !resp.Exhausted {
		t.Error("drawing fewer chunks than k should mark the study exhausted")
	}
}

func TestStudyDir_Deterministic(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"one.txt":   lineBlob(20000),
		"two.txt":   lineBlob(20000),
		"three.txt": lineBlob(20000),
		"four.txt":  lineBlob(20000),
	})
	req := StudyRequest{Path: root, Window: 8192, Density: 2, Session: "study-dir-x"}
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
		if a.Sampled[i].RelPath != b.Sampled[i].RelPath || a.Sampled[i].ByteOffset != b.Sampled[i].ByteOffset {
			t.Errorf("sampled[%d] differs: %s@%d vs %s@%d", i,
				a.Sampled[i].RelPath, a.Sampled[i].ByteOffset, b.Sampled[i].RelPath, b.Sampled[i].ByteOffset)
		}
	}
}

func TestStudyDir_SessionCoverageDrawsNewRegions(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		files[name+".txt"] = lineBlob(20000)
	}
	root := writeDirFixture(t, files)

	covered := map[string]bool{}
	seen := map[string]bool{}
	req := StudyRequest{Path: root, Window: 8192, Density: 2, Session: "study-dir-cov", Covered: covered}

	for pass := 0; pass < 2; pass++ {
		resp, err := StudyFile(context.Background(), req)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		for _, s := range resp.Sampled {
			// Each fixture file is a single 400-line chunk, so RelPath
			// identifies the chunk.
			if seen[s.RelPath] {
				t.Errorf("pass %d re-sampled %s already covered in a prior pass", pass, s.RelPath)
			}
			seen[s.RelPath] = true
		}
	}
	if len(seen) != 4 {
		t.Errorf("two k=2 passes should cover 4 distinct chunks, got %d", len(seen))
	}
}

// TestStudyDir_InferAndCitations exercises phase 2 over a corpus: the
// inference mock cites one line range it actually saw (kept) and one in
// a file that was never sampled (dropped by validation).
func TestStudyDir_InferAndCitations(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"one.txt":     lineBlob(20000),
		"two.txt":     lineBlob(20000),
		"sub/big.txt": lineBlob(20000),
	})
	var sawRegions []SampledChunk
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		sawRegions = in.Sampled
		good := Citation{
			RelPath:   in.Sampled[0].RelPath,
			LineStart: in.Sampled[0].LineStart,
			LineEnd:   in.Sampled[0].LineStart,
			Claim:     "grounded claim",
		}
		bogus := Citation{RelPath: "pkg/stuff/never-sampled.txt", LineStart: 1, LineEnd: 5, Claim: "invented"}
		return InferOutput{
			Digest:    "a corpus digest",
			Citations: []Citation{good, bogus},
			Leads:     []Lead{{RelPath: "pkg/stuff/two.txt", NearLine: 42, Why: "referenced off-sample"}},
		}, nil
	}
	resp, err := StudyFile(context.Background(), StudyRequest{
		Path: root, RelPath: "pkg/stuff", Window: 8192, Density: "sparse", Infer: infer,
	})
	if err != nil {
		t.Fatalf("StudyFile(dir, infer): %v", err)
	}
	if resp.Digest != "a corpus digest" {
		t.Errorf("Digest = %q", resp.Digest)
	}
	if len(sawRegions) == 0 {
		t.Fatal("inference saw no sampled regions")
	}
	for i, s := range sawRegions {
		if !strings.HasPrefix(s.RelPath, "pkg/stuff/") {
			t.Errorf("infer input region[%d] relpath %q not caller-relative", i, s.RelPath)
		}
	}
	if len(resp.Citations) != 1 || resp.Citations[0].Claim != "grounded claim" {
		t.Fatalf("citations = %+v, want exactly the grounded claim", resp.Citations)
	}
	if len(resp.Leads) != 1 || resp.Leads[0].RelPath != "pkg/stuff/two.txt" {
		t.Errorf("leads = %+v, want the off-sample lead passed through", resp.Leads)
	}
}
