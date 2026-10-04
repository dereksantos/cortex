package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// TestDetectInPlaceRewrites pins the bash in-place-rewrite detector (issue
// #201): the forms that rewrite files are named, and the forms that only
// read or write outside the known targets are not.
func TestDetectInPlaceRewrites(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string // raw targets, in first-occurrence order
	}{
		{"sed -i single file", `sed -i 's/foo/bar/g' internal/tools/attribution.go`, []string{"internal/tools/attribution.go"}},
		{"sed -i with -e script", `sed -i -e 's/a/b/' f.go g.go`, []string{"f.go", "g.go"}},
		{"sed -i backup suffix form", `sed -i.bak 's/x/y/' f.go`, []string{"f.go"}},
		{"sed --in-place", `sed --in-place 's/x/y/' f.go`, []string{"f.go"}},
		{"sed without -i only reads", `sed -n '2,4p' f.go`, nil},
		{"ed in-place", `ed -s f.go`, []string{"f.go"}},
		{"ed with command args", `ed -s -n f.go`, []string{"f.go"}},
		{"perl -pi in place", `perl -pi -e 's/x/y/g' internal/tools/golden_test.go`, []string{"internal/tools/golden_test.go"}},
		{"perl -i -p separate flags", `perl -i -p -e 's/x/y/' f.go`, []string{"f.go"}},
		{"perl without -i only reads", `perl -ne 'print' f.go`, nil},
		{"plain awk only reads its file list", `awk 'NR>3' a.go b.go`, nil},
		{"awk -v flag still reads", `awk -v N=3 'NR>N' a.go`, nil},
		{"gawk -i inplace rewrites its file list", `awk -i inplace '{print}' f.go`, []string{"f.go"}},
		{"gawk --in-place rewrites its file list", `awk --in-place 'NR>3' f.go`, []string{"f.go"}},
		{"env-prefixed sed rewrites", `LC_ALL=C sed -i 's/a/b/' f.go`, []string{"f.go"}},
		{"multi env-prefixed sed rewrites", `LC_ALL=C LANG=C sed -i 's/a/b/' f.go`, []string{"f.go"}},
		{"python3 -c with arg", `python3 -c 'import sys; open(sys.argv[1],"w").write("x")' internal/tools/golden_test.go`, []string{"internal/tools/golden_test.go"}},
		{"python3 -c without arg has no target", `python3 -c 'import sys; open(sys.argv[1],"w")'`, nil},
		{"python script file (not -c) is not scanned", `python3 script.py`, nil},
		{"ruby -c with arg", `ruby -c 'File.open("f","w")' f.txt`, []string{"f.txt"}},
		{"node -c with arg", `node -c 'require("fs")' f.js`, []string{"f.js"}},
		{"redirect overwrites", `awk '{print}' f.go > out.txt`, []string{"out.txt"}},
		{"append redirect", `echo x >> log.txt`, []string{"log.txt"}},
		{"two redirects", `awk '{print}' a > one.txt && awk '{print}' b > two.txt`, []string{"one.txt", "two.txt"}},
		{"chained sed then redirect", `sed -i 's/x/y/' f.go && cat f.go > copy.go`, []string{"f.go", "copy.go"}},
		{"absolute path target", `sed -i 's/x/y/' /repo/f.go`, []string{"/repo/f.go"}},
		{"dedupe by raw path", `sed -i 's/x/y/' f.go; sed -i 's/a/b/' f.go`, []string{"f.go"}},
		{"quoted redirect target keeps quotes in the note", `echo x > "out file.txt"`, []string{`"out file.txt"`}},
		{"python heredoc with redirect (issue #201)", "python3 - > out.py <<'EOF'\nprint('x')\nEOF", []string{"out.py"}},
		{"python heredoc truncated at marker (issue #201)", "python3 - <<'EOF'\nimport io\nlines=open('internal/tools/golden_test.go').read().split('\\n')\nEOF", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectInPlaceRewrites(tc.cmd)
			if len(got) != len(tc.want) {
				t.Fatalf("detectInPlaceRewrites(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
			for i, w := range tc.want {
				if got[i].raw != w {
					t.Errorf("target %d = %q, want %q (cmd %q)", i, got[i].raw, w, tc.cmd)
				}
			}
		})
	}
}

// TestDetectInPlaceRewrites_NoFalsePositives pins that routine, read-only
// commands name NO target — the note must not fire on a `go test` or a
// redirect to /dev/null. The read-only awk rows pin the false positive the
// header's conservative claim exists to prevent: plain awk only READS its
// file list (it has no in-place mode; gawk's -i inplace is the exception).
func TestDetectInPlaceRewrites_NoFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"plain go test", `go test ./...`},
		{"ls", `ls -la`},
		{"grep with redirect to devnull", `grep -rn TODO . > /dev/null`},
		{"fd dup 2>&1", `go test ./... 2>&1`},
		{"echo without redirect", `echo hello`},
		{"git status", `git status`},
		{"plain awk only reads its file list", `awk 'NR>3' a.go`},
		{"awk with -v flag only reads", `awk -v N=3 'NR>N' a.go`},
		{"perl -c compile-checks (reads) a file, never rewrites", `perl -c f.pl`},
		{"sed mentioning a file in the script is NOT the target", `sed -n '/def f/:q' f.py`},
		{"python -c mentioning a path in the script only", `python3 -c 'print(open("internal/tools/attribution.go").read())'`},
		{"$VAR target is unknowable", `sed -i 's/x/y/' $FILE`},
		{"empty command", `   `},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectInPlaceRewrites(tc.cmd)
			if len(got) != 0 {
				t.Errorf("detectInPlaceRewrites(%q) = %v, want none", tc.cmd, got)
			}
		})
	}
}

// TestInPlaceRewriteNote pins the model-facing note: it names the touched
// targets (workdir-relative when anchored) and steers to edit_file/write_file.
func TestInPlaceRewriteNote(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		wd   string
		want []string // substrings the note must contain
	}{
		{"no target means no note", `go test ./...`, "", nil},
		{"sed target named", `sed -i 's/x/y/' internal/tools/attribution.go`, "", []string{
			"internal/tools/attribution.go",
			"edit_file",
			"write_file",
			"post-edit hook",
		}},
		{"workdir-anchored target made relative", `sed -i 's/x/y/' /repo/f.go`, "/repo", []string{"f.go"}},
		{"outside-workdir target keeps raw spelling", `sed -i 's/x/y/' /elsewhere/f.go`, "/repo", []string{"/elsewhere/f.go"}},
		{"multiple targets in order", `sed -i 's/x/y/' a.go && cat a > b.txt`, "", []string{"a.go", "b.txt"}},
		{"script target named", `python3 -c 'open(sys.argv[1],"w")' f.go`, "", []string{"f.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inPlaceRewriteNote(wdDeps{wd: tc.wd}, tc.cmd)
			if len(tc.want) == 0 {
				if got != "" {
					t.Fatalf("note = %q, want empty (cmd %q)", got, tc.cmd)
				}
				return
			}
			if got == "" {
				t.Fatalf("note empty, want a note (cmd %q)", tc.cmd)
			}
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("note %q should contain %q (cmd %q)", got, sub, tc.cmd)
				}
			}
		})
	}
}

// hookCmdDeps is wdNoteDeps plus the ProjectCommands and WorkspaceTrusted
// capabilities: a session whose workspace has a per-file gofmt format command
// (the same shape Discover returns for a Go module) and a trust decision.
type hookCmdDeps struct {
	wdNoteDeps
	cmds    projectcmd.Commands
	trusted bool
}

func (d hookCmdDeps) ProjectCommands() projectcmd.Commands { return d.cmds }
func (d hookCmdDeps) WorkspaceTrusted() bool               { return d.trusted }

// goFmtCmds is a per-file gofmt format command restricted to .go files — the
// same shape Discover returns for a Go module (goRepoCmds in
// project_command_hook_test.go, without the lint role the per-edit hook does
// not run).
func goFmtCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
	}
}

// TestInPlaceRewriteHookNote pins step 4 (issue #201): a detected in-place
// rewrite that targets a workdir path runs the SAME format-only post-edit
// hook write_file/edit_file run, and the note is returned. The untrusted and
// no-format-command cases return "" (no-op).
func TestInPlaceRewriteHookNote(t *testing.T) {
	t.Run("trusted workdir target runs gofmt and notes it", func(t *testing.T) {
		wd := t.TempDir()
		// Unformatted Go file inside the workdir.
		unformatted := "package main\n\nfunc main() {\n  x :=    1\n  _ = x\n}\n"
		if err := os.WriteFile(filepath.Join(wd, "main.go"), []byte(unformatted), 0o644); err != nil {
			t.Fatal(err)
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true},
			`sed -i 's/x/y/' main.go`)
		if !strings.Contains(got, "formatted") {
			t.Fatalf("expected a format note, got %q", got)
		}
		if !strings.Contains(got, "main.go") {
			t.Errorf("note should name the file, got %q", got)
		}
		// The hook actually ran gofmt: the file on disk is now tab-indented.
		data, err := os.ReadFile(filepath.Join(wd, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "  x :=    1") {
			t.Errorf("file should be gofmt-formatted by the hook, got %q", data)
		}
	})

	t.Run("untrusted workspace is a no-op (no note)", func(t *testing.T) {
		wd := t.TempDir()
		if err := os.WriteFile(filepath.Join(wd, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), false},
			`sed -i 's/x/y/' main.go`)
		if got != "" {
			t.Fatalf("untrusted workspace should yield no note, got %q", got)
		}
	})

	t.Run("no format command is a no-op (no note)", func(t *testing.T) {
		wd := t.TempDir()
		if err := os.WriteFile(filepath.Join(wd, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, projectcmd.Commands{}, true},
			`sed -i 's/x/y/' main.go`)
		if got != "" {
			t.Fatalf("no format command should yield no note, got %q", got)
		}
	})

	t.Run("no workdir anchor is a no-op (no note)", func(t *testing.T) {
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: ""}}, goFmtCmds(), true},
			`sed -i 's/x/y/' main.go`)
		if got != "" {
			t.Fatalf("no workdir anchor should yield no note, got %q", got)
		}
	})

	t.Run("target outside the workdir is a no-op (no note)", func(t *testing.T) {
		wd := t.TempDir()
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true},
			`sed -i 's/x/y/' /elsewhere/main.go`)
		if got != "" {
			t.Fatalf("outside-workdir target should yield no note, got %q", got)
		}
	})

	t.Run("extension mismatch is a no-op (no note)", func(t *testing.T) {
		wd := t.TempDir()
		if err := os.WriteFile(filepath.Join(wd, "readme.md"), []byte("hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true},
			`sed -i 's/x/y/' readme.md`)
		if got != "" {
			t.Fatalf("gofmt does not apply to .md, expected no note, got %q", got)
		}
	})

	t.Run("multiple workdir targets each get a note", func(t *testing.T) {
		wd := t.TempDir()
		unformatted := "package main\n\nfunc main() {\n  x :=    1\n  _ = x\n}\n"
		for _, name := range []string{"a.go", "b.go"} {
			if err := os.WriteFile(filepath.Join(wd, name), []byte(unformatted), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true},
			`sed -i 's/x/y/' a.go && sed -i 's/x/y/' b.go`)
		if !strings.Contains(got, "a.go") || !strings.Contains(got, "b.go") {
			t.Errorf("note should name both files, got %q", got)
		}
	})
}

// TestInPlaceRewriteHookNote_ScriptAndReadTargets pins that the post-edit
// hook is NEVER run on a target the command demonstrably did not rewrite in
// place: a -c program's arguments are only a GUESS at the file it opens
// (rewriteFormScript), and plain awk only reads its file list. The steering
// note still names a detected target; the hook formats only what it is sure
// the command rewrote (issue #201 review fix).
func TestInPlaceRewriteHookNote_ScriptAndReadTargets(t *testing.T) {
	unformatted := "package main\n\nfunc main() {\n  x :=    1\n  _ = x\n}\n"

	t.Run("read-only python -c with arg: note names it, hook does not run", func(t *testing.T) {
		wd := t.TempDir()
		if err := os.WriteFile(filepath.Join(wd, "f.go"), []byte(unformatted), 0o644); err != nil {
			t.Fatal(err)
		}
		deps := hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true}
		// The steering note still fires: the detected script-form target is
		// named (it is a guess, and the note steers to the edit tools).
		if note := inPlaceRewriteNote(deps, `python3 -c 'import sys; print(open(sys.argv[1]).read())' f.go`); !strings.Contains(note, "f.go") {
			t.Fatalf("steering note should name the script target, got %q", note)
		}
		// But the hook must not run on a file the command demonstrably did
		// not rewrite in place.
		got := inPlaceRewriteHookNote(context.Background(), deps,
			`python3 -c 'import sys; print(open(sys.argv[1]).read())' f.go`)
		if got != "" {
			t.Fatalf("script-form target should not run the hook, got %q", got)
		}
		data, err := os.ReadFile(filepath.Join(wd, "f.go"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != unformatted {
			t.Errorf("file should be untouched, got %q", data)
		}
	})

	t.Run("plain awk with files: no hook, file untouched", func(t *testing.T) {
		wd := t.TempDir()
		if err := os.WriteFile(filepath.Join(wd, "a.go"), []byte(unformatted), 0o644); err != nil {
			t.Fatal(err)
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true},
			`awk 'NR>3' a.go`)
		if got != "" {
			t.Fatalf("plain awk should not run the hook, got %q", got)
		}
		data, err := os.ReadFile(filepath.Join(wd, "a.go"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != unformatted {
			t.Errorf("file should be untouched, got %q", data)
		}
	})

	t.Run("gawk -i inplace DOES run the hook", func(t *testing.T) {
		wd := t.TempDir()
		if err := os.WriteFile(filepath.Join(wd, "f.go"), []byte(unformatted), 0o644); err != nil {
			t.Fatal(err)
		}
		got := inPlaceRewriteHookNote(context.Background(),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true},
			`awk -i inplace '{print}' f.go`)
		if !strings.Contains(got, "formatted") {
			t.Fatalf("gawk -i inplace target should run the hook, got %q", got)
		}
		data, err := os.ReadFile(filepath.Join(wd, "f.go"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "  x :=    1") {
			t.Errorf("file should be gofmt-formatted by the hook, got %q", data)
		}
	})
}

// wdNoteDeps is wdDeps with a permissive GateShell so the bash tool can run
// end-to-end (the note is what these tests assert; the gate is not).
type wdNoteDeps struct {
	wdDeps
}

func (d wdNoteDeps) GateShell(ctx context.Context, command string) (string, bool) {
	return "", true
}

// TestBashInPlaceRewriteNoteEndToEnd pins that the bash tool actually APPENDS
// the note to its result when a command rewrites files — the model and the
// turn record both see which files were touched outside the edit tools.
func TestBashInPlaceRewriteNoteEndToEnd(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		wantSubs []string
	}{
		{"sed -i note appended on success", `sed -i 's/x/y/' /tmp/does-not-matter.go && true`, []string{
			"note: this command rewrites file(s) in place:",
			"/tmp/does-not-matter.go",
			"edit_file",
		}},
		{"no note for a read-only command", `echo hello`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wd := t.TempDir()
			got, err := Execute(context.Background(), bashCall(t, tc.cmd), wdNoteDeps{wdDeps{wd: wd}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tc.wantSubs) == 0 {
				if strings.Contains(got, "rewrites file(s) in place") {
					t.Errorf("read-only command should not carry the rewrite note; got %q", got)
				}
				return
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("result %q should contain %q", got, sub)
				}
			}
		})
	}
}

// TestBashInPlaceRewriteHookNoteEndToEnd pins that the bash tool actually
// RUNS the post-edit hook on a workdir target it rewrites in place and folds
// the hook note into the result — script-edits get the same format coverage
// as tool edits (issue #201, step 4).
func TestBashInPlaceRewriteHookNoteEndToEnd(t *testing.T) {
	t.Run("trusted: hook runs gofmt and the note lands in the result", func(t *testing.T) {
		wd := t.TempDir()
		t.Chdir(wd)
		unformatted := "package main\n\nfunc main() {\n  x :=    1\n  _ = x\n}\n"
		if err := os.WriteFile(filepath.Join(wd, "main.go"), []byte(unformatted), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Execute(context.Background(), bashCall(t, `sed -i 's/x/y/' main.go`),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The steering note names the target and the edit tools.
		if !strings.Contains(got, "rewrites file(s) in place") {
			t.Errorf("result should carry the rewrite note, got %q", got)
		}
		// The hook note names the format run.
		if !strings.Contains(got, "formatted") {
			t.Errorf("result should carry the hook's format note, got %q", got)
		}
		// The hook actually formatted the file on disk.
		data, err := os.ReadFile(filepath.Join(wd, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "  x :=    1") {
			t.Errorf("file should be gofmt-formatted by the hook, got %q", data)
		}
	})

	t.Run("untrusted: no hook note (the hook runs nothing)", func(t *testing.T) {
		wd := t.TempDir()
		t.Chdir(wd)
		if err := os.WriteFile(filepath.Join(wd, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Execute(context.Background(), bashCall(t, `sed -i 's/x/y/' main.go`),
			hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), false})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "rewrites file(s) in place") {
			t.Errorf("result should still carry the steering note, got %q", got)
		}
		if strings.Contains(got, "formatted") {
			t.Errorf("untrusted workspace should not run the hook, got %q", got)
		}
	})
}
