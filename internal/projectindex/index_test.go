package projectindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildExtractsGoSymbols(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\ntype Widget struct{}\n\nfunc New() *Widget { return nil }\n\nfunc (w *Widget) Render() string { return \"\" }\n")
	write("sub/b.go", "package sub\n\nfunc Helper() {}\n")
	write("readme.md", "# docs\njust text\n")
	write(".git/config", "[core]\n")                  // hard-excluded dir
	write("secrets.json", "{\"k\":\"v\"}\n")          // sensitive file
	write("vendor/x/v.go", "package x\nfunc V(){}\n") // hard-excluded dir

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]File{}
	for _, f := range ix.Files {
		got[f.Path] = f
	}

	if _, ok := got["a.go"]; !ok {
		t.Fatalf("a.go missing; files=%v", keys(got))
	}
	if _, ok := got["readme.md"]; !ok {
		t.Error("non-Go file readme.md should still be listed")
	}
	if _, ok := got["secrets.json"]; ok {
		t.Error("sensitive file secrets.json should be excluded")
	}
	if _, ok := got[".git/config"]; ok {
		t.Error(".git contents must be excluded")
	}
	if _, ok := got["vendor/x/v.go"]; ok {
		t.Error("vendor/ must be excluded")
	}

	a := got["a.go"]
	names := symNames(a.Symbols)
	for _, want := range []string{"Widget", "New", "(*Widget) Render"} {
		if !contains(names, want) {
			t.Errorf("a.go symbols missing %q; got %v", want, names)
		}
	}
	if got["readme.md"].Symbols != nil {
		t.Error("non-Go file should have no symbols")
	}
}

func TestRenderIsCompactAndGrouped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\ntype T struct{}\nfunc F(){}\n"), 0644)

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := ix.Render()
	if !strings.Contains(out, "1 files") {
		t.Errorf("header missing file count: %q", out)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "T:2") || !strings.Contains(out, "F:3") {
		t.Errorf("render missing file/symbols with lines: %q", out)
	}
	// Types lead funcs on the symbol line.
	if i, j := strings.Index(out, "T:2"), strings.Index(out, "F:3"); i < 0 || j < 0 || i > j {
		t.Errorf("expected type before func; render=%q", out)
	}
}

func TestSingleFileSkeleton(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\nconst Greeting = \"hi\"\n\nvar registry = map[string]int{}\n\ntype Server struct{}\n\nfunc (s *Server) Start() {}\n\nfunc helper() {}\n"
	p := filepath.Join(dir, "srv.go")
	if err := os.WriteFile(p, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if !ix.SingleFile || len(ix.Files) != 1 {
		t.Fatalf("expected single-file index, got SingleFile=%v files=%d", ix.SingleFile, len(ix.Files))
	}
	out := ix.Render()
	// const/var appear in the skeleton (they're filtered only from the dir view).
	for _, want := range []string{"const", "Greeting", "var", "registry", "type", "Server", "(*Server) Start", "helper"} {
		if !strings.Contains(out, want) {
			t.Errorf("skeleton missing %q; got:\n%s", want, out)
		}
	}
	// Declarations stay in file order: const → var → type → method → func.
	order := []string{"Greeting", "registry", "Server", "Start", "helper"}
	last := -1
	for _, sym := range order {
		i := strings.Index(out, sym)
		if i < 0 {
			t.Fatalf("missing %q", sym)
		}
		if i < last {
			t.Errorf("declaration order broken at %q:\n%s", sym, out)
		}
		last = i
	}
}

func TestDirViewOmitsConstVar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package a\nconst Secret = 1\nvar table = 2\ntype T struct{}\nfunc F(){}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\nfunc G(){}\n"), 0644)

	ix, _ := Build(dir)
	out := ix.Render() // multi-file → compact directory view
	if strings.Contains(out, "Secret") || strings.Contains(out, "table") {
		t.Errorf("directory view should omit const/var; got:\n%s", out)
	}
	if !strings.Contains(out, "T:") || !strings.Contains(out, "F:") {
		t.Errorf("directory view should keep funcs/types; got:\n%s", out)
	}
}

// TestRenderRepoSize is an env-gated preview of the real repo index, to eyeball
// size and shape. Run: PREVIEW=1 go test ./internal/projectindex -run RepoSize -v
func TestRenderRepoSize(t *testing.T) {
	if os.Getenv("PREVIEW") == "" {
		t.Skip("set PREVIEW=1 to print the repo index size/preview")
	}
	ix, err := Build("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := ix.Render()
	t.Logf("rendered %d bytes (~%d tokens), %d files", len(out), len(out)/4, len(ix.Files))
	head := out
	if len(head) > 2000 {
		head = head[:2000]
	}
	t.Logf("preview:\n%s", head)
}

func keys(m map[string]File) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	return k
}
func symNames(syms []Symbol) []string {
	var n []string
	for _, s := range syms {
		n = append(n, s.Name)
	}
	return n
}
func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
