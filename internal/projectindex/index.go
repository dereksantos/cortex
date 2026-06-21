// Package projectindex builds a compact map of a project: the file tree plus a
// per-file symbol inventory (top-level funcs and types, via go/ast for Go
// files). It's the cheap, high-signal orientation context that lets a model
// navigate a codebase without reading every file — a recursive ls fused with a
// symbol table. Walking respects the same ignore rules as study (projectscan):
// .git, vendor/build caches, .gitignore, and sensitive files are skipped.
package projectindex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// Symbol is one top-level declaration worth navigating to.
type Symbol struct {
	Name string // "Foo" for a plain decl, "(*T) Method" for a method
	Kind string // "func" | "type"
	Line int    // 1-indexed line of the declaration
}

// File is one indexed file: its path, size, and (for Go) symbols.
type File struct {
	Path    string // slash-relative to the index root
	Lines   int
	Symbols []Symbol // nil for non-Go or unparseable files
}

// Index is the project map: every non-ignored file, in path order.
type Index struct {
	Root  string
	Files []File
}

// Build walks root (respecting projectscan's ignore rules) and returns the
// index: every non-ignored file, with go/ast symbols for *.go files.
func Build(root string) (*Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	ix := &Index{Root: abs}
	ignore := projectscan.LoadIgnoreSet(abs)

	// A single-file target is still a valid (tiny) index.
	if !info.IsDir() {
		ix.Files = append(ix.Files, indexFile(abs, filepath.Base(abs)))
		return ix, nil
	}

	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry — skip, don't abort the whole walk
		}
		if d.IsDir() {
			if path != abs && ignore.IsDirExcluded(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.IsFileExcluded(path) {
			return nil
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			return nil
		}
		ix.Files = append(ix.Files, indexFile(path, filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ix.Files, func(i, j int) bool { return ix.Files[i].Path < ix.Files[j].Path })
	return ix, nil
}

// indexFile reads one file, counting lines and extracting Go symbols. A read or
// parse failure degrades to a listing with no symbols — the file still appears.
func indexFile(abs, rel string) File {
	f := File{Path: rel}
	data, err := os.ReadFile(abs)
	if err != nil {
		return f
	}
	f.Lines = countLines(data)
	if strings.HasSuffix(abs, ".go") {
		f.Symbols = goSymbols(data)
	}
	return f
}

// goSymbols parses Go source and returns its top-level funcs and types, in
// declaration order. Methods carry their receiver as "(*T) Method". const/var/
// import are intentionally omitted — they're noise for navigation. A parse
// error yields nil (the file is still listed, just without symbols).
func goSymbols(src []byte) []Symbol {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var syms []Symbol
	for _, decl := range f.Decls {
		switch v := decl.(type) {
		case *ast.FuncDecl:
			name := v.Name.Name
			if v.Recv != nil && len(v.Recv.List) > 0 {
				name = "(" + recvType(v.Recv.List[0].Type) + ") " + v.Name.Name
			}
			syms = append(syms, Symbol{Name: name, Kind: "func", Line: fset.Position(v.Pos()).Line})
		case *ast.GenDecl:
			if v.Tok != token.TYPE {
				continue
			}
			for _, spec := range v.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					syms = append(syms, Symbol{Name: ts.Name.Name, Kind: "type", Line: fset.Position(ts.Pos()).Line})
				}
			}
		}
	}
	return syms
}

// recvType renders a method receiver type, including pointer and generic forms
// (mirrors the study analyzer so labels match elsewhere).
func recvType(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return "*" + recvType(v.X)
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr: // generic receiver T[U]
		return recvType(v.X)
	case *ast.IndexListExpr: // generic receiver T[U, V]
		return recvType(v.X)
	}
	return "?"
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := strings.Count(string(b), "\n")
	if b[len(b)-1] != '\n' {
		n++ // last line has no trailing newline
	}
	return n
}
