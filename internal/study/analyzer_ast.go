package study

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// AST boundary producer (Go): the higher-fidelity alternative to the byte grid
// for Go source. Instead of cutting a file into fixed byte windows that split
// declarations anywhere, it parses the file and lays one chunk per TOP-LEVEL
// declaration — a whole func/type/const/var/import block, with its leading doc
// comment — so a sampled region is always a coherent unit the model can fully
// understand and cite precisely. Bounds are REAL (from the parser), so chunks
// are born Refined (no lazy line-snapping needed).
//
// This is the Go-only MVP of language-agnostic AST chunking
// (docs/working-memory-study.md → AST): prove the fidelity gain on our own
// codebase with zero new deps (stdlib go/parser) before taking on tree-sitter's
// CGo + grammar weight for other languages. The output is a drop-in
// BoundaryOutput the existing HierarchicalSampler consumes unchanged.

// declMeta carries the symbol a chunk corresponds to — the inventory the AST
// affords (used by the eval to check the digest names REAL symbols, and a hook
// for future symbol-outline injection).
type declMeta struct {
	ChunkID string
	Name    string // "Foo", "(T) Method", "import", "const(...)", ...
	Kind    string // "func" | "type" | "const" | "var" | "import" | "header"
	Line    int
}

// BuildASTGrid lays a declaration-aligned grid over one Go file's source and
// returns a BoundaryOutput plus the symbol inventory. Returns an error when the
// source doesn't parse (caller falls back to the byte grid) — partial/erroring
// files are not chunked here in the MVP.
func BuildASTGrid(absPath, relPath string, src []byte, opts ByteGridOpts) (*BoundaryOutput, []declMeta, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("study: ast parse %s: %w", relPath, err)
	}

	out := &BoundaryOutput{ProjectRoot: absPath, TotalFiles: 1, FileHashes: map[string]string{}}
	size := int64(len(src))
	out.StateHash = byteGridStateHash(relPath, size, opts.ModTimeUnix)
	out.RNGSeed = seedFrom(out.StateHash, opts.Salt)
	out.FileHashes[relPath] = byteGridDriftKey(size, opts.ModTimeUnix)
	if size == 0 {
		return out, nil, nil
	}

	type span struct {
		start, end int // byte offsets
		name, kind string
		line       int
	}
	var spans []span

	// Header chunk: package clause + file doc + everything up to the first decl
	// (imports usually). Captures package-level context the model needs.
	firstDeclOff := int(size)
	if len(f.Decls) > 0 {
		firstDeclOff = fset.Position(declStart(f.Decls[0])).Offset
	}
	if firstDeclOff > 0 {
		spans = append(spans, span{0, firstDeclOff, "package " + f.Name.Name, "header", 1})
	}

	for _, d := range f.Decls {
		so := fset.Position(declStart(d)).Offset
		eo := fset.Position(d.End()).Offset
		if eo <= so {
			continue
		}
		spans = append(spans, span{so, eo, declName(d), declKind(d), fset.Position(d.Pos()).Line})
	}
	if len(spans) == 0 {
		return out, nil, nil
	}

	bands := opts.Bands
	if bands <= 0 {
		bands = byteGridDefaultBands
	}
	if bands > len(spans) {
		bands = len(spans)
	}

	chunks := make([]Chunk, 0, len(spans))
	meta := make([]declMeta, 0, len(spans))
	for i, s := range spans {
		length := s.end - s.start
		// Real 1-indexed lines from byte offsets.
		ls := lineAt(src, s.start)
		le := lineAt(src, s.end-1)
		eff := nonBlankLines(src[s.start:s.end])
		if eff < 1 {
			eff = 1
		}
		id := byteChunkID(relPath, int64(s.start), length)
		band := i * bands / len(spans)
		chunks = append(chunks, Chunk{
			ID:         id,
			Path:       absPath,
			RelPath:    relPath,
			LineStart:  ls,
			LineEnd:    le,
			ByteOffset: int64(s.start),
			ByteLength: length,
			EffLines:   eff,
			EstTokens:  length / studyCharsPerToken,
			ModuleID:   fmt.Sprintf("band-%02d", band),
			Lang:       "go",
			Refined:    true, // real bounds from the parser
		})
		meta = append(meta, declMeta{ChunkID: id, Name: s.name, Kind: s.kind, Line: ls})
	}

	// Roll chunks into band modules (same shape as the byte grid, so the
	// sampler's per-module anti-coverage bias spreads draws across the file).
	modIdx := map[string]int{}
	var modules []Module
	effTotal := 0
	for _, c := range chunks {
		effTotal += c.EffLines
		mi, ok := modIdx[c.ModuleID]
		if !ok {
			mi = len(modules)
			modIdx[c.ModuleID] = mi
			modules = append(modules, Module{ID: c.ModuleID, RootPath: absPath, Files: 1})
		}
		modules[mi].ChunkIDs = append(modules[mi].ChunkIDs, c.ID)
		modules[mi].EffLines += c.EffLines
		modules[mi].Lines += c.EffLines
	}

	out.Chunks = chunks
	out.Modules = modules
	out.EffTotalLines = effTotal
	out.TotalLines = effTotal
	return out, meta, nil
}

// declStart returns the position a declaration's chunk should start at — its
// leading doc comment when present, else the declaration keyword.
func declStart(d ast.Decl) token.Pos {
	switch v := d.(type) {
	case *ast.FuncDecl:
		if v.Doc != nil {
			return v.Doc.Pos()
		}
	case *ast.GenDecl:
		if v.Doc != nil {
			return v.Doc.Pos()
		}
	}
	return d.Pos()
}

// declName returns a human label for the declaration (the symbol inventory).
func declName(d ast.Decl) string {
	switch v := d.(type) {
	case *ast.FuncDecl:
		if v.Recv != nil && len(v.Recv.List) > 0 {
			return "(" + recvType(v.Recv.List[0].Type) + ") " + v.Name.Name
		}
		return v.Name.Name
	case *ast.GenDecl:
		// First named spec, plus a "+N" when the block declares several.
		names := genDeclNames(v)
		switch {
		case len(names) == 0:
			return v.Tok.String()
		case len(names) == 1:
			return names[0]
		default:
			return fmt.Sprintf("%s +%d", names[0], len(names)-1)
		}
	}
	return "?"
}

func declKind(d ast.Decl) string {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return "func"
	case *ast.GenDecl:
		return v.Tok.String() // "import" | "const" | "type" | "var"
	}
	return "decl"
}

func genDeclNames(g *ast.GenDecl) []string {
	var out []string
	for _, s := range g.Specs {
		switch sp := s.(type) {
		case *ast.TypeSpec:
			out = append(out, sp.Name.Name)
		case *ast.ValueSpec:
			for _, n := range sp.Names {
				out = append(out, n.Name)
			}
		}
	}
	return out
}

func recvType(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return "*" + recvType(v.X)
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr: // generic receiver T[U]
		return recvType(v.X)
	}
	return "?"
}

// lineAt returns the 1-indexed line containing byte offset off.
func lineAt(src []byte, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	return 1 + bytes.Count(src[:off], []byte{'\n'})
}

// nonBlankLines counts lines with any non-whitespace content.
func nonBlankLines(b []byte) int {
	n := 0
	for _, ln := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(ln)) > 0 {
			n++
		}
	}
	return n
}
