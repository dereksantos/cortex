package study

import (
	"strings"
	"testing"
)

const astFixture = `// Package foo does things.
package foo

import (
	"fmt"
	"strings"
)

// Greeter greets.
type Greeter struct {
	Name string
}

const maxTries = 3

// Hello returns a greeting.
func Hello(name string) string {
	return fmt.Sprintf("hi %s", strings.TrimSpace(name))
}

// Greet uses the receiver.
func (g *Greeter) Greet() string {
	return Hello(g.Name)
}
`

func TestBuildASTGrid_DeclAlignedChunks(t *testing.T) {
	out, meta, err := BuildASTGrid("/abs/foo.go", "foo.go", []byte(astFixture), ByteGridOpts{})
	if err != nil {
		t.Fatalf("BuildASTGrid: %v", err)
	}
	if len(out.Chunks) < 5 {
		t.Fatalf("want a chunk per top-level decl (+header), got %d", len(out.Chunks))
	}

	// Every chunk has REAL bounds (born refined) and non-empty byte length.
	for _, c := range out.Chunks {
		if !c.Refined {
			t.Errorf("AST chunks must be born Refined, %s wasn't", c.ID)
		}
		if c.ByteLength <= 0 || c.LineStart < 1 || c.LineEnd < c.LineStart {
			t.Errorf("bad bounds: %+v", c)
		}
		if c.Lang != "go" {
			t.Errorf("lang = %q, want go", c.Lang)
		}
	}

	// The symbol inventory names the real declarations.
	names := map[string]string{} // name → kind
	for _, m := range meta {
		names[m.Name] = m.Kind
	}
	for name, kind := range map[string]string{
		"Hello":            "func",
		"(*Greeter) Greet": "func",
		"Greeter":          "type",
		"maxTries":         "const",
	} {
		if names[name] != kind {
			t.Errorf("inventory missing %q (%s); got %v", name, kind, names)
		}
	}
	// A package/header chunk leads.
	if meta[0].Kind != "header" || !strings.Contains(meta[0].Name, "package foo") {
		t.Errorf("first meta should be the package header, got %+v", meta[0])
	}
}

func TestBuildASTGrid_ChunkContainsWholeDecl(t *testing.T) {
	out, meta, err := BuildASTGrid("/abs/foo.go", "foo.go", []byte(astFixture), ByteGridOpts{})
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(astFixture)
	// Find the Hello chunk and confirm its bytes contain the WHOLE function +
	// its doc comment — the coherence guarantee the byte grid can't make.
	var helloID string
	for _, m := range meta {
		if m.Name == "Hello" {
			helloID = m.ChunkID
		}
	}
	for _, c := range out.Chunks {
		if c.ID != helloID {
			continue
		}
		body := string(src[c.ByteOffset : c.ByteOffset+int64(c.ByteLength)])
		if !strings.Contains(body, "// Hello returns a greeting.") {
			t.Errorf("chunk should include the doc comment:\n%s", body)
		}
		if !strings.Contains(body, "func Hello(name string) string {") || !strings.Contains(body, "TrimSpace(name)") {
			t.Errorf("chunk should include the whole function body:\n%s", body)
		}
		return
	}
	t.Fatal("Hello chunk not found")
}

func TestBuildASTGrid_RejectsNonParsing(t *testing.T) {
	if _, _, err := BuildASTGrid("/abs/x.go", "x.go", []byte("package foo\nfunc ("), ByteGridOpts{}); err == nil {
		t.Error("expected a parse error for malformed Go (caller falls back to byte grid)")
	}
}
