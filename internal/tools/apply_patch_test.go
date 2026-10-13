package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// patchCall marshals an apply_patch call whose input is patch.
func patchCall(t *testing.T, patch string) ToolCall {
	t.Helper()
	b, err := json.Marshal(map[string]any{"input": patch})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return ToolCall{Function: FunctionCall{Name: FunctionApplyPatch, Arguments: string(b)}}
}

// deleteDeps is headlessDeps with remove_path allowed under root.
type deleteDeps struct {
	headlessDeps
	root string
}

func (d deleteDeps) AllowDelete() (string, bool) { return d.root, true }

func readBack(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back %s: %v", p, err)
	}
	return string(b)
}

func TestParsePatch(t *testing.T) {
	tests := []struct {
		name    string
		patch   string
		want    []patchOp
		wantErr string
	}{
		{
			name:  "update with anchor and context",
			patch: "*** Begin Patch\n*** Update File: a.txt\n@@ func f\n ctx\n-old\n+new\n*** End Patch",
			want:  []patchOp{{kind: patchUpdate, path: "a.txt", hunks: []patchHunk{{anchor: "func f", old: "ctx\nold\n", new: "ctx\nnew\n"}}}},
		},
		{
			name:  "first hunk may omit @@; a blank line is blank context",
			patch: "*** Begin Patch\n*** Update File: a.txt\n x\n\n-y\n+z\n*** End Patch",
			want:  []patchOp{{kind: patchUpdate, path: "a.txt", hunks: []patchHunk{{old: "x\n\ny\n", new: "x\n\nz\n"}}}},
		},
		{
			name:  "add, delete, move",
			patch: "*** Begin Patch\n*** Add File: n.txt\n+one\n+two\n*** Delete File: d.txt\n*** Update File: m.txt\n*** Move to: m2.txt\n@@\n-a\n+b\n*** End Patch",
			want: []patchOp{
				{kind: patchAdd, path: "n.txt", body: "one\ntwo\n"},
				{kind: patchDelete, path: "d.txt"},
				{kind: patchUpdate, path: "m.txt", moveTo: "m2.txt", hunks: []patchHunk{{old: "a\n", new: "b\n"}}},
			},
		},
		{
			name:  "a heredoc wrapper around the envelope is tolerated",
			patch: "apply_patch <<'EOF'\n*** Begin Patch\n*** Delete File: d.txt\n*** End Patch\nEOF",
			want:  []patchOp{{kind: patchDelete, path: "d.txt"}},
		},
		{name: "no envelope", patch: "-a\n+b", wantErr: "must start with"},
		{name: "no sections", patch: "*** Begin Patch\n*** End Patch", wantErr: "no file sections"},
		{name: "update without hunks", patch: "*** Begin Patch\n*** Update File: a.txt\n*** End Patch", wantErr: "has no hunks"},
		{name: "unprefixed hunk line", patch: "*** Begin Patch\n*** Update File: a.txt\n@@\nbare\n*** End Patch", wantErr: "starts with ' ', '-' or '+'"},
		{name: "unprefixed added-file line", patch: "*** Begin Patch\n*** Add File: a.txt\nbare\n*** End Patch", wantErr: "starts with '+'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePatch(tc.patch)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePatch: %v", err)
			}
			gb, _ := json.Marshal(opsView(got))
			wb, _ := json.Marshal(opsView(tc.want))
			if string(gb) != string(wb) {
				t.Errorf("ops =\n%s\nwant\n%s", gb, wb)
			}
		})
	}
}

// opsView makes patchOps comparable as JSON (their fields are unexported).
func opsView(ops []patchOp) []map[string]any {
	var out []map[string]any
	for _, op := range ops {
		var hunks []map[string]string
		for _, h := range op.hunks {
			hunks = append(hunks, map[string]string{"anchor": h.anchor, "old": h.old, "new": h.new})
		}
		out = append(out, map[string]any{"kind": op.kind, "path": op.path, "moveTo": op.moveTo, "body": op.body, "hunks": hunks})
	}
	return out
}

func TestApplyHunks(t *testing.T) {
	file := "def a():\n    return None\n\ndef b():\n    return None\n"
	tests := []struct {
		name    string
		hunks   []patchHunk
		want    string
		wantErr string
	}{
		{
			name:  "an anchor tells a repeated block apart",
			hunks: []patchHunk{{anchor: "def b():", old: "    return None\n", new: "    return 1\n"}},
			want:  "def a():\n    return None\n\ndef b():\n    return 1\n",
		},
		{
			name:  "after an anchor the FIRST match wins, though the block repeats below",
			hunks: []patchHunk{{anchor: "def a():", old: "    return None\n", new: "    return 0\n"}},
			want:  "def a():\n    return 0\n\ndef b():\n    return None\n",
		},
		{
			name:    "without an anchor a repeated block is ambiguous, in this format's terms",
			hunks:   []patchHunk{{old: "    return None\n", new: "    return 1\n"}},
			wantErr: "an '@@' line above the change",
		},
		{
			name:    "an anchor that is not in the file",
			hunks:   []patchHunk{{anchor: "def c():", old: "    return None\n", new: "x\n"}},
			wantErr: "'@@ def c():' line is not in the file",
		},
		{
			name:  "a pure addition after its anchor",
			hunks: []patchHunk{{anchor: "def a():", new: "    pass\n"}},
			want:  "def a():\n    pass\n    return None\n\ndef b():\n    return None\n",
		},
		{
			name:  "a pure addition with no anchor appends",
			hunks: []patchHunk{{new: "# end\n"}},
			want:  file + "# end\n",
		},
		{
			name:  "indentation drift lands through edit_file's tolerance",
			hunks: []patchHunk{{anchor: "def a():", old: "return None\n", new: "return 0\n"}},
			want:  "def a():\n    return 0\n\ndef b():\n    return None\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := applyHunks(file, tc.hunks)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "old_string") || strings.Contains(err.Error(), "replace_all") {
					t.Errorf("error names edit_file's arguments, which this format doesn't have: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyHunks: %v", err)
			}
			if got != tc.want {
				t.Errorf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestApplyPatchToolUpdateAddDeleteMove(t *testing.T) {
	dir := t.TempDir()
	upd := filepath.Join(dir, "upd.txt")
	del := filepath.Join(dir, "del.txt")
	mov := filepath.Join(dir, "mov.txt")
	for p, c := range map[string]string{upd: "a\nb\nc\n", del: "gone\n", mov: "x\n"} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	add := filepath.Join(dir, "sub", "add.txt")
	moved := filepath.Join(dir, "moved.txt")
	patch := "*** Begin Patch\n" +
		"*** Update File: " + upd + "\n@@\n a\n-b\n+B\n c\n" +
		"*** Add File: " + add + "\n+new\n" +
		"*** Delete File: " + del + "\n" +
		"*** Update File: " + mov + "\n*** Move to: " + moved + "\n@@\n-x\n+y\n" +
		"*** End Patch"
	out, _, err := Execute(context.Background(), patchCall(t, patch), deleteDeps{root: dir})
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if got := readBack(t, upd); got != "a\nB\nc\n" {
		t.Errorf("updated file = %q", got)
	}
	if !strings.Contains(out, "edited "+upd) {
		t.Errorf("result should carry edit_file's own summary for the update; got %q", out)
	}
	if got := readBack(t, add); got != "new\n" {
		t.Errorf("added file = %q", got)
	}
	if _, err := os.Stat(del); !os.IsNotExist(err) {
		t.Errorf("deleted file still there (err %v)", err)
	}
	if _, err := os.Stat(mov); !os.IsNotExist(err) {
		t.Errorf("moved file's old path still there (err %v)", err)
	}
	if got := readBack(t, moved); got != "y\n" {
		t.Errorf("moved file = %q", got)
	}
}

// Anything knowable up front fails the whole patch before the first write.
func TestApplyPatchToolPreconditionsWriteNothing(t *testing.T) {
	tests := []struct {
		name    string
		deps    func(dir string) ToolDeps
		section func(dir string) string
		wantErr string
	}{
		{
			name: "update of a missing file",
			deps: func(string) ToolDeps { return headlessDeps{} },
			section: func(dir string) string {
				return "*** Update File: " + filepath.Join(dir, "nope.txt") + "\n@@\n-a\n+b\n"
			},
			wantErr: "no such file",
		},
		{
			name: "add over an existing file",
			deps: func(string) ToolDeps { return headlessDeps{} },
			section: func(dir string) string {
				return "*** Add File: " + filepath.Join(dir, "keep.txt") + "\n+clobber\n"
			},
			wantErr: "already exists",
		},
		{
			name: "delete while remove_path is disabled",
			deps: func(string) ToolDeps { return headlessDeps{} },
			section: func(dir string) string {
				return "*** Delete File: " + filepath.Join(dir, "keep.txt") + "\n"
			},
			wantErr: "remove_path, which is disabled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first := filepath.Join(dir, "first.txt")
			keep := filepath.Join(dir, "keep.txt")
			for _, p := range []string{first, keep} {
				if err := os.WriteFile(p, []byte("a\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// A valid first section, then the failing one: the first must not land.
			patch := "*** Begin Patch\n*** Update File: " + first + "\n@@\n-a\n+changed\n" + tc.section(dir) + "*** End Patch"
			_, _, err := Execute(context.Background(), patchCall(t, patch), tc.deps(dir))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got := readBack(t, first); got != "a\n" {
				t.Errorf("the valid first section was applied (%q); a failed precondition must write nothing", got)
			}
		})
	}
}

func TestMutatedPaths(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: n.txt\n+x\n*** Update File: m.txt\n*** Move to: m2.txt\n@@\n-a\n+b\n*** Delete File: d.txt\n*** End Patch"
	tests := []struct {
		name string
		call ToolCall
		want []string
	}{
		{"edit_file", editCallPath("edit_file", "a.go"), []string{"a.go"}},
		{"write_file", editCallPath("write_file", "b.go"), []string{"b.go"}},
		{"remove_path", editCallPath("remove_path", "c.go"), []string{"c.go"}},
		{"apply_patch: every section, both sides of a move", patchCall(t, patch), []string{"n.txt", "m.txt", "m2.txt", "d.txt"}},
		{"apply_patch that doesn't parse", patchCall(t, "nonsense"), nil},
		{"a read", editCallPath("read_file", "a.go"), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MutatedPaths(tc.call)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("MutatedPaths = %v, want %v", got, tc.want)
			}
		})
	}
}

func editCallPath(name, path string) ToolCall {
	return ToolCall{Function: FunctionCall{Name: name, Arguments: `{"path":"` + path + `"}`}}
}

// A missing new_string is not an empty one: absence is refused (the gpt-oss
// probe's silent deletion), an explicit "" still deletes.
func TestEditFileRequiresNewString(t *testing.T) {
	tests := []struct {
		name    string
		args    func(path string) map[string]any
		wantErr string
		want    string
	}{
		{
			name:    "single edit without new_string",
			args:    func(p string) map[string]any { return map[string]any{"path": p, "old_string": "b\n"} },
			wantErr: "new_string is missing",
		},
		{
			name: "edits array item without new_string",
			args: func(p string) map[string]any {
				return map[string]any{"path": p, "edits": []map[string]any{{"old_string": "a\n", "new_string": "A\n"}, {"old_string": "b\n"}}}
			},
			wantErr: "edit 2: new_string is missing",
		},
		{
			name: "explicit empty new_string deletes",
			args: func(p string) map[string]any { return map[string]any{"path": p, "old_string": "b\n", "new_string": ""} },
			want: "a\nc\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := seedEditFile(t, "f.txt", "a\nb\nc\n")
			_, _, err := Execute(context.Background(), editArgs(t, tc.args(path)), headlessDeps{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				if got := readBack(t, path); got != "a\nb\nc\n" {
					t.Errorf("file changed on a refused edit: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("edit: %v", err)
			}
			if got := readBack(t, path); got != tc.want {
				t.Errorf("file = %q, want %q", got, tc.want)
			}
		})
	}
}
