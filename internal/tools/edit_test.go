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
// warning in the result and the first-match / closest-line guidance in failures
// — so the prose can't silently regress from the behavior below it.
func TestEditFileDeclarationDocumentsResultAndFailureNotes(t *testing.T) {
	desc := EditFile.Function.Description
	for _, sub := range []string{
		"lines removed/added",
		"removes more lines than it adds",
		"first match's line",
		"closest line in the file",
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
			_, err := Execute(context.Background(), editArgs(t, tc.args), headlessDeps{})
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

	out, err := Execute(context.Background(), editArgs(t, map[string]any{
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
			out, err := Execute(context.Background(), editArgs(t, map[string]any{
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
	out, err := Execute(context.Background(), editArgs(t, map[string]any{
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

	_, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "x := 1", "new_string": "x := 2",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("ambiguous exact match should error, got none")
	}
	got := err.Error()
	for _, sub := range []string{"found 2 times", "first at line 2", "replace_all"} {
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

	_, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "\tx := 1", "new_string": "x := 2",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("ambiguous tolerant match should error, got none")
	}
	got := err.Error()
	for _, sub := range []string{"matches 2 places", "first at line 2", "replace_all"} {
		if !strings.Contains(got, sub) {
			t.Errorf("error should contain %q; got: %q", sub, got)
		}
	}
}

// TestEditFileNotFoundCarriesClosestLineHint proves a guessed anchor (never
// read, text that isn't in the file) comes back with the closest-line hint from
// the tolerant path, so the model is pointed at what's actually in the file.
// See #153.
func TestEditFileNotFoundCarriesClosestLineHint(t *testing.T) {
	before := "package main\nfunc Chdir(root string) {}\n"
	path := seedEditFile(t, "f.go", before)

	// A guessed anchor close to but not equal to the real line.
	_, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path": path, "old_string": "func T.Chdir(root string) {}", "new_string": "func Chdir(root string) {}",
	}), headlessDeps{})
	if err == nil {
		t.Fatalf("guessed anchor should error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "not found") {
		t.Errorf("error should say not found; got: %q", got)
	}
	if !strings.Contains(got, "closest is line 2") {
		t.Errorf("error should point at the closest real line; got: %q", got)
	}
}

// TestEditFileEditsArrayPropagatesFailureHint proves the "edit %d:" prefix
// carries the underlying failure hint through the edits-array path, so a bad
// anchor in a batch is still pointed at the real file content.
func TestEditFileEditsArrayPropagatesFailureHint(t *testing.T) {
	before := "package main\nfunc Chdir(root string) {}\n"
	path := seedEditFile(t, "f.go", before)

	_, err := Execute(context.Background(), editArgs(t, map[string]any{
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
