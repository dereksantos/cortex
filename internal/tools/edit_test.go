package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditFileDeclarationDocumentsResultAndFailureNotes proves the edit_file
// tool description tells the model about the net line change / net-deletion
// warning, the post-edit changed region, and the all-match-lines /
// closest-region guidance in failures — so the prose can't silently regress
// from the behavior below it.
func TestEditFileDeclarationDocumentsResultAndFailureNotes(t *testing.T) {
	desc := EditFile.Function.Description
	for _, sub := range []string{
		"lines removed/added",
		"removes more lines than it adds",
		"current changed region",
		"line numbers of every match",
		"closest region in the file",
		"GUARD DROPPED",
		"only whitespace differs",
		"does not match the file as it is now",
		"CURRENT content",
	} {
		if !strings.Contains(desc, sub) {
			t.Errorf("edit_file description should mention %q; got:\n%s", sub, desc)
		}
	}
}

// seedEditFile writes a temp file and returns its path, used by the edit_file
// argument-validation tests.
func seedEditFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return p
}

// editArgs marshals a tool call for edit_file with the given arguments.
func editArgs(t *testing.T, args map[string]any) ToolCall {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return ToolCall{Function: FunctionCall{Name: FunctionEditFile, Arguments: string(b)}}
}

// TestEditFileEmptyOldStringError proves a call that carries no `edits` array
// and an empty top-level `old_string` gets an error that names the missing
// field — not the misleading "old_string must not be empty" that describes a
// different mistake. See #153.
func TestEditFileEmptyOldStringError(t *testing.T) {
	path := seedEditFile(t, "f.go", "package main\n")

	cases := []struct {
		name    string
		args    map[string]any
		wantSub []string // substrings the error MUST contain
		notSub  string   // substring the error must NOT contain
	}{
		{
			name: "no edits array and empty top-level old_string",
			args: map[string]any{
				"path":       path,
				"old_string": "",
				"new_string": "x",
			},
			wantSub: []string{"edits", "old_string"},
			notSub:  "must not be empty",
		},
		{
			name: "edits entry with empty old_string (per-entry)",
			args: map[string]any{
				"path": path,
				"edits": []map[string]any{
					{"old_string": "", "new_string": "x"},
				},
			},
			wantSub: []string{"edit 1", "old_string"},
			notSub:  "",
		},
		{
			name: "edits entry with empty old_string second entry",
			args: map[string]any{
				"path": path,
				"edits": []map[string]any{
					{"old_string": "package main", "new_string": "package main2"},
					{"old_string": "", "new_string": "x"},
				},
			},
			wantSub: []string{"edit 2", "old_string"},
			notSub:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Execute(context.Background(), editArgs(t, tc.args), headlessDeps{})
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			got := err.Error()
			for _, sub := range tc.wantSub {
				if !strings.Contains(got, sub) {
					t.Errorf("error should mention %q; got: %q", sub, got)
				}
			}
			if tc.notSub != "" && strings.Contains(got, tc.notSub) {
				t.Errorf("error should not contain %q; got: %q", tc.notSub, got)
			}
		})
	}
}

// TestEditFileValidStillApplies guards that the new validation doesn't break a
// correct single edit.
func TestEditFileValidStillApplies(t *testing.T) {
	path := seedEditFile(t, "f.go", "package main\nfunc f() int { return 1 }\n")

	out, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "return 1", "new_string": "return 2",
	}), headlessDeps{})
	if err != nil {
		t.Fatalf("valid single edit should succeed: %v", err)
	}
	if !strings.Contains(out, "edited") {
		t.Errorf("expected an 'edited' result; got: %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "return 2") {
		t.Errorf("edit did not land; file now: %q", string(data))
	}
	if !strings.Contains(out, "Current changed region:") {
		t.Errorf("successful edit should include the changed-region snippet; got: %q", out)
	}
}

// TestEditFileResultIncludesChangedRegion proves a successful edit's result
// carries a bounded, line-numbered snippet of the current changed region so
// the model's view of the file stays in sync (#173).
func TestEditFileResultIncludesChangedRegion(t *testing.T) {
	cases := []struct {
		name    string
		before  string
		old     string
		new     string
		wantSub []string // substrings the result MUST contain
	}{
		{
			name:    "in-place change shows the new line with its line number",
			before:  "package main\nfunc f() int { return 1 }\n",
			old:     "return 1",
			new:     "return 2",
			wantSub: []string{"Current changed region:", "2: func f() int { return 2 }"},
		},
		{
			name:    "multi-line replacement shows all new lines",
			before:  "package main\nfunc a() {}\n",
			old:     "func a() {}",
			new:     "func a() int { return 1 }\nfunc b() {}",
			wantSub: []string{"Current changed region:", "2: func a() int { return 1 }", "3: func b() {}"},
		},
		{
			name:    "deletion shows the removed line with a dash marker",
			before:  "package main\nline one\nline two\nline three\n",
			old:     "line two\n",
			new:     "",
			wantSub: []string{"Current changed region:", "-3: line two"},
		},
		{
			name:    "context lines appear without a marker",
			before:  "package main\nfunc f() int {\n\treturn 1\n}\n",
			old:     "return 1",
			new:     "return 2",
			wantSub: []string{"Current changed region:", "1: package main", "2: func f() int {"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.go", tc.before)
			out, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": tc.new,
			}), headlessDeps{})
			if err != nil {
				t.Fatalf("edit_file: %v", err)
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(out, sub) {
					t.Errorf("result missing %q; got: %q", sub, out)
				}
			}
		})
	}
}

// TestEditFileReportsNetLineChange proves a successful edit's model-facing
// result carries the real removed/added counts (from the line diff, not a
// before/after count), and that a deletion-heavy edit appends the silent-loss
// warning — the thing that was invisible in the TTY-only diff. See #153.
func TestEditFileReportsNetLineChange(t *testing.T) {
	cases := []struct {
		name      string
		before    string
		old       string
		new       string
		wantDelta string // the "-R/+A lines" fragment
		wantWarn  bool   // expect the silent-loss warning
	}{
		{
			name:      "in-place change (same line count) reports both sides non-zero, no warning",
			before:    "package main\nfunc f() int { return 1 }\n",
			old:       "return 1",
			new:       "return 2",
			wantDelta: "-1/+1 lines",
			wantWarn:  false,
		},
		{
			name:      "deletion-heavy edit warns",
			before:    "package main\nfunc f() int {\n\tif got, want := implicit.Instructions(), projectInstructions(); got != want {\n\t\tt.Errorf(\"instructions mismatch: got %v want %v\", got, want)\n\t}\n\treturn 1\n}\n",
			old:       "func f() int {\n\tif got, want := implicit.Instructions(), projectInstructions(); got != want {\n\t\tt.Errorf(\"instructions mismatch: got %v want %v\", got, want)\n\t}\n\treturn 1\n}",
			new:       "func f() int { return 1 }",
			wantDelta: "-6/+1 lines",
			wantWarn:  true,
		},
		{
			name:      "addition with one line replaced reports both sides, no warning",
			before:    "package main\nfunc a() {}\n",
			old:       "func a() {}",
			new:       "func a() int { return 1 }\nfunc b() {}",
			wantDelta: "-1/+2 lines",
			wantWarn:  false,
		},
		{
			name:      "same-count replacement reports both sides non-zero, no warning",
			before:    "package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n",
			old:       "func a() {}\nfunc b() {}",
			new:       "func x() {}\nfunc y() {}",
			wantDelta: "-2/+2 lines",
			wantWarn:  false,
		},
		{
			name:      "trailing whitespace change reports both sides non-zero, no warning",
			before:    "package main\n",
			old:       "package main",
			new:       "package main ",
			wantDelta: "-1/+1 lines",
			wantWarn:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.go", tc.before)
			out, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": tc.new,
			}), headlessDeps{})
			if err != nil {
				t.Fatalf("edit_file: %v", err)
			}
			if !strings.Contains(out, tc.wantDelta) {
				t.Errorf("result missing net delta %q; got: %q", tc.wantDelta, out)
			}
			hasWarn := strings.Contains(out, "WARNING")
			if hasWarn != tc.wantWarn {
				t.Errorf("warning presence = %v, want %v; got: %q", hasWarn, tc.wantWarn, out)
			}
		})
	}
}

// TestEditFileMultiEditAggregatesLineDelta proves the multi-edit batch result
// aggregates the net line change across all edits in the batch.
func TestEditFileMultiEditAggregatesLineDelta(t *testing.T) {
	before := "package main\nline one\nline two\nline three\n"
	path := seedEditFile(t, "f.go", before)

	// Two edits: drop "line one" and "line three" (each a pure deletion), keep
	// the rest. Net: -2 lines removed, 0 added → should warn.
	out, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "line one\n", "new_string": ""},
			{"old_string": "line three\n", "new_string": ""},
		},
	}), headlessDeps{})
	if err != nil {
		t.Fatalf("multi edit: %v", err)
	}
	if !strings.Contains(out, "2 edits") {
		t.Errorf("result should name the edit count; got: %q", out)
	}
	if !strings.Contains(out, "-2/+0 lines") {
		t.Errorf("result should aggregate the net delta across the batch; got: %q", out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("deletion-heavy batch should warn; got: %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(data), "line one") || strings.Contains(string(data), "line three") {
		t.Errorf("batch should have removed the two lines; file now: %q", string(data))
	}
}

// TestEditFileAmbiguousExactMatchPointsAtFirstOccurrence proves an exact-match
// hit that lands in more than one place names the first occurrence's line, so
// the model can add the surrounding context that disambiguates (or set
// replace_all). See #153.
func TestEditFileAmbiguousExactMatchPointsAtFirstOccurrence(t *testing.T) {
	// "x := 1" appears on lines 2 and 4 of the seeded file.
	before := "package main\nx := 1\nkeep this line\nx := 1\n"
	path := seedEditFile(t, "f.go", before)

	_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "x := 1", "new_string": "x := 2",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("ambiguous exact match should error, got none")
	}
	got := err.Error()
	for _, sub := range []string{"found 2 times", "at lines 2, 4", "replace_all"} {
		if !strings.Contains(got, sub) {
			t.Errorf("error should contain %q; got: %q", sub, got)
		}
	}
}

// TestEditFileAmbiguousTolerantMatchPointsAtFirstOccurrence proves the
// whitespace-tolerant path's ambiguity error names the first match's line too
// — the tool description promises the hint for every ambiguous match, not just
// the exact ones. Here a block that appears once byte-for-byte lands twice
// once indentation is ignored. See #153.
func TestEditFileAmbiguousTolerantMatchPointsAtFirstOccurrence(t *testing.T) {
	// "x := 1" appears once exactly (line 2); "\tx := 1" matches it exactly
	// plus a second, differently-indented copy on line 4 under tier 2.
	before := "package main\nx := 1\nkeep this line\n  x := 1\n"
	path := seedEditFile(t, "f.go", before)

	_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "\tx := 1", "new_string": "x := 2",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("ambiguous tolerant match should error, got none")
	}
	got := err.Error()
	for _, sub := range []string{"matches 2 places", "at lines 2, 4", "replace_all"} {
		if !strings.Contains(got, sub) {
			t.Errorf("error should contain %q; got: %q", sub, got)
		}
	}
}

// TestEditFileNotFoundCarriesClosestLineHint proves a guessed anchor (never
// read, text that isn't in the file) comes back with the closest-region hint
// from the tolerant path, so the model can see the current file content around
// the best-matching line and correct the edit without a separate read_file
// call. See #153, #173.
func TestEditFileNotFoundCarriesClosestLineHint(t *testing.T) {
	before := "package main\nfunc Chdir(root string) {}\n"
	path := seedEditFile(t, "f.go", before)

	// A guessed anchor close to but not equal to the real line.
	_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "func T.Chdir(root string) {}", "new_string": "func Chdir(root string) {}",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("guessed anchor should error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "not found") {
		t.Errorf("error should say not found; got: %q", got)
	}
	if !strings.Contains(got, "closest region") {
		t.Errorf("error should carry the closest-region hint; got: %q", got)
	}
	if !strings.Contains(got, "  >2: func Chdir(root string) {}") {
		t.Errorf("error should point at line 2; got: %q", got)
	}
	// Issue #201: a not-found failure must steer the model back to the edit
	// tools instead of scripting the change through bash.
	if !strings.Contains(got, "do not script this change through bash") {
		t.Errorf("error should carry the no-bash directive; got: %q", got)
	}
	if !strings.Contains(got, "retry edit_file") || !strings.Contains(got, "write_file") {
		t.Errorf("directive should name the edit tools; got: %q", got)
	}
}

// TestEditFileNotFoundHintAnchorsOnBestLine proves nearMissHint scores EVERY
// line of old against the file (not just the first): when the model's
// old_string begins with a context line that is no longer in the file — a
// stale span from a view the turn itself replaced — the hint still anchors
// on the line the span's other lines point at, so the model re-reads the
// right region instead of switching to a sed/awk/python script. Issue #201.
func TestEditFileNotFoundHintAnchorsOnBestLine(t *testing.T) {
	// The file's line 2 is the only place the span could plausibly land.
	// The model's old_string is a multi-line span whose FIRST line is
	// absent (a guess, or a stale view) but whose second line is an exact
	// match — the hint must anchor on line 2.
	before := "package main\nfunc Chdir(root string) {}\n\nvar x int\n"
	path := seedEditFile(t, "f.go", before)

	_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path":       path,
		"old_string": "func Changed(root string) int {\nfunc Chdir(root string) {}\n",
		"new_string": "x",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("stale-span edit should error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "not found") {
		t.Errorf("error should say not found; got: %q", got)
	}
	if !strings.Contains(got, "closest region") {
		t.Errorf("error should carry the closest-region hint; got: %q", got)
	}
	if !strings.Contains(got, "  >2: func Chdir(root string) {}") {
		t.Errorf("error should anchor on the best line (line 2); got: %q", got)
	}
	if strings.Contains(got, "  >1: ") {
		t.Errorf("error must not anchor on the absent first line; got: %q", got)
	}
}

// TestEditFileNotFoundDirectiveTable pins the self-correction directive on
// every not-found failure path (issue #201): the span the model sent is
// absent and similar enough to earn a hint, AND absent with NO similar line
// at all. Both must carry the steer-away-from-bash text so a failure — the
// moment the model is tempted to reach for sed — always points it back at
// read_file + edit_file (or write_file for a whole-file rewrite).
func TestEditFileNotFoundDirectiveTable(t *testing.T) {
	cases := []struct {
		name     string
		before   string
		old      string
		wantHint bool // true → the closest-region hint is also present
	}{
		{
			name:     "absent span with a close line (hint + directive)",
			before:   "package main\nfunc Chdir(root string) {}\n",
			old:      "func Chdir(root string) int {}\nreturn 1\n",
			wantHint: true,
		},
		{
			name:     "absent span, nothing similar (directive only)",
			before:   "package main\nfunc Chdir(root string) {}\n",
			old:      "const zzq zzq2 zzq3\nzzq4 zzq5\n",
			wantHint: false,
		},
		{
			name:     "span longer than the file (directive only)",
			before:   "package main\n",
			old:      "line one\nline two\nline three\nline four\nline five\n",
			wantHint: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.go", tc.before)
			_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": "x",
			}), headlessDeps{})
			if err == nil {
				t.Fatalf("edit should error, got none")
			}
			got := err.Error()
			if !strings.Contains(got, "old_string not found") {
				t.Errorf("error should be the not-found failure; got: %q", got)
			}
			if !strings.Contains(got, "do not script this change through bash") {
				t.Errorf("directive missing from not-found failure; got: %q", got)
			}
			if !strings.Contains(got, "use read_file to re-read the current span") {
				t.Errorf("directive should say to re-read; got: %q", got)
			}
			if tc.wantHint && !strings.Contains(got, "closest region") {
				t.Errorf("hint expected but missing; got: %q", got)
			}
			if !tc.wantHint && strings.Contains(got, "closest region") {
				t.Errorf("hint present but not expected; got: %q", got)
			}
		})
	}
}

// TestEditFileTolerantMatchPreservesFileLineContent locks in a tier-2
// (whitespace-insensitive) tolerant match where the model's old_string and
// the file line disagree on leading whitespace: the replacement must land
// re-indented to the file's own indentation, not clipped to the model's.
func TestEditFileTolerantMatchPreservesFileLineContent(t *testing.T) {
	cases := []struct {
		name   string
		before string
		old    string
		new    string
		want   string // the exact line expected in the file after the edit
	}{
		{
			// Tab-indented line; the model's old_string omits the tab and the
			// trailing brace. The replacement must land on the file's own line
			// with the file's tab preserved.
			name:   "tab-indented line with model's old_string missing tab",
			before: "package main\nfunc f() {\n\treturnStart\n\treturn 1\n}\n",
			old:    "returnStart",
			new:    "returnStart() {",
			want:   "\treturnStart() {\n",
		},
		{
			// Space-indented line; the model's old_string uses a tab. The
			// replacement must land with the file's own space indentation.
			name:   "space-indented line with model's old_string using a tab",
			before: "package main\n  returnStart\n",
			old:    "\treturnStart",
			new:    "\treturnStart() {",
			want:   "  returnStart() {\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.go", tc.before)
			out, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": tc.new,
			}), headlessDeps{})
			if err != nil {
				t.Fatalf("tier-2 tolerant match should land the edit: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !strings.Contains(string(data), tc.want) {
				t.Errorf("file line was mangled by the tolerant match; got: %q (result: %q)", string(data), out)
			}
		})
	}
}

// TestEditFileEditsArrayPropagatesFailureHint proves the "edit %d:" prefix
// carries the underlying failure hint through the edits-array path, so a bad
// anchor in a batch is still pointed at the real file content.
func TestEditFileEditsArrayPropagatesFailureHint(t *testing.T) {
	before := "package main\nfunc Chdir(root string) {}\n"
	path := seedEditFile(t, "f.go", before)

	_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "func Chdir(root string) {}", "new_string": "func Chdir(root string) int {}"},
			{"old_string": "func T.Chdir(root string) {}", "new_string": "x"},
		},
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("bad anchor in edits array should error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "edit 2:") {
		t.Errorf("error should be prefixed with the edit index; got: %q", got)
	}
	if !strings.Contains(got, "not found") {
		t.Errorf("error should propagate the not-found reason; got: %q", got)
	}
}

// TestEditFileGuardDropWarning proves a landed edit whose removal drops lines
// containing conditionals or returns (if/for/switch/return) that do not
// reappear in new_string appends a GUARD-DROP WARNING naming the dropped
// line — the shape of the #208 incident where a large block edit silently
// deleted the subagentDepth guard from gateShell. Plain removals, and
// replacements where the dropped conditional line reappears in new_string,
// stay silent. The existing net-line-count WARNING ("removed N lines")
// fires on the same shape too; this test pins the guard-specific text so a
// regression to the generic warning cannot pass. See #210.
func TestEditFileGuardDropWarning(t *testing.T) {
	cases := []struct {
		name     string
		file     string // seeded file name (defaults to f.go when empty)
		before   string
		old      string
		new      string
		wantWarn bool
	}{
		{
			name:     "removal drops an if-guard and return not in new_string — warns",
			before:   "package main\nfunc f() int {\n\tif x != 0 {\n\t\treturn 1\n\t}\n\treturn 2\n}\n",
			old:      "if x != 0 {\n\t\treturn 1\n\t}\n\treturn 2",
			new:      "return 2",
			wantWarn: true,
		},
		{
			name:     "removal drops a for-loop guard not in new_string — warns",
			before:   "package main\nfunc f() int {\n\tfor _, v := range vs {\n\t\tif v == 0 {\n\t\t\tcontinue\n\t\t}\n\t\ts += v\n\t}\n\treturn s\n}\n",
			old:      "for _, v := range vs {\n\t\tif v == 0 {\n\t\t\tcontinue\n\t\t}\n\t\ts += v\n\t}",
			new:      "",
			wantWarn: true,
		},
		{
			name:     "removal of a plain assignment — no warning",
			before:   "package main\nvar x = 1\n",
			old:      "var x = 1\n",
			new:      "",
			wantWarn: false,
		},
		{
			name:     "removal where the dropped guard line reappears in new_string — no warning",
			before:   "package main\nfunc f() int {\n\tif x {\n\t\treturn 1\n\t}\n\treturn 2\n}\n",
			old:      "if x {\n\t\treturn 1\n\t}",
			new:      "if x {\n\t\treturn 1\n\t}\n// kept",
			wantWarn: false,
		},
		{
			name:     "removal drops an else-guard not in new_string — warns",
			before:   "package main\nfunc f() {\n\tif x {\n\t\ta()\n\t} else {\n\t\tb()\n\t}\n}\n",
			old:      "} else {\n\t\tb()\n\t}",
			new:      "}",
			wantWarn: true,
		},
		{
			name:     "removal drops a case clause not in new_string — warns",
			before:   "package main\nfunc f() {\n\tswitch x {\n\tcase 0:\n\t\tg()\n\tcase 1:\n\t\th()\n\t}\n}\n",
			old:      "case 1:\n\t\th()",
			new:      "",
			wantWarn: true,
		},
		{
			name:     "removal drops a panic not in new_string — warns",
			before:   "package main\nfunc f() {\n\tif err != nil {\n\t\tpanic(err)\n\t}\n}\n",
			old:      "\t\tpanic(err)",
			new:      "\t\tlog.Printf(\"err: %v\", err)",
			wantWarn: true,
		},
		{
			name:     "removal drops an unlock not in new_string — warns",
			before:   "package main\nfunc f() {\n\tdefer mu.Unlock()\n}\n",
			old:      "\tdefer mu.Unlock()\n",
			new:      "",
			wantWarn: true,
		},
		{
			name:     "removal of a line containing a keyword inside an identifier — no warning",
			before:   "package main\nvar returned = true\n",
			old:      "var returned = true\n",
			new:      "",
			wantWarn: false,
		},
		{
			// The dropped guard line `\tif err != nil {` also appears EARLIER in
			// the file (a sibling check). It does not reappear in the
			// replacement's added lines, so the warning must still fire — a
			// comparison against the whole file would miss it (#210).
			name:     "dropped guard also appears elsewhere in the file — still warns",
			before:   "package main\nfunc run() error {\n\tclient := dial()\n\tif client == nil {\n\t\treturn errNoClient\n\t}\n\tctx := context.Background()\n\tif ctx == nil {\n\t\treturn errNoCtx\n\t}\n\treturn do(client, ctx)\n}\nfunc do(client, ctx any) error {\n\tif err := client.(*c).Call(ctx); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n",
			old:      "\tif err := client.(*c).Call(ctx); err != nil {\n\t\treturn err\n\t}\n",
			new:      "",
			wantWarn: true,
		},
		{
			// Same as above but the dropped `return err` is re-added by the
			// replacement (a move, not a loss): the warning stays silent.
			name:     "dropped guard re-added in new_string — no warning",
			before:   "package main\nfunc a() error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n",
			old:      "\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil",
			new:      "\treturn nil\n\tif err != nil {\n\t\treturn err\n\t}",
			wantWarn: false,
		},
		{
			// Prose in a Markdown file carries `for`/`if` as plain English
			// words (or mid-line, as in a string literal): a README edit must
			// never raise the GUARD DROPPED warning (#210).
			name:     "removal of .md prose mentioning 'for'/'if' — no warning",
			before:   "# Usage\nThis is for users who want to know.\nIf the build fails, retry.\n",
			old:      "If the build fails, retry.\n",
			new:      "",
			wantWarn: false,
		},
		{
			// A code line whose keyword lives INSIDE a string literal ("wait for
			// the lock") has no guard shape: `for` is not the line's first
			// token and there is no .Lock( call (#210).
			name:     "code line with 'for' inside a string literal — no warning",
			before:   "package main\nvar msg = \"wait for the lock\"\n",
			old:      "var msg = \"wait for the lock\"\n",
			new:      "",
			wantWarn: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fname := tc.file
			if fname == "" {
				fname = "f.go"
			}
			path := seedEditFile(t, fname, tc.before)
			out, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": tc.new,
			}), headlessDeps{})
			if err != nil {
				t.Fatalf("edit_file: %v", err)
			}
			hasGuardWarn := strings.Contains(out, "GUARD DROPPED")
			if hasGuardWarn != tc.wantWarn {
				t.Errorf("GUARD-DROPPED warning presence = %v, want %v; got: %q", hasGuardWarn, tc.wantWarn, out)
			}
		})
	}
}

// TestEditFileNotFoundStaleViewFraming proves the generic not-found error
// states the observable fact — old_string does not match the file as it is
// now — WITHOUT asserting a cause (it may be stale, but equally it may be
// mistyped, invented, or copied from another file), and labels the
// closest-region snippet (when one is attached) as the file's CURRENT content
// so the model's next call copies from fresh text. See #210.
func TestEditFileNotFoundStaleViewFraming(t *testing.T) {
	cases := []struct {
		name     string
		before   string
		old      string
		wantHint bool // true → the closest-region hint (labeled CURRENT content) is attached
	}{
		{
			name:     "invented span: old_string is a line the file has never had",
			before:   "package main\nfunc Chdir(root string) {}\n",
			old:      "func T.Chdir(root string) {}",
			wantHint: true,
		},
		{
			name:     "copied span: multi-line block whose first line is absent",
			before:   "package main\nfunc Chdir(root string) {}\n\nvar x int\n",
			old:      "func Changed(root string) int {\nfunc Chdir(root string) {}\n",
			wantHint: true,
		},
		{
			name:     "mistyped span with nothing similar in the file",
			before:   "package main\nfunc Chdir(root string) {}\n",
			old:      "const zzq zzq2 zzq3\nzzq4 zzq5\n",
			wantHint: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.go", tc.before)
			_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": "x",
			}), headlessDeps{})
			if err == nil {
				t.Fatalf("edit should error, got none")
			}
			got := err.Error()
			// The error states the observable fact without claiming a cause.
			for _, sub := range []string{"not found", "does not match the file as it is now"} {
				if !strings.Contains(got, sub) {
					t.Errorf("error should contain %q; got: %q", sub, got)
				}
			}
			// It must not assert the view is stale as a fact.
			if strings.Contains(got, "your view of the span is stale") {
				t.Errorf("error must not assert the view is stale; got: %q", got)
			}
			// The closest-region snippet, when attached, is labeled the
			// file's CURRENT content — the text the model copies its next
			// old_string from — not an undated guess.
			hasHint := strings.Contains(got, "closest region")
			if hasHint != tc.wantHint {
				t.Errorf("hint presence = %v, want %v; got: %q", hasHint, tc.wantHint, got)
			}
			if hasHint && !strings.Contains(got, "CURRENT content") {
				t.Errorf("closest-region hint should be labeled the file's CURRENT content; got: %q", got)
			}
		})
	}
}

// TestEditFileNotFoundWhitespaceOnlyDifference proves that when the only
// difference between old_string and the file is whitespace — specifically
// INTERIOR whitespace, as in a gofmt realignment of aligned assignments or
// comments that the tier-1/2 tolerant match cannot bridge (it trims only
// leading/trailing) — the not-found error explicitly says "only whitespace
// differs" rather than a generic "not found". See #210.
//
// (Leading/trailing re-indentation is NOT covered here: that shape is
// bridged by the tier-2 tolerant match and lands, re-indented, by design —
// see TestEditFileTolerantMatchPreservesFileLineContent.)
func TestEditFileNotFoundWhitespaceOnlyDifference(t *testing.T) {
	cases := []struct {
		name   string
		before string
		old    string
	}{
		{
			name:   "gofmt aligned assignments, old_string has single spaces",
			before: "package main\nvar (\n\ta  = 1\n\tbb = 2\n)\n",
			old:    "a = 1\nbb = 2\n",
		},
		{
			name:   "aligned struct fields, old_string lacks the padding",
			before: "type T struct {\n\tX int    `json:\"x\"`\n\tY string `json:\"y\"`\n}\n",
			old:    "X int `json:\"x\"`\nY string `json:\"y\"`\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.go", tc.before)
			_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
				"path": path, "old_string": tc.old, "new_string": "x",
			}), headlessDeps{})
			if err == nil {
				t.Fatalf("edit should error, got none")
			}
			got := err.Error()
			if !strings.Contains(got, "not found") {
				t.Errorf("error should say not found; got: %q", got)
			}
			if !strings.Contains(got, "only whitespace differs") {
				t.Errorf("error should say 'only whitespace differs'; got: %q", got)
			}
		})
	}
}
