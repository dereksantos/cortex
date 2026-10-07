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
	"bufio"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
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
//
// A SIBLING that does not build in the current context is left out too: the
// package under check is what compiles, not what shares a directory. A
// `//go:build`-tagged pair (this repo's `internal/fslock/flock_unix.go` and
// `flock_windows.go` both declare `func flock`; `internal/lineedit`'s
// termios pair declares three names twice) and the `_linux.go` /
// `_windows.go` filename-suffix convention are legal Go that reports a
// collision only when constraints are ignored — telling the model to
// "reuse one declaration" would have it break working platform code, which
// is noise-on-clean in its worst form. Both rules that decide it — the
// filename suffix and the `//go:build` line — are applied against the
// toolchain's own context (see siblingBuildsHere), and a constraint naming a
// tag this process cannot judge excludes nothing: dropping a file the real
// build includes would strip its names from the declared set and invent
// "undefined" reports. The written file is exempt from the filter — the model
// just wrote it, so it is in the package whatever its header says.
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
		if !siblingBuildsHere(dir, name) {
			continue // excluded by build constraints: never compiled alongside this file
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

// siblingBuildsHere reports whether the .go file dir/name can build
// alongside the current package, applying the two rules that decide it and
// nothing else:
//
//   - the filename OS/arch suffix (`flock_windows.go`, `termios_linux_test.go`)
//     must not contradict this build (see filenameSuffixMatches);
//   - a `//go:build` line, or the legacy `// +build` lines, must not
//     positively exclude it, and only a tag this process can judge may do
//     that (see satisfiesBuildConstraints).
//
// Both read their context off go/build rather than re-typing it here.
// Suffixes and constraint tags are judged separately on purpose: go/build's
// MatchFile answers both together, and its one answer is not ours to
// interpret — what matters here is that a sibling may be dropped only on a
// judgement this process can actually make. Dropping a file the real build
// DOES include strips its names from the declared set and invents
// "undefined" reports, the one failure mode this check must never produce;
// keeping a file that in fact does not compile can only ever add a duplicate
// note the model's own build disproves. So everything uncertain keeps the
// file in.
func siblingBuildsHere(dir, name string) bool {
	return satisfiesBuildConstraints(dir, name, filepath.Join(dir, name))
}

// filenameSuffixMatches implements the filename half of go/build's rule: the
// elements after the first underscore, with a trailing `_test` stripped, may
// end in a known GOARCH, a known GOOS, or a known GOOS_GOARCH pair — and then
// only that platform's file builds (`flock_windows.go` builds on Windows
// only; `hooks.go`, `linux.go` and `parser_windows_friendly.go` are
// unconstrained). The name is only a suffix when it is a whole underscore
// element and the file has no further underscore after it, so a name that is
// merely ABOUT a platform is never mistaken for one gated by it.
func filenameSuffixMatches(name string) bool {
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i] // drop ".go" / "_test.go"
	}
	if !strings.Contains(stem, "_") {
		return true // no underscore at all: never platform-suffixed (foo.go, linux.go)
	}
	elems := strings.Split(stem, "_")[1:]
	if n := len(elems); n > 0 && elems[n-1] == "test" {
		elems = elems[:n-1]
	}
	n := len(elems)
	if n == 0 {
		return true
	}
	matches := func(platform string) bool {
		return platform == build.Default.GOOS || platform == build.Default.GOARCH
	}
	if n >= 2 && knownOS[elems[n-2]] && knownArch[elems[n-1]] {
		return matches(elems[n-2]) && matches(elems[n-1])
	}
	if knownOS[elems[n-1]] || knownArch[elems[n-1]] {
		return matches(elems[n-1])
	}
	return true
}

// knownOS and knownArch mirror go/build's own tables of acceptable target
// names (internal/syslist): they decide which filename elements are platform
// suffixes at all. They are deliberately the FULL list of past, present and
// future names rather than the platforms this toolchain can build for — a
// `termios_hurd.go` is somebody's real file even though no build here
// includes it, and treating it as unconstrained would merge it into every
// package it shares a directory with. A name Go has not used yet simply looks
// like an ordinary identifier and stays unconstrained, which is the safe side.
// They also bound what a build CONSTRAINT may be judged on (see judgeableTag),
// where the filename list is the wrong scope for one name: `unix` is a real
// tag that is not a GOOS at all, and is decided separately.
var (
	knownOS = map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true,
		"freebsd": true, "hurd": true, "illumos": true, "ios": true,
		"js": true, "linux": true, "nacl": true, "netbsd": true,
		"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true,
	}
	knownArch = map[string]bool{
		"386": true, "amd64": true, "amd64p32": true, "arm": true,
		"armbe": true, "arm64": true, "arm64be": true, "loong64": true,
		"mips": true, "mipsle": true, "mips64": true, "mips64le": true,
		"mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
		"ppc64le": true, "riscv": true, "riscv64": true, "s390": true,
		"s390x": true, "sparc": true, "sparc64": true, "wasm": true,
	}
)

// satisfiesBuildConstraints reads the `//go:build` line (and legacy
// `// +build` lines) that open path and reports whether they leave the file
// in the build. The header is the run of blank lines and // comments that
// opens the file — peeking its head sees every constraint, and the first line
// that is neither ends the header, exactly as the toolchain reads it. An
// unreadable file and a constraint naming a tag this toolchain does not
// recognize keep the file (see constraintHolds); nothing here splits a package
// on a judgement it cannot make.
//
// The filename's GOOS/GOARCH suffix is the one exclusion decided before the
// header, because go/build applies it to EVERY file whatever its constraints
// say — a `_windows.go` sibling is out of a linux build even with no `//go:build`
// line at all (filenameSuffixMatches). It is decided first and on its own
// terms, so a constraint this check refuses to judge cannot resurrect a file
// the toolchain excludes by name, and a header it cannot read cannot resurrect
// one either.
func satisfiesBuildConstraints(dir, name, path string) bool {
	if !filenameSuffixMatches(name) {
		return false // the toolchain's own filename rule, no judgement needed
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	head, _ := bufio.NewReader(f).Peek(4096)
	if len(head) == 0 {
		return true // unreadable or empty: nothing excludes it that we can see
	}
	for _, line := range strings.Split(string(head), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !constraint.IsGoBuild(line) && !constraint.IsPlusBuild(line) {
			return true // the constraint header is over
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			continue // unparseable directive: keep the file, the build names it
		}
		if !constraintHolds(expr) {
			return false
		}
	}
	return true
}

// constraintHolds evaluates a parsed constraint against this build, and
// refuses to judge it at all when it names a tag outside the platform
// vocabulary. The tag vocabulary is go/build's own context — GOOS, GOARCH,
// the compiler, the GOOS-equivalence expansions the toolchain applies by
// hand (`unix` on a Unix GOOS, `linux` on android, `solaris` on illumos,
// `darwin` on ios) — plus `cgo` and the `go1.N` release tags, which are the
// only non-platform tags this process can POSITIVELY decide. Anything else —
// a project's custom `-tags integration`, an experiment tag, a name from a
// future toolchain — is unknown, and the whole expression is then reported as
// SATISFIED, because the one judgement this check may make about a tag it
// cannot see is no judgement: a file may be excluded only on a platform name
// that contradicts this build.
//
// That last clause is why the decision is taken over the expression rather
// than per tag. Evaluating tag-by-tag and answering "satisfied" for the
// unknown ones breaks the moment such a tag is negated: `//go:build
// !integration` evaluates to false on a build that does not define
// `integration`, and the file that carries it — an integration-test stub,
// say — vanishes from the package, taking its declarations with it and
// inventing "undefined" reports for whoever calls them. Skipping the whole
// constraint when any unknown tag appears keeps both sides of that pair in,
// which can only ever add a duplicate note the model's own build disproves —
// the right direction to err in, per the package comment.
func constraintHolds(expr constraint.Expr) bool {
	if namesUnknownTag(expr) {
		return true
	}
	return expr.Eval(toolchainTagHolds)
}

// namesUnknownTag reports whether the constraint mentions a build tag this
// process cannot positively judge — see constraintHolds for why that is worth
// a walk of its own instead of an answer from Eval's tag callback. The walk
// visits every node rather than short-circuiting like Eval does: a
// subexpression the toolchain never evaluates still names a tag, and a file
// gated on one is a file this check cannot rule out. (go/build/constraint has
// no walker, so the recursion is spelled out over its four expression types.)
func namesUnknownTag(expr constraint.Expr) bool {
	switch v := expr.(type) {
	case *constraint.TagExpr:
		return !judgeableTag(v.Tag)
	case *constraint.NotExpr:
		return namesUnknownTag(v.X)
	case *constraint.AndExpr:
		return namesUnknownTag(v.X) || namesUnknownTag(v.Y)
	case *constraint.OrExpr:
		return namesUnknownTag(v.X) || namesUnknownTag(v.Y)
	}
	return true // an expression shape we don't know: judge nothing
}

// judgeableTag reports whether a build tag is one this process can decide:
// a known GOOS or GOARCH name (so that a mismatch with this build may
// exclude its file), `cgo`, or a `go1.N` release tag (`cgo` and go1.N may
// each be decided in both directions — a release tag the toolchain does not
// advertise is genuinely absent from this build). A tag none of those covers —
// `integration`, `e2e`, `noopt`, a name this toolchain has never heard of —
// is unknown, and unknown tags are what constraintHolds keeps out of the
// exclusion business.
//
// Every known GOOS/GOARCH name is judgeable — a name that contradicts this
// build may exclude its file — because toolchainTagHolds decides it with the
// same GOOS-equivalence expansions the real toolchain applies by hand
// (`linux` on android, `solaris` on illumos, `darwin` on ios). Excluding a
// `//go:build solaris` sibling on linux is therefore correct, not a lost
// declaration, and this is what keeps an exclusive platform pair like
// lineedit's termios_real.go (`darwin || linux`) / termios_stub.go
// (`!(darwin || linux)`) from merging into one package on either OS.
// `unix` is the one name decided here rather than by toolchainTagHolds' OS
// equality: it is a real tag that is not a GOOS, matches any Unix GOOS by
// expansion, and names no platform to contradict — so on a Unix build it is
// judgeable true (toolchainTagHolds' unix expansion decides it), and on a
// non-Unix build it stays unjudgeable: `!unix` holds there while some
// Unix-only files carry it alongside it, and no GOOS value contradicts it to
// tell the pair apart. `boringcrypto` and the toolchain's experiment tags
// stay unjudgeable for the same reason in reverse — only a build INVOKED
// with them (GOFLAGS=-tags) can say which way they go.
func judgeableTag(tag string) bool {
	switch tag {
	case build.Default.GOOS, build.Default.GOARCH, build.Default.Compiler:
		return true
	case "unix":
		return unixOS[build.Default.GOOS]
	case "cgo":
		return true // satisfied iff cgo is enabled; toolchainTagHolds decides it
	}
	for _, t := range build.Default.ToolTags {
		if t == tag {
			return false // a toolchain experiment tag: only that toolchain can say
		}
	}
	if tag == "boringcrypto" {
		return false // decided by how the toolchain was built, not by this process
	}
	if isReleaseTag(tag) {
		return true // a release tag: present or absent, ReleaseTags answers
	}
	return knownOS[tag] || knownArch[tag] // a platform name: mismatch may exclude
}

// toolchainTagHolds reports whether a platform or toolchain tag is satisfied
// by this build, mirroring go/build's own matchTag for the names that reach
// it (see judgeableTag — a constraint naming anything else never gets here).
func toolchainTagHolds(tag string) bool {
	if tag == build.Default.GOOS || tag == build.Default.GOARCH || tag == build.Default.Compiler {
		return true
	}
	if build.Default.CgoEnabled && tag == "cgo" {
		return true
	}
	for _, list := range [][]string{build.Default.ToolTags, build.Default.ReleaseTags} {
		for _, t := range list {
			if t == tag {
				return true
			}
		}
	}
	// The GOOS names this build implies, exactly as go/build expands them.
	if build.Default.GOOS == "android" && tag == "linux" {
		return true
	}
	if build.Default.GOOS == "illumos" && tag == "solaris" {
		return true
	}
	if build.Default.GOOS == "ios" && tag == "darwin" {
		return true
	}
	if tag == "unix" && unixOS[build.Default.GOOS] {
		return true
	}
	return false
}

// isReleaseTag reports whether tag is a Go release tag ("go1.21", "go1.22.1")
// — the family go/build advertises in ReleaseTags, which therefore decides
// those tags in both directions. Deliberately shape-only: matching a tag this
// toolchain does not advertise is a true exclusion, so the shape test is what
// must not be loose — digits after "go", at least one dot, no letters.
func isReleaseTag(tag string) bool {
	if !strings.HasPrefix(tag, "go") {
		return false
	}
	rest := tag[len("go"):]
	if rest == "" || !strings.Contains(rest, ".") {
		return false
	}
	for _, r := range rest {
		if !unicode.IsDigit(r) && r != '.' {
			return false
		}
	}
	return true
}

// unixOS is the set of GOOS values the `unix` build tag matches — go/build's
// own list (internal/syslist.UnixOS), which is narrower than knownOS: plan9,
// aix, js, wasip1, windows and zos are known systems that `unix` does not
// match. Inlined because it is internal to the toolchain.
var unixOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"linux": true, "netbsd": true, "openbsd": true, "solaris": true,
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
// skipped. So too is the KEY of a keyed composite literal (`Timeout:` in
// `&http.Client{Timeout: d}`): it resolves in the literal type's field set,
// which nobody here can see — exempting it keeps field names off the report
// whether the type is imported or declared in a sibling file, while the
// literal's type and values stay judged.
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

	// uses records the first referenced line of every bare name that nothing
	// accounts for; order keeps deterministic reporting order.
	uses := map[string]int{}
	var order []string
	// noteUse judges one IDENTIFIER: records it as an unaccounted bare
	// reference, or waves it through.
	noteUse := func(id *ast.Ident) {
		name := id.Name
		// ""/"_" carry no reference; the package-clause ident declares the
		// package rather than referencing it; the file's own package-level
		// names are already in pkgNames (collected from every parsed file
		// including this one).
		if name == "" || name == "_" || name == pkgName {
			return
		}
		if pkgNames[name] || nameIsUniverse(name) || declared[name] {
			return
		}
		if _, seen := uses[name]; !seen {
			uses[name] = fset.Position(id.Pos()).Line
			order = append(order, name)
		}
	}
	// visit is the walk's whole judgement of one node: exempt a subtree whose
	// names resolve in a namespace this check cannot see, or judge the node if
	// it is a bare identifier. It is a named funcValue rather than a closure
	// literal because the one exception below — a keyed composite-literal
	// element, whose KEY is exempt while the rest of it still holds references
	// — runs the same rules over nested spans through ast.Inspect.
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			// Neither side of X.Sel is judged: Sel resolves in X's namespace
			// and X through its inferred type (see the function comment), so
			// nothing beneath a selector is a bare use. This is also what keeps
			// the `Client` of a `http.Client{…}` literal type and the `Second`
			// of a `time.Second` value off the report.
			return false
		case *ast.KeyValueExpr:
			// A keyed element's KEY (`Addr` in `http.Server{Addr: ":0"}`, `A` in
			// `P{A: 1}` where P is declared in a sibling file, `ID` in
			// `Base{ID: x}` through an embedded field) is a name in the literal
			// type's field set, which nobody here can see — a field or map key,
			// never a bare reference. Only a BARE identifier key is exempt: an
			// expression key (`x.field`, `pkg.K`, an index) is an ordinary
			// expression whose references are real, so its subtree is walked
			// under these same rules. The value is always judged. A key this
			// check cannot resolve is left silent — the accepted trade, never
			// noise-on-clean.
			if _, bareKey := v.Key.(*ast.Ident); bareKey {
				ast.Inspect(v.Value, visit)
				return false
			}
			ast.Inspect(v.Key, visit)
			ast.Inspect(v.Value, visit)
			return false
		case *ast.Ident:
			noteUse(v)
			return true
		}
		return true
	}
	ast.Inspect(file, visit)
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
