package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// writeFixture creates a deterministic fixture project under tempDir
// and returns the project root path. The fixture exercises every
// determinism + module-marker + sensitive-filter contract.
func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// .gitignore — exclude tmp/
	mustWrite(t, root, ".gitignore", "tmp/\n*.log\n")

	// Top-level marker: go.mod (this is the root module).
	mustWrite(t, root, "go.mod", "module example.com/m\n")

	// Sensitive: .env should be excluded (layer 1).
	mustWrite(t, root, ".env", "SECRET=abc123\n")
	mustWrite(t, root, ".env.example", "SECRET=changeme\n") // allowed (template)

	// Sensitive: a notes file containing PEM-style content — should
	// be excluded by layer 3 magic-byte sniff.
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEogIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----\n"
	mustWrite(t, root, "notes_with_key.txt", pem)

	// Gitignored: tmp/ — should never be entered.
	mustWrite(t, root, "tmp/cache.dat", "junk")

	// Source files at root (assigned to "." module — go.mod is there).
	mustWrite(t, root, "main.go", "package main\n\nfunc main() {}\n")

	// 50-line file: 50 short lines, no CRLF.
	mustWrite(t, root, "src/small.go", "package src\n"+strings.Repeat("var _ = 1\n", 49))

	// 1000-line file: 1000 short lines. With WindowLines=400 +
	// overlap=40, step=360, so chunk count = ceil((1000-400)/360)+1
	// = ceil(600/360)+1 = 2+1 = 3.
	mustWrite(t, root, "src/big.go", "package src\n"+strings.Repeat("var _ = 1\n", 999))

	// CRLF file: counts the same as LF.
	mustWrite(t, root, "src/crlf.md", "alpha\r\nbravo\r\ncharlie\r\n")

	// No-final-newline file.
	mustWrite(t, root, "src/no_final_nl.txt", "line1\nline2\nline3")

	// Mixed indent file.
	mustWrite(t, root, "src/mixed.py", "def f():\n\treturn 1\ndef g():\n    return 2\n")

	// Nested module: package.json under sub/ at depth 1 — nearest
	// ancestor wins, so sub/ files belong to "sub" module even
	// though root has go.mod.
	mustWrite(t, root, "sub/package.json", "{}\n")
	mustWrite(t, root, "sub/index.js", "module.exports = {};\n")

	// Tie-break case: same-depth Makefile vs go.mod inside a deep
	// dir. Language-root precedence should mean the module marker
	// is "go.mod" (the language-root), not "Makefile".
	mustWrite(t, root, "deep/go.mod", "module example.com/m/deep\n")
	mustWrite(t, root, "deep/Makefile", "all:\n\techo hi\n")
	mustWrite(t, root, "deep/lib.go", "package deep\n\nfunc f() {}\n")

	return root
}

func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestUniversal_SensitiveExclusion(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Verify no sensitive paths leaked into the chunks.
	for _, c := range out.Chunks {
		if strings.Contains(c.RelPath, ".env") && !strings.HasSuffix(c.RelPath, ".example") {
			t.Errorf("forbidden .env in chunks: %s", c.RelPath)
		}
		if c.RelPath == "notes_with_key.txt" {
			t.Errorf("magic-byte sensitive file leaked: %s", c.RelPath)
		}
		if strings.HasPrefix(c.RelPath, "tmp/") {
			t.Errorf("gitignored tmp/ leaked: %s", c.RelPath)
		}
	}
}

func TestUniversal_ModuleAssignment_NearestWins(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// sub/index.js MUST belong to "sub", not "." (root go.mod) —
	// nearest-ancestor wins.
	var subFound, rootFound bool
	for _, c := range out.Chunks {
		if c.RelPath == "sub/index.js" {
			subFound = true
			if c.ModuleID != "sub" {
				t.Errorf("sub/index.js ModuleID = %q, want %q", c.ModuleID, "sub")
			}
		}
		if c.RelPath == "main.go" {
			rootFound = true
			if c.ModuleID != "." {
				t.Errorf("main.go ModuleID = %q, want %q", c.ModuleID, ".")
			}
		}
	}
	if !subFound {
		t.Error("sub/index.js chunk not produced")
	}
	if !rootFound {
		t.Error("main.go chunk not produced")
	}
}

func TestUniversal_ModuleMarker_LanguageRootPrecedence(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// deep/ has BOTH go.mod and Makefile at the same depth.
	// Language-root precedence: go.mod wins.
	var found bool
	for _, m := range out.Modules {
		if m.ID == "deep" {
			found = true
			if m.MarkerName != "go.mod" {
				t.Errorf("deep module MarkerName = %q, want %q (language-root precedence)", m.MarkerName, "go.mod")
			}
		}
	}
	if !found {
		t.Error("deep module not present in output")
	}
}

func TestUniversal_ChunkSizes_HonorWindowKnob(t *testing.T) {
	root := writeFixture(t)
	a1 := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out1, err := a1.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze 400: %v", err)
	}
	a2 := UniversalAnalyzer{WindowLines: 200, WindowOverlap: 20}
	out2, err := a2.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze 200: %v", err)
	}

	// Halving the window should roughly double total chunks (the
	// exact factor depends on file sizes, but it must strictly
	// increase).
	if len(out2.Chunks) <= len(out1.Chunks) {
		t.Errorf("smaller window did not produce more chunks: 200→%d 400→%d",
			len(out2.Chunks), len(out1.Chunks))
	}

	// State hash unchanged across knob changes — it's a function of
	// file content/size/mtime, not analyzer config.
	if out1.StateHash != out2.StateHash {
		t.Errorf("StateHash should be independent of window knobs: %q vs %q",
			out1.StateHash, out2.StateHash)
	}
}

func TestUniversal_BigFileMultiChunk(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// src/big.go should produce multiple chunks (1000 lines, win=400,
	// overlap=40, step=360 → ceil((1000-400)/360)+1 = 3 chunks).
	bigChunks := 0
	for _, c := range out.Chunks {
		if c.RelPath == "src/big.go" {
			bigChunks++
		}
	}
	if bigChunks < 2 {
		t.Errorf("big.go produced %d chunks, want ≥ 2", bigChunks)
	}
}

func TestUniversal_Determinism(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40, Salt: "test"}
	out1, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze 1: %v", err)
	}
	out2, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze 2: %v", err)
	}

	if out1.StateHash != out2.StateHash {
		t.Errorf("StateHash not deterministic: %q vs %q", out1.StateHash, out2.StateHash)
	}
	if out1.RNGSeed != out2.RNGSeed {
		t.Errorf("RNGSeed not deterministic: %d vs %d", out1.RNGSeed, out2.RNGSeed)
	}
	if len(out1.Chunks) != len(out2.Chunks) {
		t.Fatalf("chunk count differs: %d vs %d", len(out1.Chunks), len(out2.Chunks))
	}
	for i := range out1.Chunks {
		if out1.Chunks[i].ID != out2.Chunks[i].ID {
			t.Errorf("chunk[%d] ID drift: %q vs %q", i, out1.Chunks[i].ID, out2.Chunks[i].ID)
		}
	}
}

func TestUniversal_SaltAffectsRNGSeed(t *testing.T) {
	root := writeFixture(t)
	a1 := UniversalAnalyzer{Salt: "a"}
	a2 := UniversalAnalyzer{Salt: "b"}
	o1, err := a1.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze a: %v", err)
	}
	o2, err := a2.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze b: %v", err)
	}
	if o1.RNGSeed == o2.RNGSeed {
		t.Errorf("salt did not change RNGSeed: %d", o1.RNGSeed)
	}
	// StateHash must NOT depend on salt.
	if o1.StateHash != o2.StateHash {
		t.Errorf("salt affected StateHash (must not): %q vs %q", o1.StateHash, o2.StateHash)
	}
}

func TestUniversal_LineCounting_CRLF(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// src/crlf.md has 3 lines (alpha, bravo, charlie). One chunk.
	for _, c := range out.Chunks {
		if c.RelPath == "src/crlf.md" {
			if c.LineEnd-c.LineStart+1 != 3 {
				t.Errorf("CRLF file line span = %d, want 3", c.LineEnd-c.LineStart+1)
			}
			return
		}
	}
	t.Error("CRLF chunk not produced")
}

func TestUniversal_LineCounting_NoFinalNewline(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, c := range out.Chunks {
		if c.RelPath == "src/no_final_nl.txt" {
			if c.LineEnd-c.LineStart+1 != 3 {
				t.Errorf("no-final-NL file line span = %d, want 3", c.LineEnd-c.LineStart+1)
			}
			return
		}
	}
	t.Error("no-final-NL chunk not produced")
}

func TestUniversal_EmptyProject(t *testing.T) {
	root := t.TempDir()
	a := UniversalAnalyzer{}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze empty: %v", err)
	}
	if len(out.Chunks) != 0 {
		t.Errorf("expected 0 chunks for empty project, got %d", len(out.Chunks))
	}
	if out.TotalFiles != 0 {
		t.Errorf("expected 0 files, got %d", out.TotalFiles)
	}
}

func TestUniversal_SiblingEdges(t *testing.T) {
	root := writeFixture(t)
	a := UniversalAnalyzer{WindowLines: 400, WindowOverlap: 40}
	out, err := a.Analyze(context.Background(), root, projectscan.LoadIgnoreSet(root))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Modules: ".", "sub", "deep", "src" (top-level fallback). All
	// share project root as parent → siblings. Expect (n choose 2) * 2
	// directed edges.
	if len(out.Edges) == 0 {
		t.Error("expected sibling edges, got 0")
	}
	for _, e := range out.Edges {
		if e.Kind != "fs_dir" {
			t.Errorf("unexpected edge kind: %s", e.Kind)
		}
		if e.FromModuleID == e.ToModuleID {
			t.Errorf("self-edge: %s", e.FromModuleID)
		}
	}
}
