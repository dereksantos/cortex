package outline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny test helper.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOutlineFileUnits: a Go file yields its declarations with correct line
// spans, in file order.
func TestOutlineFileUnits(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\n" + // 1-2
		"type T struct {\n\tX int\n}\n\n" + // 3-5
		"func Foo() {\n\treturn\n}\n\n" + // 7-9
		"func (t *T) Bar() {}\n" // 11
	p := filepath.Join(dir, "f.go")
	writeFile(t, p, src)

	entries, err := Outline(p, 4000)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name       string
		start, end int
	}{
		{"type T", 3, 5},
		{"func Foo", 7, 9},
		{"func (*T) Bar", 11, 11},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		e := entries[i]
		if e.Name != w.name || e.Span != [2]int{w.start, w.end} {
			t.Errorf("entry %d = {%q %v}, want {%q [%d %d]}", i, e.Name, e.Span, w.name, w.start, w.end)
		}
		if e.Path != "" {
			t.Errorf("in-file unit %d should have empty Path, got %q", i, e.Path)
		}
	}

	text, _ := Render(p, 4000)
	if !strings.Contains(text, "L3-5") || !strings.Contains(text, "func Foo") {
		t.Errorf("render missing spans/names:\n%s", text)
	}
}

// TestOutlineChildNamesAlwaysListed: every direct child's name appears in the
// render even under a tiny budget — the load-bearing truncation rule (a relevant
// file can never silently vanish; only its expansion is deferred).
func TestOutlineChildNamesAlwaysListed(t *testing.T) {
	dir := t.TempDir()
	// Several Go files, each with many decls, plus a subdir — far more than a
	// tiny budget can expand.
	var big strings.Builder
	big.WriteString("package p\n")
	for i := 0; i < 40; i++ {
		big.WriteString("func F")
		big.WriteByte(byte('a' + i%26))
		big.WriteString("() {}\n")
	}
	for _, n := range []string{"alpha.go", "bravo.go", "charlie.go"} {
		writeFile(t, filepath.Join(dir, n), big.String())
	}
	writeFile(t, filepath.Join(dir, "sub", "deep.go"), "package sub\nfunc Deep() {}\n")

	text, err := Render(dir, 20) // tiny budget
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha.go", "bravo.go", "charlie.go", "sub/"} {
		if !strings.Contains(text, name) {
			t.Errorf("child %q vanished under tiny budget:\n%s", name, text)
		}
	}
	// And the budget must have deferred SOME expansion with a note.
	if !strings.Contains(text, "to expand") {
		t.Errorf("tiny budget should defer expansion with a note:\n%s", text)
	}
}

// TestOutlineBudgetExpands: a generous budget expands a Go file's symbols inline
// (the breadth-first deepening), a tiny one does not.
func TestOutlineBudgetExpands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nfunc Alpha() {}\nfunc Beta() {}\n")

	full, _ := Render(dir, 4000)
	if !strings.Contains(full, "func Alpha") || !strings.Contains(full, "func Beta") {
		t.Errorf("generous budget should expand symbols:\n%s", full)
	}
	tiny, _ := Render(dir, 1)
	if !strings.Contains(tiny, "a.go") {
		t.Errorf("file name must still appear under budget 1:\n%s", tiny)
	}
}

// TestOutlineDirChildEntries: directory children carry an outlineable Path; files
// have it too so the model can outline deeper.
func TestOutlineDirChildEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.go"), "package p\nfunc X() {}\n")
	writeFile(t, filepath.Join(dir, "sub", "y.go"), "package sub\nfunc Y() {}\n")

	entries, err := Outline(dir, 4000)
	if err != nil {
		t.Fatal(err)
	}
	var sawFile, sawDir bool
	for _, e := range entries {
		if e.Name == "x.go" && e.Path == "x.go" {
			sawFile = true
		}
		if e.Name == "sub/" && e.Path == "sub" {
			sawDir = true
		}
	}
	if !sawFile || !sawDir {
		t.Errorf("missing child entries: file=%v dir=%v\n%+v", sawFile, sawDir, entries)
	}
}

// TestOutlineNonGoSections: a large non-Go file gets a regex/positional outline
// rather than nothing.
func TestOutlineNonGoSections(t *testing.T) {
	dir := t.TempDir()
	var md strings.Builder
	for i := 0; i < 5; i++ {
		md.WriteString("# Heading ")
		md.WriteByte(byte('A' + i))
		md.WriteString("\n\n")
		for j := 0; j < 20; j++ {
			md.WriteString("body line\n")
		}
	}
	p := filepath.Join(dir, "doc.md")
	writeFile(t, p, md.String())
	entries, err := Outline(p, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("markdown should yield heading sections, got %+v", entries)
	}
	if !strings.HasPrefix(entries[0].Name, "# Heading A") {
		t.Errorf("first section = %q, want heading", entries[0].Name)
	}
}

// TestOutlineMissingPath surfaces an error, not a panic.
func TestOutlineMissingPath(t *testing.T) {
	if _, err := Outline(filepath.Join(t.TempDir(), "nope"), 100); err == nil {
		t.Error("expected an error for a missing path")
	}
}
