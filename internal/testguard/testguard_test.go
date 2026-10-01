package testguard

import (
	"strings"
	"testing"
)

func TestIsTestPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"go test file", "internal/tools/tools_test.go", true},
		{"go non-test file", "internal/tools/tools.go", false},
		{"python test prefix", "app/test_parser.py", true},
		{"python test suffix", "app/parser_test.py", true},
		{"python non-test", "app/parser.py", false},
		{"ts test extension", "src/auth.test.ts", true},
		{"ts spec extension", "src/auth.spec.tsx", true},
		{"js test extension", "src/util.test.mjs", true},
		{"ts non-test", "src/auth.ts", false},
		{"ruby test", "spec/models/user_test.rb", true},
		{"ruby spec", "spec/models/user_spec.rb", true},
		{"rust tests", "src/lib_tests.rs", true},
		{"rust non-test", "src/lib.rs", false},
		{"tests directory", "tests/helpers.py", true},
		{"test directory", "test/helpers.py", true},
		{"pytest no extension under tests", "tests/test_foo", true},
		{"tests in middle of path", "src/tests/fixtures/data.json", true},
		{"non-test path", "src/main.py", false},
		{"name contains test but no convention", "src/internship.go", false},
		{"nested go test", "pkg/events/events_test.go", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTestPath(tc.path); got != tc.want {
				t.Errorf("IsTestPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsTestDefinitionLine(t *testing.T) {
	tests := []struct {
		name string
		path string
		line string
		want bool
	}{
		// Go (fixture: tools_test.go shape)
		{"go func test", "pkg/a/b_test.go", "func TestEditFilePrintsDiff(t *testing.T) {", true},
		{"go func example", "pkg/a/b_test.go", "func ExampleGrep_matchesBroadJournalQuery() {", true},
		{"go func benchmark", "pkg/a/b_test.go", "func BenchmarkScan(b *testing.B) {", true},
		{"go plain func", "pkg/a/b_test.go", "func readFile(tc ToolCall, deps ToolDeps) (string, error) {", false},
		// Python (fixture: test_parser.py shape)
		{"py def test", "app/test_parser.py", "def test_parse_quotes():", true},
		{"py async def test", "app/test_parser.py", "async def test_stream_cancel():", true},
		{"py class Test", "app/test_parser.py", "class TestParser:", true},
		{"py plain def", "app/test_parser.py", "def parse(text):", false},
		{"py assert is not a definition", "app/test_parser.py", "    assert result == 42", false},
		// JS/TS (fixture: auth.test.ts shape)
		{"ts it block", "src/auth.test.ts", "  it('rejects expired tokens', () => {", true},
		{"ts describe block", "src/auth.test.ts", "describe('auth', () => {", true},
		{"ts xit block", "src/auth.test.ts", "  xit('skipped case', () => {", true},
		{"ts plain arrow", "src/auth.test.ts", "  const handler = () => {", false},
		{"ts plain call site", "src/auth.test.ts", "    expect(user).toBe(true)", false},
		// Ruby
		{"rb def test", "spec/models/user_test.rb", "  def test_valid_user", true},
		{"rb class Test", "spec/models/user_test.rb", "class TestUser", true},
		{"rb plain def", "spec/models/user_test.rb", "  def setup", false},
		// Rust
		{"rs attr", "src/lib_tests.rs", "#[test]", true},
		{"rs fn test_", "src/lib_tests.rs", "fn test_addition() {", true},
		{"rs pub async fn test_", "src/lib_tests.rs", "pub async fn test_io() {", true},
		{"rs plain fn", "src/lib_tests.rs", "fn add(a: u32, b: u32) -> u32 {", false},
		// Java
		{"java annotation", "src/MainTest.java", "    @Test", true},
		{"java test method", "src/MainTest.java", "    public void testParse() {", true},
		{"java plain method", "src/MainTest.java", "    public int parse(String s) {", false},
		// C/C++
		{"c test macro", "src/util_test.cc", "TEST(Quoter, SingleQuote)", true},
		{"c test_f macro", "src/util_test.cc", "TEST_F(FixtureTest, HandlesEscape)", true},
		{"c plain func", "src/util_test.cc", "static int helper() {", false},
		// Untyped fallback
		{"untyped def test", "tests/test_foo", "def test_thing():", true},
		{"untyped describe", "tests/test_foo", "describe('suite', function () {", true},
		{"untyped plain def", "tests/test_foo", "def helper():", false},
		{"untyped comment", "tests/test_foo", "# test things here", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTestDefinitionLine(tc.path, tc.line); got != tc.want {
				t.Errorf("isTestDefinitionLine(%q, %q) = %v, want %v", tc.path, tc.line, got, tc.want)
			}
		})
	}
}

// fixtureGoBefore is a realistic Go test file: three test functions and a
// helper. fixtureGoAfter keeps one test — the shape of the PR #127
// incident where a failing temp-repo test was sed'd out.
const fixtureGoBefore = `package tools

import "testing"

func TestWriteFileCreatesFile(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		if false {
			t.Error("no")
		}
	})
}

func TestWriteFileOverwrites(t *testing.T) {
	if false {
		t.Error("no")
	}
}

func TestCommitChangeWithAttribution(t *testing.T) {
	dir := t.TempDir()
	if dir == "" {
		t.Fatal("no dir")
	}
}

func helper() string {
	return "ok"
}
`

const fixtureGoAfter = `package tools

import "testing"

func TestCommitChangeWithAttribution(t *testing.T) {
	dir := t.TempDir()
	if dir == "" {
		t.Fatal("no dir")
	}
}

func helper() string {
	return "ok"
}
`

func TestScanRemovesGoTest(t *testing.T) {
	rep := Scan(map[string]FilePair{
		"internal/tools/tools_test.go": {Before: fixtureGoBefore, After: fixtureGoAfter},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report, got empty")
	}
	if len(rep.Files) != 1 {
		t.Fatalf("expected 1 file report, got %d", len(rep.Files))
	}
	f := rep.Files[0]
	if f.Path != "internal/tools/tools_test.go" {
		t.Errorf("path = %q", f.Path)
	}
	if f.RemovedTests != 2 {
		t.Errorf("RemovedTests = %d, want 2 (the two dropped func Test lines)", f.RemovedTests)
	}
	if f.Shrank {
		t.Errorf("Shrank = true for a 25-line file losing 9 lines")
	}
	if len(f.RemovedSample) != 2 {
		t.Errorf("RemovedSample has %d entries, want 2", len(f.RemovedSample))
	}
}

func TestScanDeletesWholeGoTestFile(t *testing.T) {
	rep := Scan(map[string]FilePair{
		"internal/tools/tools_test.go": {Before: fixtureGoBefore, After: ""},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report for a deleted test file")
	}
	f := rep.Files[0]
	if f.RemovedTests != 3 {
		t.Errorf("RemovedTests = %d, want 3 (all test functions)", f.RemovedTests)
	}
	if !f.Shrank {
		t.Error("Shrank = false, want true (file gone)")
	}
	if f.AfterLines != 0 {
		t.Errorf("AfterLines = %d, want 0", f.AfterLines)
	}
}

// fixturePyBefore/After: a pytest module where two test functions and one
// assertion-heavy body are dropped, the other tests survive.
const fixturePyBefore = `import pytest


def test_parse_quotes():
    assert parse("'a'") == "a"


def test_parse_nested():
    assert parse("'a \"b\" c'") == 'a "b" c'


def test_parse_empty():
    assert parse("") == ""


def test_parse_error():
    with pytest.raises(ValueError):
        parse(None)
`

const fixturePyAfter = `import pytest


def test_parse_error():
    with pytest.raises(ValueError):
        parse(None)
`

func TestScanRemovesPyTest(t *testing.T) {
	rep := Scan(map[string]FilePair{
		"app/test_parser.py": {Before: fixturePyBefore, After: fixturePyAfter},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report, got empty")
	}
	f := rep.Files[0]
	if f.RemovedTests != 3 {
		t.Errorf("RemovedTests = %d, want 3 (the three dropped def test_ lines)", f.RemovedTests)
	}
}

func TestScanTsSpecShrink(t *testing.T) {
	before := "describe('auth', () => {\n"
	for i := 0; i < 40; i++ {
		before += "  it('case " + string(rune('a'+i%26)) + "', () => {\n    expect(true).toBe(true)\n  })\n"
	}
	before += "})\n"
	after := "describe('auth', () => {\n  it('kept', () => {\n    expect(true).toBe(true)\n  })\n})\n"
	rep := Scan(map[string]FilePair{
		"src/auth.spec.ts": {Before: before, After: after},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report for a shrunk spec file")
	}
	f := rep.Files[0]
	if !f.Shrank {
		t.Error("Shrank = false, want true (most cases dropped)")
	}
	if f.RemovedTests < 30 {
		t.Errorf("RemovedTests = %d, want the many dropped it() lines", f.RemovedTests)
	}
	if len(f.RemovedSample) > maxSample {
		t.Errorf("RemovedSample has %d entries, want at most %d", len(f.RemovedSample), maxSample)
	}
}

func TestScanSilentWhenNothingLost(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]FilePair
	}{
		{
			name: "unchanged test file",
			files: map[string]FilePair{
				"pkg/a/b_test.go": {Before: fixtureGoBefore, After: fixtureGoBefore},
			},
		},
		{
			name: "non-test file rewritten to empty",
			files: map[string]FilePair{
				"src/main.go": {Before: "package main\n\nfunc main() {}\n", After: ""},
			},
		},
		{
			name: "test file gains a test",
			files: map[string]FilePair{
				"app/test_parser.py": {Before: fixturePyAfter, After: fixturePyBefore},
			},
		},
		{
			name: "small non-definition edit",
			files: map[string]FilePair{
				"internal/tools/tools_test.go": {
					Before: fixtureGoBefore,
					After:  fixtureGoBefore + "\n// a comment\n",
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rep := Scan(tc.files); !rep.IsEmpty() {
				t.Errorf("Scan() = %+v, want empty", rep)
			}
		})
	}
}

// TestScanReportsFilesInSortedOrder pins that Scan's multi-file report is
// deterministic: map iteration is random, but the receipt this report
// feeds must list files in a stable order for a stable turn.
func TestScanReportsFilesInSortedOrder(t *testing.T) {
	// Two test files, both losing a definition, with paths that sort
	// counter to the order a random map iteration might return them.
	before := "package p\n\nfunc TestKeep(t *testing.T) {}\nfunc TestGone(t *testing.T) {}\n"
	after := "package p\n\nfunc TestKeep(t *testing.T) {}\n"
	rep := Scan(map[string]FilePair{
		"zzz/late_test.go":  {Before: before, After: after},
		"aaa/early_test.go": {Before: before, After: after},
	})
	if len(rep.Files) != 2 {
		t.Fatalf("Scan() reported %d files, want 2", len(rep.Files))
	}
	if rep.Files[0].Path != "aaa/early_test.go" {
		t.Errorf("first file = %q, want aaa/early_test.go (sorted order)", rep.Files[0].Path)
	}
	if rep.Files[1].Path != "zzz/late_test.go" {
		t.Errorf("second file = %q, want zzz/late_test.go (sorted order)", rep.Files[1].Path)
	}
}

func TestIsScratchPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// Scratch prefixes (throwaway conventions, whatever the language).
		{"zz_dbg go test", "cmd/cortex/zz_dbg_test.go", true},
		{"dbg_ prefix go", "pkg/xx/dbg_parser.go", true},
		{"debug_ prefix py", "app/debug_parse.py", true},
		{"scratch prefix js", "src/scratch_utils.js", true},
		{"tmp_ prefix", "lib/tmp_loader.py", true},
		{"temp_ prefix", "lib/temp_loader.py", true},
		{"zzdbg no underscore", "cmd/zzdbg_repro.go", true},
		// Scratch suffixes.
		{"_tmp name", "pkg/loader_tmp.py", true},
		{"_scratch suffix", "lib/helper_scratch.py", true},
		{"config.tmp.go is not scratch", "pkg/config.tmp.go", false}, // ext is .go, name "config.tmp" carries no marker
		// Scratch extensions.
		{".tmp extension", "repro.tmp", true},
		{".bak extension", "main.go.bak", true},
		{".backup extension", "main.go.backup", true},
		// Non-scratch: a legitimate name that contains a marker word but not
		// as a throwaway convention.
		{"debugger is not scratch", "pkg/debugger.go", false},
		{"debug dir not scratch", "debug/parse.go", false},
		{"plain go file", "internal/tools/tools.go", false},
		{"plain py file", "app/parser.py", false},
		{"test file not scratch", "internal/tools/tools_test.go", false},
		{"tmp in the middle is not scratch", "pkg/middleware_tmp_handler.go", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsScratchPath(tc.path); got != tc.want {
				t.Errorf("IsScratchPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsDebugPrintLine(t *testing.T) {
	tests := []struct {
		name string
		path string
		line string
		want bool
	}{
		// Explicit debug marker — any language. (Lines use a real tab prefix
		// via an interpreted string, matching how they appear in source.)
		{"go DEBUG marker", "cmd/cortex/config.go", "\t\tfmt.Fprintf(os.Stderr, \"DEBUG: walk at %s\\n\", dir)", true},
		{"py debug marker", "app/parse.py", "    print(\"DEBUG: parsed\", value)", true},
		{"js debug marker", "src/auth.js", "  console.log(\"DEBUG: token\", tok)", true},
		{"go debug_ call", "pkg/xx/repro.go", "\tdebug_dump(state)", true},
		{"go dbg_ call", "pkg/xx/repro.go", "\tdbg_dump(state)", true},
		// Go bare debug prints — deliberate writes in a non-test file.
		{"go fmt.Println", "cmd/cortex/config.go", "\tfmt.Println(\"value:\", x)", true},
		{"go fmt.Fprintf os.Stderr", "cmd/cortex/config.go", "\tfmt.Fprintf(os.Stderr, \"x=%d\\n\", x)", true},
		{"go fmt.Fprintln os.Stdout", "cmd/cortex/config.go", "\tfmt.Fprintln(os.Stdout, x)", true},
		{"go println", "cmd/cortex/config.go", "\tprintln(\"x\")", true},
		{"go print", "cmd/cortex/config.go", "\tprint(\"x\")", true},
		// Routine Go formatting is NOT a debug print (this repo formats into
		// buffers all the time): the Sprint* family and a bare Fprintf to an
		// arbitrary writer only match through the explicit marker above.
		{"go fmt.Fprintf arbitrary writer not flagged", "cmd/cortex/config.go", "\tfmt.Fprintf(w, \"x=%d\\n\", x)", false},
		{"go fmt.Sprintf not flagged", "cmd/cortex/config.go", "\treturn fmt.Sprintf(\"%d\", x)", false},
		{"go fmt.Fprintf buffer not flagged", "cmd/cortex/config.go", "\tfmt.Fprintf(&b, \"%d\\n\", x)", false},
		{"go fmt.Sprintln not flagged", "cmd/cortex/config.go", "\tfmt.Sprintln(a, b)", false},
		{"go log.Print not flagged", "cmd/cortex/config.go", "\tlog.Print(\"recovered\")", false},
		// Bare print in non-Go is routine code — only the marker matches.
		{"py bare print not flagged", "app/parse.py", "    print(value)", false},
		{"js console.log not flagged", "src/auth.js", "  console.log(tok)", false},
		// No marker, no print call.
		{"plain go line", "cmd/cortex/config.go", "\treturn nil", false},
		{"plain py line", "app/parse.py", "    return value", false},
		// Comments are never debug prints.
		{"go comment", "cmd/cortex/config.go", "// DEBUG: left in for later", false},
		{"py comment", "app/parse.py", "# DEBUG: left in for later", false},
		// "debug" inside an identifier does not match the marker alone.
		{"debugger identifier", "pkg/xx/repro.go", "\treturn debugging", false},
		{"empty line", "cmd/cortex/config.go", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDebugPrintLine(tc.path, tc.line); got != tc.want {
				t.Errorf("isDebugPrintLine(%q, %q) = %v, want %v", tc.path, tc.line, got, tc.want)
			}
		})
	}
}

// TestScanDebugFlagsAddedGoPrint is the #152 headline: a turn adds
// fmt.Fprintf(os.Stderr, "DEBUG: ...") into a non-test production file and
// the report names the file with the added line as the sample.
func TestScanDebugFlagsAddedGoPrint(t *testing.T) {
	before := `package cortex

import "fmt"

func projectInstructions() string {
	return ""
}
`
	after := `package cortex

import (
	"fmt"
	"os"
)

func projectInstructions() string {
	fmt.Fprintf(os.Stderr, "DEBUG: walk at %s\n", dir)
	return ""
}
`
	rep := ScanDebug(map[string]FilePair{
		"cmd/cortex/config.go": {Before: before, After: after},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report, got empty")
	}
	if len(rep.Files) != 1 {
		t.Fatalf("expected 1 file report, got %d", len(rep.Files))
	}
	f := rep.Files[0]
	if f.Path != "cmd/cortex/config.go" {
		t.Errorf("path = %q", f.Path)
	}
	if f.DebugPrints != 1 {
		t.Errorf("DebugPrints = %d, want 1", f.DebugPrints)
	}
	if f.Scratch {
		t.Error("Scratch = true, want false (a production file is not scratch)")
	}
	if len(f.DebugSample) != 1 || !strings.Contains(f.DebugSample[0], "DEBUG") {
		t.Errorf("DebugSample = %v, want the DEBUG line", f.DebugSample)
	}
}

// TestScanDebugFlagsScratchTestFile is the #152 cited incident: a turn drops
// a zz_dbg test file into the package. zz_dbg_test.go is BOTH scratch- and
// test-named: the non-test gate skips its debug-print signal (a debug print
// inside a test is legitimate), but the scratch signal fires for every
// snapshotted path — so ScanDebug reports it as a leftover scratch file.
func TestScanDebugFlagsScratchTestFile(t *testing.T) {
	rep := ScanDebug(map[string]FilePair{
		"cmd/cortex/zz_dbg_test.go": {Before: "", After: "package main\n\nimport \"testing\"\n\nfunc TestDbg(t *testing.T) {}\n", Created: true},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report for a leftover scratch test file, got empty")
	}
	if len(rep.Files) != 1 {
		t.Fatalf("expected 1 file report, got %d", len(rep.Files))
	}
	f := rep.Files[0]
	if f.Path != "cmd/cortex/zz_dbg_test.go" {
		t.Errorf("path = %q, want cmd/cortex/zz_dbg_test.go", f.Path)
	}
	if !f.Scratch {
		t.Error("Scratch = false, want true (zz_dbg_test.go is a scratch file)")
	}
	if f.DebugPrints != 0 {
		t.Errorf("DebugPrints = %d, want 0 (the non-test gate skips the debug-print signal)", f.DebugPrints)
	}
}

// TestScanDebugScratchNonTestFile: a scratch-NAMED NON-TEST file (e.g. a
// zz_dbg.go scratch module) left in the workspace IS reported, with Scratch.
func TestScanDebugScratchNonTestFile(t *testing.T) {
	rep := ScanDebug(map[string]FilePair{
		"cmd/cortex/zz_dbg.go": {Before: "", After: "package main\n\nfunc main() {}\n", Created: true},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report for a scratch non-test file")
	}
	f := rep.Files[0]
	if !f.Scratch {
		t.Error("Scratch = false, want true (zz_dbg.go is a scratch file)")
	}
	if f.Path != "cmd/cortex/zz_dbg.go" {
		t.Errorf("path = %q", f.Path)
	}
}

// TestScanDebugScratchNotFlaggedWithoutCreated pins the Created gate: a
// scratch-named file the harness did NOT create (Created=false — a
// pre-existing file it only saw through the baseline, or a file whose
// created state is unknown) is never "left behind", whatever its
// before/after shape.
func TestScanDebugScratchNotFlaggedWithoutCreated(t *testing.T) {
	tests := []struct {
		name string
		path string
		pair FilePair
	}{
		{
			name: "pre-existing scratch file unchanged",
			path: "testdata/foo.bak",
			pair: FilePair{Before: "existing\n", After: "existing\n"},
		},
		{
			name: "pre-existing scratch file deleted",
			path: "scripts/tmp_setup.sh",
			pair: FilePair{Before: "existing\n", After: ""},
		},
		{
			name: "pre-existing scratch file edited",
			path: "scripts/tmp_setup.sh",
			pair: FilePair{Before: "existing\n", After: "existing\nedited\n"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rep := ScanDebug(map[string]FilePair{tc.path: tc.pair}); !rep.IsEmpty() {
				t.Errorf("ScanDebug() = %+v, want empty (%s is not the turn's leftover)", rep, tc.name)
			}
		})
	}
}

// TestScanDebugScratchCreatedThenRemoved is case 1 of the Created gate: a
// scratch file the turn created (Created=true, Before="") and then REMOVED
// (After="") was cleaned up, not left behind — penalizing exactly the
// cleanup the issue asks for would defeat the receipt's purpose.
func TestScanDebugScratchCreatedThenRemoved(t *testing.T) {
	rep := ScanDebug(map[string]FilePair{
		"zz_dbg.go": {Before: "", After: "", Created: true},
	})
	if !rep.IsEmpty() {
		t.Errorf("ScanDebug() = %+v, want empty (the turn removed the scratch file it created)", rep)
	}
}

// TestScanDebugSilentWhenNothingAdded proves the receipt degrades to silence
// for every non-debug turn: an unchanged production file, a production file
// that gains ordinary (non-print) code, and a turn with no files at all.
func TestScanDebugSilentWhenNothingAdded(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]FilePair
	}{
		{
			name: "unchanged production file",
			files: map[string]FilePair{
				"cmd/cortex/config.go": {Before: "package main\n\nfunc main() {}\n", After: "package main\n\nfunc main() {}\n"},
			},
		},
		{
			name: "production file gains ordinary code",
			files: map[string]FilePair{
				"cmd/cortex/config.go": {
					Before: "package main\n\nfunc main() {}\n",
					After:  "package main\n\nfunc main() {}\n\nfunc helper() int { return 1 }\n",
				},
			},
		},
		{
			name: "test file gains a debug print (legitimate, skipped)",
			files: map[string]FilePair{
				"pkg/a/b_test.go": {
					Before: "package p\n\nfunc TestKeep(t *testing.T) {}\n",
					After:  "package p\n\nfunc TestKeep(t *testing.T) {}\n\n// DEBUG: probe\n",
				},
			},
		},
		{
			name:  "no files",
			files: map[string]FilePair{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rep := ScanDebug(tc.files); !rep.IsEmpty() {
				t.Errorf("ScanDebug() = %+v, want empty", rep)
			}
		})
	}
}

// TestScanDebugReportsFilesInSortedOrder pins that ScanDebug's multi-file
// report is deterministic, like Scan's: map iteration is random, but the
// receipt must list files in a stable order.
func TestScanDebugReportsFilesInSortedOrder(t *testing.T) {
	before := "package main\n\nfunc main() {}\n"
	after := "package main\n\nfunc main() {\n\tfmt.Println(\"x\")\n}\n"
	rep := ScanDebug(map[string]FilePair{
		"zzz/late.go":  {Before: before, After: after},
		"aaa/early.go": {Before: before, After: after},
	})
	if len(rep.Files) != 2 {
		t.Fatalf("ScanDebug() reported %d files, want 2", len(rep.Files))
	}
	if rep.Files[0].Path != "aaa/early.go" {
		t.Errorf("first file = %q, want aaa/early.go (sorted order)", rep.Files[0].Path)
	}
	if rep.Files[1].Path != "zzz/late.go" {
		t.Errorf("second file = %q, want zzz/late.go (sorted order)", rep.Files[1].Path)
	}
}

// TestScanDebugSampleCapped pins the bounded-sample contract: more added
// debug lines than maxSample still report them all in the count but cap the
// sample at maxSample entries, each truncated to maxSampleLen.
func TestScanDebugSampleCapped(t *testing.T) {
	before := "package main\n\nfunc main() {}\n"
	var after strings.Builder
	after.WriteString("package main\n\nfunc main() {\n")
	for i := 0; i < maxSample+3; i++ {
		after.WriteString("\tfmt.Println(\"DEBUG: probe\", i)\n")
	}
	after.WriteString("}\n")
	rep := ScanDebug(map[string]FilePair{
		"cmd/cortex/repro.go": {Before: before, After: after.String()},
	})
	if rep.IsEmpty() {
		t.Fatal("expected a report")
	}
	f := rep.Files[0]
	if f.DebugPrints != maxSample+3 {
		t.Errorf("DebugPrints = %d, want %d (all added debug lines counted)", f.DebugPrints, maxSample+3)
	}
	if len(f.DebugSample) > maxSample {
		t.Errorf("DebugSample has %d entries, want at most %d", len(f.DebugSample), maxSample)
	}
	for _, s := range f.DebugSample {
		if len(s) > maxSampleLen {
			t.Errorf("sample line exceeds the cap: %d bytes", len(s))
		}
	}
}

// TestScanDebugNotFlaggedWhenAlreadyInBefore: a debug print that was ALREADY
// in the file before the turn (not added by it) is not flagged — the scan
// only reports what the turn ADDED.
func TestScanDebugNotFlaggedWhenAlreadyInBefore(t *testing.T) {
	base := `package main

func main() {
	fmt.Println("DEBUG: pre-existing")
}
`
	rep := ScanDebug(map[string]FilePair{
		"cmd/cortex/repro.go": {Before: base, After: base + "\nfunc extra() {}\n"},
	})
	if !rep.IsEmpty() {
		t.Errorf("ScanDebug() = %+v, want empty (the debug print was pre-existing, not added)", rep)
	}
}
