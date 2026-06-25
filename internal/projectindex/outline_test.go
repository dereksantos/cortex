package projectindex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutlineMarkdownHeadings(t *testing.T) {
	dir := t.TempDir()
	src := "# Title\nintro\n\n## Setup\nrun it\n\n## Usage\nuse it\n"
	p := filepath.Join(dir, "README.md")
	if err := os.WriteFile(p, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	syms := ix.Files[0].Symbols
	if len(syms) != 3 {
		t.Fatalf("want 3 heading sections, got %d: %+v", len(syms), syms)
	}
	if syms[0].Name != "# Title" || syms[0].Kind != "head" {
		t.Errorf("first section = %+v, want '# Title' head", syms[0])
	}
	// ## Setup spans line 4 until the line before ## Usage (line 6).
	if syms[1].Line != 4 || syms[1].EndLine != 6 {
		t.Errorf("Setup span = L%d-%d, want L4-6", syms[1].Line, syms[1].EndLine)
	}
}

func TestOutlinePythonDecls(t *testing.T) {
	dir := t.TempDir()
	src := "import os\n\ndef alpha():\n    return 1\n\nclass Beta:\n    def m(self):\n        pass\n"
	p := filepath.Join(dir, "x.py")
	if err := os.WriteFile(p, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	ix, _ := Build(p)
	names := map[string]bool{}
	for _, s := range ix.Files[0].Symbols {
		names[s.Name] = true
	}
	if !names["def alpha():"] || !names["class Beta:"] {
		t.Errorf("python outline missing decls; got %v", names)
	}
}

func TestOutlinePositionalFallback(t *testing.T) {
	dir := t.TempDir()
	// A structureless file longer than positionalMinWindow → positional sections.
	var sb strings.Builder
	for i := 0; i < positionalMinWindow*3; i++ {
		fmt.Fprintf(&sb, "noise %d\n", i)
	}
	p := filepath.Join(dir, "blob.log")
	if err := os.WriteFile(p, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	ix, _ := Build(p)
	syms := ix.Files[0].Symbols
	if len(syms) < 2 {
		t.Fatalf("expected positional sections, got %d", len(syms))
	}
	if syms[0].Kind != "lines" || syms[0].Line != 1 {
		t.Errorf("first positional section = %+v, want kind 'lines' at L1", syms[0])
	}
	// Sections are contiguous and cover the file.
	last := syms[len(syms)-1]
	if last.EndLine != positionalMinWindow*3 {
		t.Errorf("last section ends at L%d, want %d", last.EndLine, positionalMinWindow*3)
	}
}

func TestOutlineSkippedInDirView(t *testing.T) {
	// A directory walk must NOT compute non-Go outlines (full=false) — the dir
	// view is Go-symbol-only, so outlines there would be wasted work.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc F(){}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "doc.md"), []byte("# H1\nx\n\n## H2\ny\n"), 0644)
	ix, _ := Build(dir)
	for _, f := range ix.Files {
		if f.Path == "doc.md" && f.Symbols != nil {
			t.Errorf("dir-view should not outline non-Go files; doc.md got %+v", f.Symbols)
		}
	}
}

func TestOutlineCapsSections(t *testing.T) {
	// Far more boundaries than the cap → coalesced to <= outlineMaxSections.
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < outlineMaxSections*3; i++ {
		fmt.Fprintf(&sb, "## Section %d\nbody\n\n", i)
	}
	p := filepath.Join(dir, "big.md")
	if err := os.WriteFile(p, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	ix, _ := Build(p)
	if n := len(ix.Files[0].Symbols); n > outlineMaxSections {
		t.Errorf("outline has %d sections, want <= %d", n, outlineMaxSections)
	}
}
