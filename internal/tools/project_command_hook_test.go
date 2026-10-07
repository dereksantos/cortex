package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// hookDeps is headlessDeps plus the optional ProjectCommands capability
// (issue #129's post-edit hook), the optional WorkspaceTrust capability
// (trust is the hook's ONLY gate), and the optional PostEditHookState
// capability (the "hook inactive" note fires once per session). A test
// session is a hookDeps value constructed WITH its own shared state
// pointer (state: &PostEditHookState{}) — Execute passes deps by value,
// and the state pointer is the identity every copy shares. A hookDeps
// without an explicit state (state == nil) has no session to announce for:
// the hook surfaces no note, byte-identical to the pre-hook result.
// trusted defaults to false — untrusted is the safe default a session
// without a trust decision must apply.
type hookDeps struct {
	headlessDeps
	cmds    projectcmd.Commands
	trusted bool
	state   *PostEditHookState
}

func (d hookDeps) ProjectCommands() projectcmd.Commands { return d.cmds }

func (d hookDeps) WorkspaceTrusted() bool { return d.trusted }

// HookState returns the session state this hookDeps value was constructed
// with, or nil when the test didn't supply one (no session, no note).
func (d hookDeps) HookState() *PostEditHookState { return d.state }

// goRepoCmds mirrors what Discover returns for a Go module: a per-file gofmt
// (format) and a PER-PACKAGE go vet (lint, {dir} — go vet on one file
// type-checks it as its own package and reports spurious undefined: errors),
// both restricted to .go files. Both run on a TRUSTED workspace: trust is
// the only gate, whatever the tool is.
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

// TestPostEditHookFormatsUnformattedFile is the core behavior: on a TRUSTED
// workspace, a write_file of an unformatted Go file lands, the hook runs
// the project's format command on it, writes back the formatted result, and
// notes it — without failing the write.
func TestPostEditHookFormatsUnformattedFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// Unformatted: space-indented body, mis-spaced assignments.
	unformatted := "package main\n\nfunc main() {\n  x :=    1\n  _ = x\n}\n"
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "main.go", "content": unformatted}), hookDeps{cmds: goRepoCmds(), trusted: true})
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

// TestPostEditHookSurfacesFailingLint pins the turn-end lint pass (issue
// #129 piece 3): lint moved off the per-edit hook — clippy/eslint are slow
// and noisy per edit — so the per-edit write_file result carries only the
// format observation, and the finding surfaces ONCE at the turn end, over
// the distinct files the turn touched (RunTurnEndLint). The write must not
// fail because of a failing lint.
func TestPostEditHookSurfacesFailingLint(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// Well-formatted (format step is a no-op) but vet-FAILING: %d with a
	// string argument.
	bad := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Printf(\"%d\\n\", \"hello\")\n}\n"
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "bad.go", "content": bad}), hookDeps{cmds: goRepoCmds(), trusted: true})
	if err != nil {
		t.Fatalf("write_file must not fail because of a failing lint, got %v", err)
	}
	// Piece 3: the per-edit hook is format-only — the write result carries
	// no lint (lint runs once at the turn end).
	if strings.Contains(out, "lint") || strings.Contains(out, "%d") {
		t.Errorf("the per-edit write result must not carry lint (lint moved to the turn end), got %q", out)
	}
	// The turn-end pass runs the lint once over the distinct touched files
	// and surfaces the finding in its receipt.
	receipt := RunTurnEndLint(context.Background(), goRepoCmds(), "", []string{"bad.go"}, true, time.Minute)
	if !strings.Contains(receipt, "for bad.go") {
		t.Errorf("turn-end lint receipt should name the touched file, got %q", receipt)
	}
	if !strings.Contains(receipt, "%d") {
		t.Errorf("turn-end lint receipt should carry the lint's finding, got %q", receipt)
	}
	// A clean turn (a vet-PASSING package) gets no receipt — no extra round.
	// (go vet {dir} type-checks the whole package, so the bad file above
	// would still taint a same-package "clean" run — the ok file gets its
	// own package dir, where the pass is genuinely clean.)
	if err := os.MkdirAll(filepath.Join(root, "cleanpkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(root, "cleanpkg", "ok.go"), "package cleanpkg\n")
	if got := RunTurnEndLint(context.Background(), goRepoCmds(), "", []string{"cleanpkg/ok.go"}, true, time.Minute); got != "" {
		t.Errorf("clean turn-end lint must be silent (no extra round), got %q", got)
	}
}

// TestPostEditHookErrorNeverBlocksTheEdit: a REAL hook error — gofmt fails
// on syntactically broken Go (exit 2) — must not fail the write. The file
// is left exactly as written and the failure is folded into the result.
func TestPostEditHookErrorNeverBlocksTheEdit(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// Broken Go: gofmt exits non-zero and leaves the file untouched.
	broken := "package main\n\nfunc broken( {\n"
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "x.go", "content": broken}), hookDeps{cmds: goRepoCmds(), trusted: true})
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

// TestPostEditHookUntrustedRunsNothing pins the trust-only gate (issue
// #129's change): on an UNTRUSTED workspace the hook runs NOTHING — no
// command, in any language, declared or discovered — and the FIRST
// write/edit of the session gets the one-line "hook inactive" note; later
// edits of the same session are silent (the note is per-session, not
// per-edit).
func TestPostEditHookUntrustedRunsNothing(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// An arbitrary (unallowlisted, whatever-language) declared command — it
	// must NOT run untrusted, whatever the tool is.
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "custom-fmt -w {file}", PerFile: true, Source: "config.json"},
	}
	// One session: the shared state pointer is the unit of "once per
	// session" (both writes below are the same session).
	deps := hookDeps{cmds: cmds, state: &PostEditHookState{}} // trusted=false

	out1, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "a.go", "content": "package main\n"}), deps)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out1, "post-edit hook inactive: this workspace isn't trusted") {
		t.Errorf("first untrusted edit must carry the one-line inactive note, got %q", out1)
	}
	if strings.Contains(out1, "formatted") || strings.Contains(out1, "custom-fmt") {
		t.Errorf("untrusted: no command may run, got %q", out1)
	}

	out2, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "b.go", "content": "package main\n"}), deps)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if strings.Contains(out2, "inactive") {
		t.Errorf("the inactive note must not repeat on later edits of the same session, got %q", out2)
	}
	if out2 != "wrote 13 bytes to b.go" {
		t.Errorf("a later untrusted edit is byte-identical to the pre-hook result, got %q", out2)
	}
}

// TestPostEditHookTrustedRunsArbitraryCommand pins the other half of the
// trust-only gate: on a TRUSTED workspace the hook runs the resolved
// format/lint commands that apply to the touched file, WHATEVER THE TOOL
// IS — declared, discovered, any language. The command is a tiny script in
// a temp dir (repo-local binaries are legal on a trusted workspace), and
// the hook must exec it and fold its observation in.
func TestPostEditHookTrustedRunsArbitraryCommand(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	// A repo-local script that "formats" by appending a marker — any
	// tool, any language: the trust decision already authorized it.
	script := filepath.Join(root, "bin", "my-fmt")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'MY-FMT RAN ON: %s\\n' \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "./bin/my-fmt {file}", PerFile: true, Source: "config.json"},
	}
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "x.go", "content": "package main\n"}), hookDeps{cmds: cmds, trusted: true})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	// {file} is the model-visible RELATIVE path (fsPath for a CWD-implicit
	// session is just the relative path — the hook anchors nothing extra):
	// the script sees "x.go", not the absolute temp path.
	if !strings.Contains(out, "MY-FMT RAN ON: x.go") {
		t.Errorf("trusted: the repo-local format command must have run and reported, got %q", out)
	}
}

// TestPostEditHookSkipsShellControlTemplate pins the template contract: a
// trusted command whose template carries shell-control characters (a ;
// chain — the class the hook has no way to express without a shell) is
// SKIPPED with a clear note; the file is left exactly as written and the
// edit still succeeds.
func TestPostEditHookSkipsShellControlTemplate(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "gofmt -w {file}; echo hacked", PerFile: true, Source: "config.json"},
	}
	before := "package main\n"
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "x.go", "content": before}), hookDeps{cmds: cmds, trusted: true})
	if err != nil {
		t.Fatalf("a hook skip must not fail the write, got %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x.go")); string(data) != before {
		t.Errorf("the file must be unchanged when the template is skipped, got %q", data)
	}
	if !strings.Contains(out, "not run") {
		t.Errorf("tool result must note the command was not run, got %q", out)
	}
	if !strings.Contains(out, "shell syntax") {
		t.Errorf("the note must say the template uses shell syntax, got %q", out)
	}
}

// TestPostEditHookFileWithSpacesIsOneArgument pins the argv contract
// end-to-end: a {file} whose path contains spaces reaches the command as
// ONE argument — the template is split into argv BEFORE substitution and
// exec'd directly (no shell), so the path travels as a single opaque
// argument and the script sees it whole. The {file} the hook passes is the
// workdir-resolved path the write targeted (fsPath) — the model-visible
// relative path for an unanchored (CWD) session, an absolute path for an
// anchored one — and the script resolves it from the command's working
// directory (the session root, CWD here), so the script must live OUTSIDE
// the temp dir (a root-relative "./bin/…" would not resolve from CWD).
func TestPostEditHookFileWithSpacesIsOneArgument(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	script := filepath.Join(t.TempDir(), "my-fmt")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The script prints its argument count and each argument: a path with
	// spaces arriving as two arguments would show up here.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"ARGS:$#\"\nfor a; do echo \"ARG:[$a]\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: script + " {file}", PerFile: true, Source: "config.json"},
	}
	dirWithSpace := filepath.Join(root, "dir with space")
	if err := os.MkdirAll(dirWithSpace, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "dir with space/my file.go", "content": "package main\n"}), hookDeps{cmds: cmds, trusted: true})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	// {file} is the workdir-resolved path the write targeted (fsPath) — for
	// this unanchored (CWD) session it is the model-visible RELATIVE path:
	// the spaced path must arrive WHOLE as the single argument, in its
	// relative spelling.
	const wantArg = "ARG:[dir with space/my file.go]"
	if !strings.Contains(out, "ARGS:1") {
		t.Errorf("the script must see exactly one argument (the path), got %q", out)
	}
	if !strings.Contains(out, wantArg) {
		t.Errorf("the script must see the spaced path WHOLE, got %q", out)
	}
}

// TestPostEditHookUntrustedNoteOnWrite is the hook-side proof of the
// untrusted path: with the session reporting its workspace as untrusted —
// the state cmd/cortex's trust resolution (CortexSession.WorkspaceTrusted)
// produces for ANY workspace not on the user-level trust list, including a
// repo whose own .cortex/config.json claims trust — the hook runs nothing
// and the first write of a session gets the one-line inactive note. The
// end-to-end coverage that the repo-claiming fixture really resolves to
// untrusted (loadMergedConfig + Config.WorkspaceTrusted) lives in
// cmd/cortex's TestWorkspaceTrustedEndToEndFromConfigs
// (project_trust_test.go); this package can't import cmd/cortex (cycle), so
// here the hook is fed the trust state that resolution produces and the
// full untrusted path (nothing run + one-line note) is verified.
func TestPostEditHookUntrustedNoteOnWrite(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "custom-fmt -w {file}", PerFile: true, Source: "config.json"},
	}
	// One session (an explicit shared state) drives the full untrusted
	// path: note on the first write, nothing run.
	deps := hookDeps{cmds: cmds, trusted: false, state: &PostEditHookState{}}
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "a.go", "content": "package main\n"}), deps)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out, "post-edit hook inactive: this workspace isn't trusted") {
		t.Errorf("the inactive note must appear on an untrusted workspace, got %q", out)
	}
	if strings.Contains(out, "formatted") {
		t.Errorf("the command must not have run on an untrusted workspace, got %q", out)
	}
}

// TestPostEditHookReportsTimeout drives the timeout path via the
// hookRunner seam: a format command that hangs is cut off by the budget, the
// hook reports the timeout with the budget's duration AND the command's
// name, and the write still succeeds with the file left as written. (No
// formatter ever hangs in practice, so the budget is unreachable
// end-to-end without this seam.)
func TestPostEditHookReportsTimeout(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	orig := hookRunner
	hookRunner = func(ctx context.Context, argv []string, dir string) (time.Duration, string, error) {
		return 10 * time.Second, "", errHookTimeout // a hung command, budget expired
	}
	t.Cleanup(func() { hookRunner = orig })

	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "y.go", "content": "package main\n"}), hookDeps{cmds: goRepoCmds(), trusted: true})
	if err != nil {
		t.Fatalf("a hook timeout must not fail the write, got %v", err)
	}
	// The note names the command that hung AND how long the budget let it
	// run — "gofmt timed out after 10s" (the default budget; the note uses
	// the elapsed the runner reported, which is the budget on a deadline).
	if !strings.Contains(out, "gofmt timed out after 10s") {
		t.Errorf("timeout note must name the command and the budget (\"gofmt timed out after 10s\"), got %q", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "y.go")); string(data) != "package main\n" {
		t.Errorf("the file must be left as written when the hook times out")
	}
}

// TestPostEditHookRunsOnEditFile: the hook fires for edit_file too, not just
// write_file — on a TRUSTED workspace.
func TestPostEditHookRunsOnEditFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	// Start clean; the edit introduces an unformatted line (spaces, not tabs).
	writeRepoFile(t, filepath.Join(root, "m.go"), "package main\n\nfunc f() {\n\treturn\n}\n")

	out, _, err := Execute(context.Background(), callArgs(t, FunctionEditFile, map[string]any{
		"path":       "m.go",
		"old_string": "func f() {",
		"new_string": "func f() {\n  return",
	}), hookDeps{cmds: goRepoCmds(), trusted: true})
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
// said about the result. Piece 3: the stub command stands in for the FORMAT
// command (the per-edit hook is format-only; lint runs once at the turn end,
// and the turn-end receipt composition is pinned by TestTurnEndLintReceipt).
func TestPostEditHookNoteComposesWithEditWarnings(t *testing.T) {
	orig := hookRunner
	hookRunner = func(ctx context.Context, argv []string, dir string) (time.Duration, string, error) {
		return 50 * time.Millisecond, "HOOK-OUTPUT-MARKER", nil
	}
	t.Cleanup(func() { hookRunner = orig })
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "fmt-marker {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
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
			out, _, err := Execute(context.Background(), tc.call, hookDeps{cmds: cmds, trusted: true})
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
	// A markdown file, not a .go file: the #224 write-sanity note is the one
	// other post-write observation that can append, and it is Go-only —
	// using it here would blur this test's claim (no HOOK note) with the
	// sanity pass's own contract (writesanity_test.go).
	content := "# heading\n\nsome text\n"

	outHeadless, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "a.md", "content": content}), headlessDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outHeadless, "formatted") || strings.Contains(outHeadless, "lint") {
		t.Errorf("headless write_file must be hook-free, got %q", outHeadless)
	}

	outEmpty, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "b.md", "content": content}), hookDeps{cmds: projectcmd.Commands{}, trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	// Both results must be the bare "wrote N bytes to PATH" with NO hook note
	// appended — an empty Commands adds nothing.
	if outHeadless != "wrote 21 bytes to a.md" {
		t.Errorf("headless write_file result changed, got %q", outHeadless)
	}
	if outEmpty != "wrote 21 bytes to b.md" {
		t.Errorf("empty-Commands write_file should add no note, got %q", outEmpty)
	}
}

// TestHookGateTrustGate is the direct check of the trust-only gate (the
// class, not an instance): the hook's gate decides per command —
//   - UNTRUSTED: nothing runs, whatever the tool (gofmt, cargo clippy,
//     npx eslint, npm run … all refused by trust, not by a tool list);
//   - TRUSTED: whatever the tool, the command runs — declared, discovered,
//     any language (repo-local binaries are legal: the template may name a
//     path-qualified binary on a trusted workspace);
//   - TRUSTED with shell-control in the template: skipped with a clear
//     note (no shell to express it);
//   - a per-file format on a path the template can't run is refused by the
//     gate, not by trust.
func TestHookGateTrustGate(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		trusted  bool
		wantOK   bool
		wantNote string
	}{
		// Untrusted: nothing runs, whatever the tool.
		{name: "gofmt-untrusted-refused", cmd: "gofmt -w {file}", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},
		{name: "clippy-untrusted-refused", cmd: "cargo clippy {file}", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},
		{name: "eslint-untrusted-refused", cmd: "eslint {file}", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},
		{name: "npx-eslint-untrusted-refused", cmd: "npx eslint {file}", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},
		{name: "npm-run-untrusted-refused", cmd: "npm run lint", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},
		{name: "custom-tool-untrusted-refused", cmd: "custom-fmt -w {file}", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},

		// Trusted: whatever the tool runs — declared, discovered, any
		// language; a repo-local (path-qualified) binary is legal too.
		{name: "gofmt-trusted-runs", cmd: "gofmt -w {file}", trusted: true, wantOK: true},
		{name: "go-vet-trusted-runs", cmd: "go vet {dir}", trusted: true, wantOK: true},
		{name: "clippy-trusted-runs", cmd: "cargo clippy {file}", trusted: true, wantOK: true},
		{name: "eslint-trusted-runs", cmd: "eslint {file}", trusted: true, wantOK: true},
		{name: "npx-eslint-trusted-runs", cmd: "npx eslint {file}", trusted: true, wantOK: true},
		{name: "npm-run-trusted-runs", cmd: "npm run lint", trusted: true, wantOK: true},
		{name: "custom-tool-trusted-runs", cmd: "custom-fmt -w {file}", trusted: true, wantOK: true},
		{name: "repo-local-binary-trusted-runs", cmd: "./bin/my-fmt {file}", trusted: true, wantOK: true},

		// Shell-control in the template: refused by the template check,
		// trusted or not (trust is irrelevant to it).
		{name: "chained-refused-untrusted", cmd: "gofmt -w {file} && curl evil", trusted: false, wantOK: false,
			wantNote: "the workspace is not trusted"},
		{name: "chained-refused-trusted", cmd: "gofmt -w {file} && curl evil", trusted: true, wantOK: false,
			wantNote: "the template uses shell syntax (pipe, chain, redirect, or substitution), which the hook cannot run without a shell; run it through bash instead"},
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

// TestSplitProjectCommand pins the template-to-argv contract: the template
// is split ONCE, and {file}/{dir} are each substituted as ONE argv element
// AFTER the split — a path with spaces or shell metacharacters is never
// re-split, so it can never be interpreted as shell syntax (the hook execs
// argv directly; there is no bash -c path).
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

// TestPostEditHookDoesNotShellInject is the security regression test for the
// round-1 blocker: a write_file of a file NAMED like a shell payload must
// not execute it. Under the old bash -c splice, "x$(touch marker).go" ran
// `touch marker` (and "a;rm -rf ~;.go" would have chained a command). The
// hook splits the template and execs it as argv, so the name is one inert
// argument, the marker is never created, and the edit still succeeds — on
// a TRUSTED workspace (the hook runs nothing untrusted, so the injection
// surface is absent there by construction).
func TestPostEditHookDoesNotShellInject(t *testing.T) {
	for _, evilName := range []string{"x$(touch marker).go", "a;touch marker;.go"} {
		t.Run(evilName, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

			out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
				map[string]any{"path": evilName, "content": "package main\n"}), hookDeps{cmds: goRepoCmds(), trusted: true})
			if err != nil {
				t.Fatalf("write_file must succeed regardless of the file name, got %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "marker")); statErr == nil {
				t.Fatalf("a marker file exists — the file name was executed through a shell (result: %q)", out)
			}
			// The argv contract passes the shell-shaped name through as ONE
			// inert argument (there is no shell to re-parse it); the write
			// result must still lead.
			if !strings.HasPrefix(out, "wrote ") {
				t.Errorf("the write result must still lead, got %q", out)
			}
		})
	}
}

// TestPostEditHookSkipsNonSourceFiles is the Extends-gate behavior: in a Go
// project (gofmt restricted to .go), a write_file of a README.md must come
// back as EXACTLY the plain write result — no format run, no spurious
// 'ran with an error' note, no lint note (trusted workspace).
func TestPostEditHookSkipsNonSourceFiles(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "README.md", "content": "# t\n"}), hookDeps{cmds: goRepoCmds(), trusted: true})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if out != "wrote 4 bytes to README.md" {
		t.Errorf("non-source write in a Go project must be note-free, got %q", out)
	}
}

// TestPostEditHookWholePackageVetIsSilent pins the {dir} substitution against
// the real toolchain (trusted workspace): two files in one package where
// a.go CALLS a function defined in b.go. A per-file `go vet a.go` would
// type-check a.go as its own package and report a spurious 'undefined:
// helper' on every edit; the whole-package `go vet ./sub` type-checks both
// files together and is clean — so the hook must run it and stay silent
// (no note at all).
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
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "sub/a.go", "content": "package sub\n\nfunc main() {\n  _ = helper()\n}\n"}), hookDeps{cmds: goRepoCmds(), trusted: true})
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

// TestPostEditHookInactiveNoteIsOncePerSession pins the per-session state
// end-to-end through the REAL write_file path (Execute →
// runProjectCommandHook → firstProjectCommandRunOf): a session (one hookDeps
// with its own PostEditHookState) on an untrusted workspace announces the
// "hook inactive" note on its FIRST write/edit and stays silent on later
// ones — the state lives on the session, not on the tool call.
func TestPostEditHookInactiveNoteIsOncePerSession(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")

	// One session: the explicit shared state pointer is the session's unit
	// of "once per session" (value copies of deps share the pointer).
	deps := hookDeps{cmds: goRepoCmds(), state: &PostEditHookState{}} // trusted=false

	out1, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "a.go", "content": "package main\n"}), deps)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out1, "post-edit hook inactive: this workspace isn't trusted") {
		t.Errorf("the first untrusted write of a session must carry the inactive note, got %q", out1)
	}

	out2, _, err := Execute(context.Background(), callArgs(t, FunctionEditFile, map[string]any{
		"path":       "a.go",
		"old_string": "package main",
		"new_string": "package main // edited",
	}), deps)
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if strings.Contains(out2, "inactive") {
		t.Errorf("the inactive note must NOT repeat on a later edit of the same session, got %q", out2)
	}

	// A NEW session (its own state pointer) gets its own flag: the note
	// fires again.
	fresh := hookDeps{cmds: goRepoCmds(), state: &PostEditHookState{}}
	out3, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "c.go", "content": "package main\n"}), fresh)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out3, "post-edit hook inactive: this workspace isn't trusted") {
		t.Errorf("a new session's first untrusted write must carry the inactive note again, got %q", out3)
	}
}
