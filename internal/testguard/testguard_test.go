package testguard

import "testing"

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
