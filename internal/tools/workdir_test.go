package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wdDeps is headlessDeps plus the optional Workdirer capability: a session
// hosted by a long-lived process (cortex serve, a loop firing) whose
// workspace root is not the process CWD.
type wdDeps struct {
	headlessDeps
	wd string
}

func (d wdDeps) Workdir() string { return d.wd }

// GateShell permits everything: these tests exercise path resolution, not
// the risk gate (which headlessDeps fails closed).
func (d wdDeps) GateShell(ctx context.Context, command string) (string, bool) {
	return "", true
}

func callArgs(t *testing.T, fn string, m map[string]any) ToolCall {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return ToolCall{Function: FunctionCall{Name: fn, Arguments: string(b)}}
}

func TestWriteFileResolvesAgainstWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	t.Chdir(cwd)

	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "note.txt", "content": "hello"}), wdDeps{wd: wd})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, "note.txt")); err != nil {
		t.Errorf("file should land under the workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "note.txt")); err == nil {
		t.Errorf("file must not land in the process CWD")
	}
	if strings.Contains(out, wd) {
		t.Errorf("result should keep the relative path, got %q", out)
	}
}

func TestEditFileResolvesAgainstWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	t.Chdir(cwd)
	target := filepath.Join(wd, "a.txt")
	if err := os.WriteFile(target, []byte("old text"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Execute(context.Background(), callArgs(t, FunctionEditFile,
		map[string]any{"path": "a.txt", "old_string": "old", "new_string": "new"}), wdDeps{wd: wd}); err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "new text" {
		t.Errorf("edit should apply in the workdir file, got %q", got)
	}
}

func TestReadFileResolvesAgainstWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(filepath.Join(wd, "r.txt"), []byte("workdir content"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Execute(context.Background(), callArgs(t, FunctionReadFile,
		map[string]any{"path": "r.txt"}), wdDeps{wd: wd})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out != "workdir content" {
		t.Errorf("read should resolve against the workdir, got %q", out)
	}
}

func TestGrepDefaultPathResolvesAgainstWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(filepath.Join(wd, "g.txt"), []byte("needle-xyzzy here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Execute(context.Background(), callArgs(t, FunctionGrep,
		map[string]any{"pattern": "needle-xyzzy"}), wdDeps{wd: wd})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "needle-xyzzy") {
		t.Errorf("grep with default path should search the workdir, got %q", out)
	}
}

func TestOutlineResolvesAgainstWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	t.Chdir(cwd)
	src := "package o\n\nfunc HeadingFromWorkdir() {}\n\nfunc SecondDecl() {}\n"
	if err := os.WriteFile(filepath.Join(wd, "o.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Execute(context.Background(), callArgs(t, FunctionOutline,
		map[string]any{"path": "o.go"}), wdDeps{wd: wd})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	if !strings.Contains(out, "HeadingFromWorkdir") {
		t.Errorf("outline should resolve against the workdir, got %q", out)
	}
}

func TestBashRunsInWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	t.Chdir(cwd)

	out, err := Execute(context.Background(), callArgs(t, FunctionBash,
		map[string]any{"command": "pwd"}), wdDeps{wd: wd})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	wantReal, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, wantReal) && !strings.Contains(out, wd) {
		t.Errorf("bash should run in the workdir, pwd = %q, want %q", strings.TrimSpace(out), wd)
	}
}

func TestNoWorkdirKeepsCWDResolution(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	if _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "plain.txt", "content": "x"}), wdDeps{wd: ""}); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "plain.txt")); err != nil {
		t.Errorf("empty workdir must preserve CWD-relative behavior: %v", err)
	}
}

func TestAbsolutePathIgnoresWorkdir(t *testing.T) {
	cwd := t.TempDir()
	wd := t.TempDir()
	other := t.TempDir()
	t.Chdir(cwd)
	abs := filepath.Join(other, "abs.txt")

	if _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": abs, "content": "x"}), wdDeps{wd: wd}); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("absolute paths must pass through untouched: %v", err)
	}
}
