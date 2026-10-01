// Package testguard detects, from a before/after diff of file contents,
// turns that remove or substantially shrink the project's tests.
//
// The detection is language-agnostic on purpose: it never parses a
// language. Test files are recognized by the naming conventions projects
// use (Go *_test.go, Python test_*.py / *_test.py, JS/TS *.test.* /
// *.spec.*, Ruby *_test.rb, Rust *_tests.rs, and files under a tests/
// or test/ directory), and a removed line is counted as a test
// definition when its shape matches the conventions of the file's
// language — or of no language, for files without a recognizable
// extension: a test-named function (def test_*, func Test*) or a
// block-opening test keyword (describe / it / test) at the start of the
// expression.
//
// A report is only a warning for the turn's receipt: it tells the model
// (and the journal) that tests disappeared, so the summary has to
// account for it. It never blocks the turn.
package testguard

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ShrinkFraction is the share of a test file's original lines that may be
// removed before a turn is reported as shrinking the file substantially,
// even when none of the removed lines look like test definitions. A test
// file that goes from 200 lines to 40 is suspect regardless of which
// lines survived; 0.75 keeps the bar high enough that a routine refactor
// of a 25-line file is not flagged, while a file that is emptied or
// halved still is. Tiny files (fewer than 8 original lines) are exempt:
// there, a single dropped definition is the signal the RemovedTests
// count already carries.
const (
	ShrinkFraction = 0.75
	shrinkMinLines = 8
)

// IsTestPath reports whether path is one of the project's test files by
// naming convention, whatever the language. A file is a test file when
// its name carries a test prefix/suffix or a .test/.spec extension part,
// or when it sits under a tests/ or test/ directory. Exported so the
// harness (cmd/cortex's testwatch, issue #141) can find the project's
// test files by the same convention the scan uses to judge them.
func IsTestPath(path string) bool {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	name := strings.ToLower(strings.TrimSuffix(base, ext))
	if strings.HasSuffix(name, "_test") || strings.HasSuffix(name, "_tests") ||
		strings.HasSuffix(name, "_spec") || strings.HasPrefix(name, "test_") {
		return true
	}
	// JS/TS convention: *.test.ts / *.spec.js / *.spec.mjs ...
	if strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".spec") {
		return true
	}
	// Directory convention: a tests/ or test/ component anywhere in the
	// path. This does not require a recognizable extension: a file called
	// test_foo (pytest-style, no extension) under tests/ is a test file.
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if part == "tests" || part == "test" {
			return true
		}
	}
	return false
}

// The shape of a test definition, per file type. Each pattern is anchored
// so it matches a declaration — not a comment, not a bare call site
// (assertions and runner helpers are deliberately not test definitions).
// What makes a line a test definition is the convention (a Test-prefixed
// or test_-prefixed name, or a test keyword at the expression head), not
// any particular framework.
var (
	// Go: func TestFoo(...), func ExampleFoo, func BenchmarkFoo,
	// func FuzzFoo — Go's testing package names are the convention.
	goTestRe = regexp.MustCompile(`^\s*func\s+(Test|Example|Benchmark|Fuzz)[A-Za-z0-9_]*\s*\(`)
	// Python/Ruby: def test_foo(...) (also async def), or a class named
	// Test* — covers pytest, unittest, and RSpec example methods. RSpec
	// methods end in a bare name with no parens, so the def form allows
	// either a signature or a bare name.
	defTestRe   = regexp.MustCompile(`^\s*(async\s+)?def\s+(test_[A-Za-z0-9_]*|Test[A-Za-z0-9_]*)\s*([(:]|$)`)
	classTestRe = regexp.MustCompile(`^\s*class\s+Test[A-Za-z0-9_]*\b`)
	// JS/TS: describe/it/test/testCase/when as the expression head,
	// including the one-line arrow form and the x-prefixed variants.
	jsTestRe = regexp.MustCompile(`^\s*(x|f)?(describe|it|test|testCase|when)\s*[("(]`)
	// Rust: a #[test] attribute line, or fn test_foo inside mod tests.
	rsTestAttrRe = regexp.MustCompile(`^\s*#\[(\w+::)*test\]`)
	rsTestFnRe   = regexp.MustCompile(`^\s*(pub\s+|async\s+)*fn\s+test_[a-z_0-9]*\s*\(`)
	// C/C++: gtest TEST/TEST_F/TEST_P macros and doctest TEST_CASE.
	cTestRe = regexp.MustCompile(`^\s*TEST(?:_F|_P)?\s*\(|^\s*TEST_CASE\s*\(`)
	// Java: an @Test annotation (with or without args), or a
	// (void) testFoo/shouldFoo method.
	javaAttrRe = regexp.MustCompile(`^\s*@Test\b`)
	javaFnRe   = regexp.MustCompile(`^\s*(public|protected|private)?\s*(static\s+)?(void|@Test)\s+(test|should)[A-Z_][A-Za-z0-9_]*\s*\(`)
	// Generic fallback for untyped files: the two shapes every supported
	// language's test declaration shares.
	genericDefRe   = regexp.MustCompile(`^\s*(async\s+)?def\s+test_[A-Za-z0-9_]*\s*\(`)
	genericBlockRe = regexp.MustCompile(`^\s*(describe|it|test)\s*[("(]`)
)

// isTestDefinitionLine reports whether a single line of code declares a
// test. The file's extension selects the pattern set; unknown
// extensions fall back to the language-agnostic shapes.
func isTestDefinitionLine(path, line string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return goTestRe.MatchString(line)
	case ".py":
		return defTestRe.MatchString(line) || classTestRe.MatchString(line)
	case ".rb":
		return defTestRe.MatchString(line) || classTestRe.MatchString(line)
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return jsTestRe.MatchString(line)
	case ".rs":
		return rsTestAttrRe.MatchString(line) || rsTestFnRe.MatchString(line)
	case ".java":
		return javaAttrRe.MatchString(line) || javaFnRe.MatchString(line)
	case ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp", ".hxx":
		return cTestRe.MatchString(line)
	default:
		return genericDefRe.MatchString(line) || genericBlockRe.MatchString(line)
	}
}

// FilePair is one file's before/after content for the turn being scanned.
// An empty After marks a deleted file. Created marks a file the TURN created
// (it did not exist before the turn) — the harness sets it when it knows;
// ScanDebug only fires its scratch-file signal on files it was created by
// the turn, so a pre-existing scratch-named file the turn merely edited or
// deleted is not reported as "left behind".
type FilePair struct {
	Before  string
	After   string
	Created bool
}

// Baseline is the compact per-file before-side for a file the harness
// baselined before a bash call ran (see MakeBaseline). Lines is the file's
// line count at snapshot time; Defs is every line that looked like a test
// definition then. The receipt diffs a live read of the file against this
// to recover removed definitions and shrink without storing the full
// content — a 32-file / 1 MiB content budget cannot cover a real repo's
// test files, but a {line-count, definition-lines} pair per file can.
type Baseline struct {
	Lines int
	Defs  []string
}

// FileReport is the per-file half of a Report.
type FileReport struct {
	Path string // the file's path as the turn touched it
	// RemovedTests counts removed lines that looked like test
	// definitions.
	RemovedTests int
	// RemovedSample is a bounded sample of the removed test-definition
	// lines, so the recipient can see which ones.
	RemovedSample []string
	// Shrank reports the file lost at least ShrinkFraction of its
	// original lines.
	Shrank bool
	// BeforeLines / AfterLines are the file's line counts, either side.
	BeforeLines, AfterLines int
}

// Report is what a turn did to the project's test files.
type Report struct {
	Files []FileReport
}

// IsEmpty reports whether the turn left every test file untouched — the
// case where the receipt should stay silent.
func (r Report) IsEmpty() bool {
	return len(r.Files) == 0
}

// Scan compares each file's before/after content and returns the report
// of what the turn did to the project's tests. Non-test files are
// skipped entirely — a turn that rewrites main.go to empty loses no
// tests this package knows about. rep.Files is sorted by Path: map
// iteration is random, but the receipt this report feeds must be
// deterministic for a deterministic turn.
func Scan(files map[string]FilePair) Report {
	var rep Report
	for path, pair := range files {
		if !IsTestPath(path) {
			continue
		}
		if frep, ok := scanFile(path, pair.Before, pair.After); ok {
			rep.Files = append(rep.Files, frep)
		}
	}
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
	return rep
}

// scanFile reports one file. ok=false when the file is unchanged,
// nothing test-relevant was removed, and the shrink was below the
// threshold.
func scanFile(path, before, after string) (FileReport, bool) {
	if before == after {
		return FileReport{}, false
	}
	beforeLines := splitLines(before)
	afterLines := splitLines(after)

	removed := removedLines(beforeLines, afterLines)
	defs := 0
	sample := make([]string, 0, len(removed))
	for _, l := range removed {
		if isTestDefinitionLine(path, l) {
			defs++
			if len(sample) < maxSample {
				sample = append(sample, truncate(l, maxSampleLen))
			}
		}
	}
	shrank := len(beforeLines) >= shrinkMinLines &&
		float64(len(beforeLines)-len(afterLines))/float64(len(beforeLines)) >= ShrinkFraction

	if defs == 0 && !shrank {
		return FileReport{}, false
	}
	return FileReport{
		Path:          path,
		RemovedTests:  defs,
		RemovedSample: sample,
		Shrank:        shrank,
		BeforeLines:   len(beforeLines),
		AfterLines:    len(afterLines),
	}, true
}

const (
	maxSample    = 5
	maxSampleLen = 80
)

// removedLines is the multiset difference before minus after: each line
// present more often in before than in after is listed once per excess
// occurrence. It is a count a reviewer would eyeball, not a proof that
// any particular definition is gone.
func removedLines(before, after []string) []string {
	count := map[string]int{}
	for _, l := range after {
		count[l]++
	}
	var out []string
	for _, l := range before {
		if count[l] > 0 {
			count[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// MakeBaseline is the compact per-file before-side the harness (cmd/cortex's
// testwatch, issue #141) keeps for the workspace's test-named files when a
// bash call is about to run: bash names no file, so the receipt cannot
// snapshot the full content of every test file (a real repo has hundreds),
// but it CAN remember, per file, how many lines it had and which lines were
// test definitions. The turn-end receipt then compares those against the
// file's current content (see cmd/cortex/testwatch.go) to recover removed
// definitions and shrink — the #127 incident's shape — without holding the
// whole file in memory. A file the scan does not recognize by name (a
// non-test) yields a zero Baseline.
func MakeBaseline(path, content string) Baseline {
	if !IsTestPath(path) {
		return Baseline{}
	}
	var b Baseline
	for _, l := range splitLines(content) {
		b.Lines++
		if isTestDefinitionLine(path, l) {
			b.Defs = append(b.Defs, l)
		}
	}
	return b
}

// splitLines splits on \n and drops the trailing empty element a final
// newline produces.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// truncate caps a sample line at n bytes (a mid-rune cut would only ever
// affect the display sample, never the count).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
