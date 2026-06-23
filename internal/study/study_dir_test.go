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

// TestMergeLargeFiles_NamespacesAndTotals unit-tests the merge seam:
// two over-cap files become byte grids whose band modules are
// namespaced per relpath (no cross-file coverage groups), totals grow,
// drift keys land, and the RNG seed is re-derived.
func TestMergeLargeFiles_NamespacesAndTotals(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"small.txt": lineBlob(20000),
		"big1.txt":  lineBlob(1300000),
		"big2.txt":  lineBlob(1300000),
	})
	out, err := UniversalAnalyzer{}.Analyze(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := len(out.FileHashes); got != 1 {
		t.Fatalf("analyzer should only see the small file, got %d hashes", got)
	}
	preEff := out.EffTotalLines
	preSeed := out.RNGSeed
	preChunks := len(out.Chunks)

	large := []sourceFile{
		{abs: filepath.Join(root, "big1.txt"), rel: "big1.txt", size: 1300000, mtime: 1111},
		{abs: filepath.Join(root, "big2.txt"), rel: "big2.txt", size: 1300000, mtime: 2222},
	}
	mergeLargeFiles(out, large, 8192, 0, "s")

	if len(out.Chunks) <= preChunks {
		t.Fatal("merge added no chunks")
	}
	if out.EffTotalLines <= preEff {
		t.Error("merge did not grow EffTotalLines")
	}
	if out.RNGSeed == preSeed {
		t.Error("merge must re-derive the RNG seed (large-file drift changes the draw)")
	}
	for _, rel := range []string{"big1.txt", "big2.txt"} {
		if _, ok := out.FileHashes[rel]; !ok {
			t.Errorf("FileHashes missing drift key for %s", rel)
		}
	}
	// Module IDs must be unique — every grid emits band-00..band-NN and
	// two large files must not share coverage groups.
	seenMod := map[string]bool{}
	for _, m := range out.Modules {
		if seenMod[m.ID] {
			t.Errorf("module ID collision: %s", m.ID)
		}
		seenMod[m.ID] = true
	}
	// Chunks of each large file sit in modules namespaced by its relpath.
	for _, c := range out.Chunks {
		if c.RelPath == "big1.txt" && !strings.HasPrefix(c.ModuleID, "big1.txt#") {
			t.Errorf("big1 chunk in module %q, want big1.txt# prefix", c.ModuleID)
		}
	}

	// Determinism: merging the same inputs again from a fresh Analyze
	// yields the same seed and chunk IDs.
	out2, err := UniversalAnalyzer{}.Analyze(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Analyze 2: %v", err)
	}
	mergeLargeFiles(out2, large, 8192, 0, "s")
	if out2.RNGSeed != out.RNGSeed {
		t.Errorf("seeds differ across identical merges: %d vs %d", out.RNGSeed, out2.RNGSeed)
	}
	if len(out2.Chunks) != len(out.Chunks) {
		t.Errorf("chunk counts differ across identical merges: %d vs %d", len(out.Chunks), len(out2.Chunks))
	}
}

// TestStudyDir_LargeFileSampled is the end-to-end guarantee: a >1 MiB
// file inside a studied dir is no longer invisible. With two 1-chunk
// small files and k=20, at least 18 draws must come from the large
// file's grid (pigeonhole), and they refine to real line bounds. The
// large file's size also pushes the dir over the read threshold.
func TestStudyDir_LargeFileSampled(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"a.txt":   "tiny alpha\n",
		"b.txt":   "tiny beta\n",
		"big.txt": lineBlob(1300000),
	})
	resp, err := StudyFile(context.Background(), StudyRequest{
		Path: root, RelPath: "logs", Window: 8192, Density: 20,
	})
	if err != nil {
		t.Fatalf("StudyFile(dir with large file): %v", err)
	}
	if resp.Mode != "study" {
		t.Fatalf("Mode = %q, want study (large file must count toward the threshold)", resp.Mode)
	}
	bigSampled := 0
	for i, s := range resp.Sampled {
		if s.RelPath != "logs/big.txt" {
			continue
		}
		bigSampled++
		if s.LineStart <= 0 || s.LineEnd < s.LineStart {
			t.Errorf("sampled[%d] unrefined line bounds %d-%d", i, s.LineStart, s.LineEnd)
		}
		if s.Snippet == "" {
			t.Errorf("sampled[%d] empty snippet", i)
		}
	}
	if bigSampled < 18 {
		t.Errorf("sampled %d chunks from the large file, want >= 18 (only 2 small chunks exist)", bigSampled)
	}
	// Coverage denominator includes the large file's estimated lines.
	if resp.Coverage.EffLinesTotal < 10000 {
		t.Errorf("EffLinesTotal = %d, want the large file's ~26k estimated lines included", resp.Coverage.EffLinesTotal)
	}
}

// TestStudyDir_ScopeToRootModule_ExcludesNestedModules is the core
// guarantee: a nested module (its own go.mod) inside the studied root is
// excluded from sampling, while marker-less subdirs of the root module
// are kept. This is the fix for the eval-fixture misfire — studying the
// repo root must not sample test/evals/projects/*/server.
func TestStudyDir_ScopeToRootModule_ExcludesNestedModules(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"go.mod":               "module example.com/m\n",
		"main.go":              "package main\nfunc main() {}\n",
		"src/small.go":         "package src\nvar _ = 1\n",
		"nested/go.mod":        "module example.com/m/nested\n",
		"nested/server.go":     "package nested\nfunc Serve() {}\n",
		"nested/deep/go.mod":   "module example.com/m/nested/deep\n",
		"nested/deep/x.go":     "package deep\nvar _ = 1\n",
	})
	out, err := UniversalAnalyzer{}.Analyze(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	large := []sourceFile{}
	large = scopeToRootModule(out, root, large)

	// The nested module and its sub-module are excluded; root + src kept.
	for _, c := range out.Chunks {
		if strings.HasPrefix(c.RelPath, "nested/") {
			t.Errorf("nested-module chunk leaked: %s (module %s)", c.RelPath, c.ModuleID)
		}
	}
	seenFiles := map[string]bool{}
	for _, c := range out.Chunks {
		seenFiles[c.RelPath] = true
	}
	if !seenFiles["main.go"] {
		t.Error("root-module file main.go was excluded")
	}
	if !seenFiles["src/small.go"] {
		t.Error("marker-less subdir file src/small.go was excluded")
	}

	// Totals reflect only the kept chunks. go.mod itself is a 1-line
	// file that produces a chunk, so the kept files are go.mod, main.go,
	// and src/small.go.
	wantFiles := 3
	if out.TotalFiles != wantFiles {
		t.Errorf("TotalFiles = %d, want %d", out.TotalFiles, wantFiles)
	}
	if out.EffTotalLines <= 0 {
		t.Errorf("EffTotalLines = %d, want > 0", out.EffTotalLines)
	}

	// Excluded modules are gone from the module list.
	for _, m := range out.Modules {
		if m.ID == "nested" || m.ID == "nested/deep" {
			t.Errorf("excluded module survived: %s", m.ID)
		}
	}
}

// TestStudyDir_ScopeToRootModule_NoMarkerAtRootIsNoOp confirms the
// filter is a no-op when the studied root has no language-root marker —
// a plain directory of scripts has no objective module boundary to
// enforce, so nothing is excluded.
func TestStudyDir_ScopeToRootModule_NoMarkerAtRootIsNoOp(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"a.sh":          "echo a\n",
		"sub/b.sh":      "echo b\n",
		"nested/go.mod": "module example.com/nested\n",
		"nested/x.go":   "package nested\nvar _ = 1\n",
	})
	out, err := UniversalAnalyzer{}.Analyze(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	preChunks := len(out.Chunks)
	preMods := len(out.Modules)
	preEff := out.EffTotalLines
	large := []sourceFile{}
	large = scopeToRootModule(out, root, large)

	if len(out.Chunks) != preChunks {
		t.Errorf("chunks changed: %d → %d (should be no-op without root marker)", preChunks, len(out.Chunks))
	}
	if len(out.Modules) != preMods {
		t.Errorf("modules changed: %d → %d", preMods, len(out.Modules))
	}
	if out.EffTotalLines != preEff {
		t.Errorf("EffTotalLines changed: %d → %d", preEff, out.EffTotalLines)
	}
}

// TestStudyDir_ScopeToRootModule_StudyingNestedModuleDirectly confirms
// that pointing study at a nested module path scopes to THAT module:
// its own go.mod is the root marker, and its own sub-modules are
// excluded. This is how the agent studies a specific subproject.
func TestStudyDir_ScopeToRootModule_StudyingNestedModuleDirectly(t *testing.T) {
	root := writeDirFixture(t, map[string]string{
		"go.mod":             "module example.com/m\n",
		"main.go":            "package main\nfunc main() {}\n",
		"sub/go.mod":         "module example.com/m/sub\n",
		"sub/a.go":           "package sub\nvar _ = 1\n",
		"sub/inner/go.mod":   "module example.com/m/sub/inner\n",
		"sub/inner/b.go":     "package inner\nvar _ = 1\n",
	})
	subRoot := filepath.Join(root, "sub")
	out, err := UniversalAnalyzer{}.Analyze(context.Background(), subRoot, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	large := []sourceFile{}
	large = scopeToRootModule(out, subRoot, large)

	// sub/ is now the root; sub/a.go is kept, sub/inner/ is excluded.
	seenFiles := map[string]bool{}
	for _, c := range out.Chunks {
		seenFiles[c.RelPath] = true
	}
	if !seenFiles["a.go"] {
		t.Error("root-module file a.go was excluded when studying sub/ directly")
	}
	if seenFiles["inner/b.go"] {
		t.Error("sub-module inner/b.go leaked when studying sub/ directly")
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
