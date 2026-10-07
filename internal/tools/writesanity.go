// writesanity.go is issue #224's mechanical lever: after write_file or
// edit_file lands a Go file, a stdlib-only sanity pass inspects the file's
// PACKAGE (not just the file — including its same-package _test.go siblings)
// and appends every name-level problem it finds to the tool result in ONE
// pass, so a session fixes them all in one round instead of clearing compile
// errors one build at a time. The self-dev loop's tick 20261006T074738Z is
// the case: sessions wrote test files against helpers, type shapes and paths
// they never located (seven undefined identifiers surfacing at once; a
// duplicate package-level const discovered build by build; a compile error
// carried from one session into the next). The locate-before-writing
// principle (cmd/cortex/prompt.go) is the guidance; this is the backstop for
// when the guidance is skipped.
//
// What it reports, in one pass:
//
//   - Duplicate package-level declarations (the okResponse-collision class):
//     a package-level name declared in two files, or twice in one.
//   - References to names that exist nowhere (the undefined-identifier
//     class: itoa, editFileCallResp, FinishReason): a bare (non-qualified)
//     reference that nothing can account for — neither a package-level
//     declaration in any parsed package file, nor a universe name
//     (builtins, error, any), nor an identifier declared anywhere in the
//     written file itself.
//
// Deliberately NO full go/types pass (the plan's "fast type-check where it
// succeeds" clause is answered by a cheaper, quieter mechanism): a type-check
// of a real package needs the module's whole dependency graph — the exported
// data of every non-stdlib import — which is exactly the slow, budget-eating
// half of the compile-error loop this issue is about, and the harness has no
// export data to check against. Name resolution needs no types: the declared
// identifier set of the parsed package answers the undefined-name class for
// the common case.
//
// The soundness boundary, accepted on purpose: the check is CONSERVATIVE.
// A name is reported only when it appears NOWHERE — not declared in the
// package, not in the universe, and not declared anywhere in the written
// file — so a local variable, parameter, label, or struct field can never
// trigger it, and the only false-positive class is a name declared solely in
// a sibling file the parser could not parse (a syntax-broken file); a broken
// sibling must be repaired before the build anyway, and the model's own
// build names the syntax error, so the extra line is noise-on-broken, never
// noise-on-clean. Qualified references (pkg.Name) are not judged — the
// qualifier resolves through the import, which nobody here can see, and a
// broken import path is named precisely by the model's own build.
//
// It is a NOTE, never a veto, exactly like every post-edit observation: the
// write already succeeded and the harness's write path must not become a
// compiler. Silent degradations (result byte-identical to the pre-hook
// behavior): non-Go files, a written file whose package clause or body does
// not parse (syntax errors belong to the model's own build), and packages
// with nothing wrong. Cheapness: one go/parser pass over the package's .go
// files (PackageClauseOnly per sibling, full parse where needed) plus
// linear walks — no subprocess, no network, no import resolution.
package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// writeSanityNote inspects the package of the just-written/edited Go file at
// fsPath (workdir-resolved, absolute) and returns the note to append to the
// tool result, or "" when there is nothing to report. displayName is the
// path as the model wrote it — the note names that, never an absolute path.
// All problems are collected before rendering so ONE note lists every
// undefined name and every collision at once.
func writeSanityNote(fsPath, displayName string) string {
	if !strings.HasSuffix(fsPath, ".go") {
		return "" // the check is Go-specific; every other file's result stays byte-identical
	}
	pkgName := goPackageOf(fsPath)
	if pkgName == "" {
		return "" // the written file's own package clause does not parse: its build names that
	}
	goFiles, testFiles := packageFiles(filepath.Dir(fsPath), fsPath, pkgName)

	fset := token.NewFileSet()
	regular := parseQuiet(fset, goFiles)
	tests := parseQuiet(fset, testFiles)
	if regular[fsPath] == nil && tests[fsPath] == nil {
		return "" // the written file itself does not parse: syntax errors belong to the model's build
	}

	problems := duplicatePackageDecls(fset, regular, tests)
	problems = append(problems, unresolvedReferences(fset, regular, tests, fsPath, pkgName)...)
	if len(problems) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "note: %s: this file's package reports %d name problem(s) — fix them all in one pass instead of one build per error", displayName, len(problems))
	for _, p := range problems {
		fmt.Fprintf(&b, "\n- %s", p)
	}
	return clipNote(b.String())
}

// packageFiles lists the .go files of the package the written file belongs
// to: every non-test .go file declaring pkgName, plus every _test.go file
// declaring the SAME package (an _test.go declaring pkgName_test is the
// external test package — a different unit whose names must not merge with
// or collide against this one, so it is left out). The written file itself
// is always included (it belongs to its own package by construction).
func packageFiles(dir, written, pkgName string) (regular, test []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable directory: the written file alone is still worth
		// checking (a same-file duplicate is reportable).
		if strings.HasSuffix(written, "_test.go") {
			return nil, []string{written}
		}
		return []string{written}, nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		p := filepath.Join(dir, name)
		if p == written {
			continue // appended below, once, in the right bucket
		}
		if goPackageOf(p) != pkgName {
			continue // different package clause (including pkgName_test): not this package
		}
		if strings.HasSuffix(name, "_test.go") {
			test = append(test, p)
		} else {
			regular = append(regular, p)
		}
	}
	if strings.HasSuffix(written, "_test.go") {
		test = append(test, written)
	} else {
		regular = append(regular, written)
	}
	return regular, test
}

// goPackageOf reads a .go file's package clause name. "" when unparseable.
func goPackageOf(path string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
	if err != nil || f == nil || f.Name == nil {
		return ""
	}
	return f.Name.Name
}

// parseQuiet parses each path into a full syntax tree (both checks need the
// bodies), keeping files even when the parser recovered from errors (it
// returns a partial AST alongside the error — good enough for name
// collection). A file yielding nil is dropped.
func parseQuiet(fset *token.FileSet, paths []string) map[string]*ast.File {
	out := make(map[string]*ast.File, len(paths))
	for _, p := range paths {
		f, _ := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if f != nil {
			out[p] = f
		}
	}
	return out
}

// packageLevelNames walks a parsed file's top-level declarations, reporting
// each package-level name (funcs without a receiver, named types, named
// const/var specifiers) with its position. Methods are skipped — their name
// lives in the receiver type's namespace, not the package's — and blank
// names are ignored.
func packageLevelNames(fset *token.FileSet, f *ast.File, report func(kind, name string, pos token.Pos)) {
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			if v.Recv != nil || v.Name.Name == "_" {
				continue
			}
			report("func", v.Name.Name, v.Pos())
		case *ast.GenDecl:
			for _, s := range v.Specs {
				switch sp := s.(type) {
				case *ast.TypeSpec:
					if sp.Name.Name != "_" {
						report("type", sp.Name.Name, sp.Pos())
					}
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if n.Name != "_" {
							report(v.Tok.String(), n.Name, n.Pos())
						}
					}
				}
			}
		}
	}
}

// duplicatePackageDecls finds package-level names declared more than once
// across the package's parsed files, or twice within one file (the
// okResponse class). The note lists the names in sorted order with every
// declaration site (base file:line).
func duplicatePackageDecls(fset *token.FileSet, regular, tests map[string]*ast.File) []string {
	type declSite struct {
		file string
		line int
	}
	sites := map[string][]declSite{}
	record := func(file string) func(kind, name string, pos token.Pos) {
		return func(kind, name string, pos token.Pos) {
			key := kind + " " + name
			sites[key] = append(sites[key], declSite{filepath.Base(file), fset.Position(pos).Line})
		}
	}
	for file, f := range regular {
		packageLevelNames(fset, f, record(file))
	}
	for file, f := range tests {
		packageLevelNames(fset, f, record(file))
	}
	keys := make([]string, 0, len(sites))
	for k, v := range sites {
		if len(v) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		where := make([]string, 0, len(sites[key]))
		for _, s := range sites[key] {
			where = append(where, fmt.Sprintf("%s:%d", s.file, s.line))
		}
		out = append(out, fmt.Sprintf("%s is declared %d times (%s) — reuse one declaration or rename the new one", key, len(sites[key]), strings.Join(where, ", ")))
	}
	return out
}

// unresolvedReferences reports the bare identifiers the file just written
// references that nothing can account for: not a package-level declaration
// in any parsed package file (test siblings included — a test file calling a
// helper declared in another test file is #224's exact class), not a
// universe name, not the name of one of the file's imports, and not DECLARED
// anywhere in the written file itself (locals, parameters, labels, struct
// fields — anything the file declares is assumed available where it is used;
// see the conservative soundness note in the package comment).
//
// Identifiers that DECLARE something (the file's own package-level and local
// definitions, import names) are never references. Selector expressions are
// not judged at all (both X and Sel in X.Sel): Sel resolves in X's namespace
// and X resolves through its inferred type — neither visible without
// type-checking the imports (see the package comment), and with pass 1 any
// local an expression binds is already exempt. Struct field names and
// interface method names are declarations in their own scopes and are
// skipped.
//
// A file with a dot import gets NO undefined report at all: every bare name
// could be a dot-imported symbol, and guessing which would manufacture the
// false positives this check must never produce. (The duplicate pass still
// runs — it needs no import knowledge.)
func unresolvedReferences(fset *token.FileSet, regular, tests map[string]*ast.File, written, pkgName string) []string {
	file := regular[written]
	if file == nil {
		file = tests[written]
	}
	if file == nil {
		return nil
	}

	// The package name set: every package-level declaration name in every
	// parsed file of the package.
	pkgNames := map[string]bool{}
	collect := func(f *ast.File) {
		packageLevelNames(fset, f, func(_, name string, _ token.Pos) { pkgNames[name] = true })
	}
	for _, f := range regular {
		collect(f)
	}
	for _, f := range tests {
		collect(f)
	}
	// Every identifier the written file DECLARES, anywhere — locals,
	// parameters, named results, labels, struct fields included — the
	// conservative superset of what may legally appear as a bare
	// identifier in it (pass 1). Pass 2 (below) exempts any name in this
	// set: a name the file itself declares is never reported, which
	// subsumes every scope-visibility question without a scope model.
	// Crucially it is built ONLY from declarations, never from all idents:
	// a file that merely USES a guessed name must not whitelist its own
	// guess.
	declared := identDeclMap(file)
	for _, imp := range file.Imports {
		if imp.Name != nil && imp.Name.Name == "." {
			return nil // dot import: no bare name can be judged undefined
		}
	}

	uses := map[string]int{} // name → first referenced line
	var order []string
	ast.Inspect(file, func(n ast.Node) bool {
		if _, isSel := n.(*ast.SelectorExpr); isSel {
			// Neither side of X.Sel is judged: Sel resolves in X's namespace
			// and X through its inferred type (see the function comment).
			// Stop at the selector node so its idents are never visited as
			// bare uses.
			return false
		}
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		name := id.Name
		// ""/"_" carry no reference; the package-clause ident declares the
		// package rather than referencing it; the file's own package-level
		// names are already in pkgNames (collected from every parsed file
		// including this one).
		if name == "" || name == "_" || name == pkgName {
			return true
		}
		if pkgNames[name] || nameIsUniverse(name) || declared[name] {
			return true
		}
		if _, seen := uses[name]; !seen {
			uses[name] = fset.Position(id.Pos()).Line
			order = append(order, name)
		}
		return true
	})
	if len(order) == 0 {
		return nil
	}
	sort.Strings(order) // deterministic note regardless of walk order ties
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, fmt.Sprintf("%q is undefined in package %s (first referenced at line %d) — grep the package for the real name or locate the helper you meant to call", name, pkgName, uses[name]))
	}
	return out
}

// identDeclMap collects every identifier in f that DECLARES something — the
// names a legal bare reference may resolve to inside this very file: import
// names, named types, field names (struct fields, interface methods,
// parameters, receivers, results), func names, label declarations, and the
// new variables of := / range-define / type-switch-define assignments. Uses
// of a name never enter the map — only declarations do.
func identDeclMap(f *ast.File) map[string]bool {
	decls := map[string]bool{}
	mark := func(ids ...*ast.Ident) {
		for _, id := range ids {
			if id != nil {
				decls[id.Name] = true
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ImportSpec:
			if v.Name != nil {
				if v.Name.Name != "_" && v.Name.Name != "." {
					decls[v.Name.Name] = true // a named import: qualified by its alias
				}
			} else if upath, err := strconv.Unquote(v.Path.Value); err == nil {
				// An unnamed import is qualified by its path's last element (the
				// package NAME the import resolves to — nothing here resolves it,
				// so every such element that is a valid identifier is exempt:
				// "fmt", "strings", "my/pkg/httputil" → httputil). A path whose
				// last element is not an identifier ("go-critic") can never be a
				// qualifier.
				if el := pathElement(upath); el != "" && isIdentName(el) {
					decls[el] = true
				}
			}
		case *ast.TypeSpec:
			mark(v.Name)
		case *ast.ValueSpec:
			// A var/const declaration's names (incl. grouped blocks and
			// `var a, b = 1, ""`): declarations, not references.
			mark(v.Names...)
		case *ast.Field:
			mark(v.Names...) // struct fields, interface methods, params, receivers, results — all declarations
		case *ast.FuncDecl:
			mark(v.Name)
		case *ast.LabeledStmt:
			mark(v.Label)
		case *ast.AssignStmt:
			if v.Tok == token.DEFINE {
				for _, l := range v.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						mark(id)
					}
				}
			}
		case *ast.RangeStmt:
			if v.Tok == token.DEFINE {
				if id, ok := v.Key.(*ast.Ident); ok {
					mark(id)
				}
				if id, ok := v.Value.(*ast.Ident); ok {
					mark(id)
				}
			}
		case *ast.TypeSwitchStmt:
			if a, ok := v.Assign.(*ast.AssignStmt); ok && a.Tok == token.DEFINE {
				for _, l := range a.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						mark(id)
					}
				}
			}
		}
		return true
	})
	return decls
}

// pathElement is the last slash-separated element of an import path ("").
func pathElement(importPath string) string {
	if i := strings.LastIndexByte(importPath, '/'); i >= 0 {
		return importPath[i+1:]
	}
	return importPath
}

// isIdentName reports whether s is a usable Go identifier (letters/digits/
// underscore, not starting with a digit, non-empty) — the shape test that
// tells a qualifier-able import path element ("httputil") from one that is
// not ("go-critic", "v2").
func isIdentName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || unicode.IsLetter(r):
		case unicode.IsDigit(r) && i > 0:
		default:
			return false
		}
	}
	return true
}

// nameIsUniverse reports whether name resolves without any declaration: a
// universe name from go/types (builtins, error, any, byte/rune/int...) —
// nil, true, false and iota are in go/types' universe lookup, so no extra
// spelling is needed here.
func nameIsUniverse(name string) bool {
	return types.Universe.Lookup(name) != nil
}
