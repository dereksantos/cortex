package main

// Step 1 of issue #219: the turn receipt measures what the turn actually
// left in the workspace. This file pins the MEASUREMENT (the receipt's three
// facts — files changed, the model's own verification runs and their real
// exit codes, the files the format hook could not verify) and its SURFACE
// (TurnResult.Receipt on a tools-ran turn, empty otherwise), plus the
// hook-side regression: a per-call `hook: "skip"` on a session that
// implements FormatHookNoter suppresses the formatter.
//
// The verification fact is the model's own test/build runs (the bash
// recorder), NOT a harness run of the project's commands — the harness runs
// no project commands for the receipt (trust is the only gate for executing
// project-declared commands, and a measurement path that ran them itself
// would bypass that gate). The exit code comes from the bash tool's own
// observation (the "[exit error: exit status N]" marker, parsed — never
// inferred from a shadowed tool error), and a command the shell gate
// REFUSED or the user DECLINED never ran: it records (not run: …) through
// the structured ShellGateOutcome the dispatcher observes, never an exit
// code.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
)

// receiptSession builds a session over dir (a fresh temp dir by default, or
// a git repository when initGit) whose workdir, transcript, and project
// commands are wired so the receipt's measurements resolve. The workspace is
// EXPLICIT (the shape serve, --root, and tests all use): an explicit-root
// workspace is what the receipt's git read must measure. trusted controls
// the format hook's trust gate via the USER-level config
// (corpusTrustUserConfig — the authoritative trust source
// Config.WorkspaceTrusted reads); the hook ceiling is installed process-wide
// exactly as NewCortexSession does (SetHookCeiling(all)), and the hook state
// is session-allocated. The test/build commands are DISCOVERED `failcheck` /
// `passcheck` binaries with real exits, so a turn's run of them records the
// REAL exit the bash tool observed. The format command is a per-file stub
// ({file} template, .go files) that a test-set hookRunner answers without
// exec'ing a formatter (the hook runs it through the internal tools'
// hookRunner seam).
func receiptSession(t *testing.T, dir string, initGit, trusted bool) *CortexSession {
	t.Helper()
	if initGit {
		git := exec.Command("git", "init", "-q")
		git.Dir = dir
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
		gitc := func(args ...string) {
			t.Helper()
			c := exec.Command("git", args...)
			c.Dir = dir
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		gitc("config", "user.email", "t@t")
		gitc("config", "user.name", "t")
	}
	corpusTrustUserConfig(t, dir, trusted)
	cs := newMemSession(t)
	// The session's resolved config: WorkspaceTrusted() reads it (nil →
	// untrusted, so the hook would run nothing); the trust LIST itself is
	// the user-level config corpusTrustUserConfig just wrote (Config.
	// TrustedList reads it directly — the operator's persisted decision).
	cs.Config = &Config{}
	cs.workspace = &Workspace{Root: dir, Explicit: true}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	t.Cleanup(func() { cs.Close() })
	cs.hookState = &tools.PostEditHookState{}
	tools.SetHookCeiling(tools.HookModeAll)
	t.Cleanup(func() { tools.SetHookCeiling(tools.HookModeAll) })
	// The discovered test/build commands are real binaries in a real PATH
	// dir: failcheck exits 1, passcheck exits 0. A turn running `failcheck`
	// therefore executes a REAL shell script through the real bash tool —
	// the receipt's exit code is the shell's observed exit, never a stubbed
	// marker. The recorder sees a one-field command, so receiptBashRole's
	// one-field-exact rule records it.
	//
	// The bin/ dir is gitignored: the receipt's files-changed fact appends
	// every UNTRACKED file in the workspace, and without this ignore the
	// test's own two scripts would surface in every case's receipt (the
	// fact is the workspace's on-disk state at turn end — the harness
	// cannot attribute which untracked file this turn created).
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	writeCmdScript(t, bin, "failcheck", "exit 1")
	writeCmdScript(t, bin, "passcheck", "exit 0")
	// The real bash tool spawns its process with the test's PATH: the
	// discovered checks are real binaries in a real PATH dir, so a turn
	// running `failcheck` executes a REAL shell script — the receipt's exit
	// code is the shell's observed exit, never a stubbed marker. Prepending
	// bin through t.Setenv scopes it to this test; the production bash
	// path has no test-only env seam.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The real gate must let the checks through: a bare custom binary is
	// not on the safe path, so the tier-3 classifier decides. The seam
	// classifies both checks Safe (a test command the gate should run);
	// TestTurnReceiptRefusedOrRefusedNeverExit0 re-stubs it per case.
	cs.classifyShell = func(_ context.Context, command, _ string) (shellrisk.Level, string, error) {
		if f := strings.Fields(command); len(f) > 0 && (f[0] == "failcheck" || f[0] == "passcheck") {
			return shellrisk.Safe, "test: check command", nil
		}
		return shellrisk.Risky, "test: unclassified command", nil
	}
	cs.projectCommands = projectcmd.Commands{
		Test:   projectcmd.Command{Cmd: "failcheck", PerFile: false, Source: "go.mod"},
		Build:  projectcmd.Command{Cmd: "passcheck", PerFile: false, Source: "go.mod"},
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
	}
	return cs
}

// writeCmdScript writes an executable named name into bin that runs body
// through the system shell — the discovered test/build commands' real
// binary. The body is what the model's `test`/`build` run actually does
// (failcheck: exit 1, passcheck: exit 0), so the receipt's exit code is a
// REAL process exit, not a stubbed string.
func writeCmdScript(t *testing.T, bin, name, body string) string {
	t.Helper()
	path := filepath.Join(bin, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// receiptBashTurn drives one real turn on cs: a scripted sender that issues
// one round of tool calls (bashCall and/or writeFileCall), then a final
// answer. NO dispatcher override — the REAL production wiring
// (coderDispatcher, loop.go) runs: it records the bash command
// (receiptBash), executes it through the real bash tool (the shell gate
// classifies it for real, the process runs for real), and resolves the
// outcome through the structured ShellGateOutcome (receiptBashOutcome) —
// the exact lines where the round-1 shadowed-err blocker lived. The
// write_file call (when present) runs through the real tool path (the
// format hook, the turn-end lint arm), so the unformatted fact is measured
// the way production measures it.
func receiptBashTurn(t *testing.T, cs *CortexSession, script []*AgentResponse) TurnResult {
	t.Helper()
	cs.senderOverride = multiTurnScriptedSender(script)
	res, err := cs.Turn(context.Background(), "verify the build")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	return res
}

// TestTurnReceipt is the issue #219 acceptance table: (a) an edit plus a
// FAILING test run (the discovered test command exits 1 — a failing
// verification must record the real non-zero exit, and the formatter-rewrote
// the file is NOT unformatted), (b) a clean passing build run (exit 0,
// recorded), (c) a turn that ran no verification (no tools at all — no
// receipt), (d) a non-git workspace (no files-changed section), (e) an
// untrusted workspace (the hook runs nothing and records nothing), (f) a
// formatter that fails (the file IS unformatted — the hook's structured
// outcome HookOutcomeFailed). Each case is a t.Run subtest; the git cases
// use a t.TempDir git repo, the non-git case a plain temp dir.
func TestTurnReceipt(t *testing.T) {
	// The default stub hookRunner for the CLEAN-RUN shape: a format run
	// that REWRITES the file (the "the formatter fixed it" shape — the
	// file's bytes differ after the run). The failing-formatter case below
	// swaps it for one that returns an error.
	rewritingHook := func(_ context.Context, argv []string, _ string) (time.Duration, string, error) {
		p := argv[len(argv)-1]
		b, err := os.ReadFile(p)
		if err != nil {
			return time.Millisecond, "", err
		}
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			return time.Millisecond, "", err
		}
		return time.Millisecond, "", nil
	}
	failingHook := func(_ context.Context, _ []string, _ string) (time.Duration, string, error) {
		return time.Millisecond, "", fmt.Errorf("formatter exploded")
	}

	cases := []struct {
		name            string
		git             bool
		trusted         bool
		script          []*AgentResponse
		wantReceipt     string // substring the receipt must contain
		wantNoReceipt   bool
		wantAbsent      []string // substrings the receipt must NOT contain
		wantUnformatted string
		hook            func(context.Context, []string, string) (time.Duration, string, error)
	}{
		{
			// The discovered test command (failcheck) exits 1: the receipt
			// records the REAL non-zero exit the bash tool observed, and the
			// file the formatter REWROTE is not unformatted (it is clean now —
			// the hook fixed it). The bash tool's result is real: no
			// "[exit error: …]" marker is ever stubbed in.
			name:    "edit plus failing test run",
			git:     true,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{
					bashCall("t1", "failcheck"),
					writeFileCall("w1", "main.go", "package main\n\nfunc main() {}\n"),
				}),
				respWithAnswer("verified the build"),
			},
			wantReceipt:     "test: failcheck (exit 1",
			wantUnformatted: "",
			wantAbsent:      []string{"unformatted:"},
			hook:            rewritingHook,
		},
		{
			// A run that actually passed (the discovered build command
			// passcheck exits 0) records exit 0 — the only legitimate exit 0
			// in the receipt: the process ran and exited 0.
			name:    "passing build run records exit 0",
			git:     true,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{bashCall("t1", "passcheck")}),
				respWithAnswer("done"),
			},
			wantReceipt: "build: passcheck (exit 0",
			hook:        rewritingHook,
		},
		{
			name:          "no tools means no verification and no receipt",
			git:           true,
			trusted:       true,
			script:        []*AgentResponse{respWithAnswer("nothing to run")},
			wantNoReceipt: true,
			hook:          rewritingHook,
		},
		{
			name:    "non-git workspace has no files-changed section",
			git:     false,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{bashCall("t1", "passcheck")}),
				respWithAnswer("done"),
			},
			wantReceipt: "build: passcheck (exit 0",
			wantAbsent:  []string{"files changed:"},
			hook:        rewritingHook,
		},
		{
			// The hook trust gate (trust is the ONLY gate): on an untrusted
			// workspace the format hook runs NOTHING and records NOTHING —
			// the one-time "hook inactive" note is not an unformatted fact.
			name:    "untrusted workspace runs and records nothing",
			git:     true,
			trusted: false,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{writeFileCall("w1", "main.go", "package main\n\nfunc main() {}\n")}),
				respWithAnswer("done"),
			},
			wantReceipt:     "files changed:",
			wantUnformatted: "",
			wantAbsent:      []string{"unformatted:"},
			hook:            rewritingHook,
		},
		{
			// The formatter FAILED (the hook's structured outcome
			// HookOutcomeFailed): the file is left as written, its
			// cleanliness unverified — the receipt names it.
			name:    "formatter failure records the file as unformatted",
			git:     true,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{writeFileCall("w1", "main.go", "package main\n\nfunc main() {}\n")}),
				respWithAnswer("done"),
			},
			wantReceipt:     "files changed:",
			wantUnformatted: "main.go",
			hook:            failingHook,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := tools.SetHookRunner(tc.hook)
			t.Cleanup(func() { tools.SetHookRunner(prev) })
			dir := t.TempDir()
			cs := receiptSession(t, dir, tc.git, tc.trusted)
			res := receiptBashTurn(t, cs, tc.script)
			if tc.wantNoReceipt {
				if res.Receipt != "" {
					t.Fatalf("TurnResult.Receipt = %q, want empty (no tools ran — nothing measured)", res.Receipt)
				}
				return
			}
			if !strings.Contains(res.Receipt, tc.wantReceipt) {
				t.Fatalf("TurnResult.Receipt = %q, want it to contain %q", res.Receipt, tc.wantReceipt)
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(res.Receipt, absent) {
					t.Errorf("TurnResult.Receipt = %q, want it to NOT contain %q", res.Receipt, absent)
				}
			}
			if tc.git && !strings.Contains(res.Receipt, "files changed:") {
				t.Errorf("TurnResult.Receipt = %q, want the files-changed section (git workspace)", res.Receipt)
			}
			if !tc.git && strings.Contains(res.Receipt, "files changed:") {
				t.Errorf("TurnResult.Receipt = %q, want NO files-changed section (non-git workspace)", res.Receipt)
			}
			if tc.wantUnformatted != "" && !strings.Contains(res.Receipt, "unformatted: "+tc.wantUnformatted) {
				t.Errorf("TurnResult.Receipt = %q, want the unformatted fact naming %q", res.Receipt, tc.wantUnformatted)
			}
		})
	}
}

// TestTurnReceiptRefusedOrRefusedNeverExit0 pins the reviewer's blocker: a
// bash run the shell gate refuses (a Blocked verdict — the deny floor) or
// the user declines (a Risky verdict with no approver) NEVER RAN, so the
// receipt must record (not run: …) for it — never an exit code, least of all
// exit 0. The real production wiring runs: the real dispatcher (no
// coderDispatcherOverride), the real bash tool, the real gate — only the
// classifier is stubbed (the session's classifyShell seam) so the verdict
// is deterministic without a live judge.
func TestTurnReceiptRefusedOrRefusedNeverExit0(t *testing.T) {
	cases := []struct {
		name       string
		level      shellrisk.Level
		wantAbsent string
	}{
		{
			name:       "a Blocked test command is refused, never an exit",
			level:      shellrisk.Blocked,
			wantAbsent: "exit 0",
		},
		{
			name:       "a declined Risky test command never ran, never an exit",
			level:      shellrisk.Risky,
			wantAbsent: "exit 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cs := receiptSession(t, dir, true, true)
			cs.classifyShell = func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
				return tc.level, "test: verdict", nil
			}
			res := receiptBashTurn(t, cs, []*AgentResponse{
				respWithCalls([]ToolCall{bashCall("t1", "failcheck")}),
				respWithAnswer("the gate said no"),
			})
			// The verification run IS recorded (the model intended it) —
			// as a not-run line, not an exit code.
			if !strings.Contains(res.Receipt, "test: failcheck (not run:") {
				t.Fatalf("TurnResult.Receipt = %q, want the run recorded as (not run: …)", res.Receipt)
			}
			if strings.Contains(res.Receipt, tc.wantAbsent) {
				t.Fatalf("TurnResult.Receipt = %q, must not carry %q — a refused or declined command never ran", res.Receipt, tc.wantAbsent)
			}
		})
	}
}

// TestReceiptBashRole pins the recorder's prefix matching: a command is a
// recognized run of the project's own test/build command when it shares at
// least the first TWO fields of the discovered command and nothing with
// shell-control syntax follows — never a bare toolchain word (`go`), never a
// pipeline or chain whose exit status is not the check's, and never in the
// other direction (discovery with more fields than the model ran — that is a
// different, broader command, not the check). A foreign command (go vet on a
// project that declared go test) is not this project's verification.
// A discovered command of ONE field (a bare `make`, a single test binary)
// matches ONLY the exact command — it has no prefix to share, so anything
// else is a different command line.
func TestReceiptBashRole(t *testing.T) {
	cs := &CortexSession{projectCommands: projectcmd.Commands{
		Test:  projectcmd.Command{Cmd: "go test ./...", Source: "go.mod"},
		Build: projectcmd.Command{Cmd: "go build ./...", Source: "go.mod"},
	}}
	cases := []struct {
		command string
		role    string
		ok      bool
	}{
		{"go test ./...", "test", true},
		{"go test ./... -v", "test", true}, // the model ran the check with extra flags
		{"go build ./...", "build", true},
		{"go", "", false},                      // bare toolchain — at least two fields
		{"go test", "", false},                 // one shared field is not the check
		{"go test ./... | tail -5", "", false}, // a pipeline: tail's exit is not the test's
		{"go test ./... || true", "", false},   // a chain that never fails is not the check
		{"go test ./... ; echo done", "", false},
		{"go test ./... > out.txt", "", false},
		{"go vet ./...", "", false}, // foreign command, same toolchain
		{"rm -rf /", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		role, ok := cs.receiptBashRole(tc.command)
		if ok != tc.ok || string(role) != tc.role {
			t.Errorf("receiptBashRole(%q) = (%q, %v), want (%q, %v)", tc.command, role, ok, tc.role, tc.ok)
		}
	}
	// The one-field discovery shape: a bare command matches ONLY exactly —
	// the single test/build binary the tests discover as their project
	// command (failcheck/passcheck) and a target-less `make`.
	single := &CortexSession{projectCommands: projectcmd.Commands{
		Test:  projectcmd.Command{Cmd: "failcheck", Source: "go.mod"},
		Build: projectcmd.Command{Cmd: "passcheck", Source: "go.mod"},
	}}
	singleCases := []struct {
		command string
		role    string
		ok      bool
	}{
		{"failcheck", "test", true},           // the exact one-field check
		{"passcheck", "build", true},          // the exact one-field build
		{"failcheck -v", "", false},           // extra fields: a different command line
		{"failcheck | tail", "", false},       // a pipeline: not the check
		{"failcheck && passcheck", "", false}, // a chain: not the check
	}
	for _, tc := range singleCases {
		role, ok := single.receiptBashRole(tc.command)
		if ok != tc.ok || string(role) != tc.role {
			t.Errorf("receiptBashRole(%q) = (%q, %v), want (%q, %v)", tc.command, role, ok, tc.role, tc.ok)
		}
	}
}

// TestReceiptExitCodeOf pins the exit-code parse: the bash tool reports a
// non-zero exit as a trailing "[exit error: <error>]" line, where <error> is
// the run error's Error() text. exec.ExitError renders as "exit status N"
// (the number is the process's real exit code), but a run KILLED BY A SIGNAL
// renders as "signal: killed" and carries no number. A marker that names no
// exit status — a signal-killed run, or a bare marker — is a real failure
// reported as exit 1: the receipt's job is to never report a failed run as
// exit 0. The dispatcher passes only the marker's TAIL (the text after
// "[exit error: "), so a marker-less input resolves to the absence of an
// observed failure.
func TestReceiptExitCodeOf(t *testing.T) {
	cases := []struct {
		in   string
		code int
		ok   bool
	}{
		{"", 0, false},              // marker-less arm: no exit status observed
		{"exit status 1]", 1, true}, // the real process exit code
		{"exit status 3]", 3, true},
		{"exit status ]", 1, true}, // no number: a failure, not a clean exit
		{"exit status]", 1, true},
		{"signal: killed]", 1, true}, // signal-killed: no number, a failure
	}
	for _, tc := range cases {
		code, ok := receiptExitCodeOf(tc.in)
		if ok != tc.ok || code != tc.code {
			t.Errorf("receiptExitCodeOf(%q) = (%d, %v), want (%d, %v)", tc.in, code, ok, tc.code, tc.ok)
		}
	}
}

// TestBoundStatLines pins the files-changed bound: a clean tree (empty block)
// yields nothing, a block at or under the cap is kept whole, a larger block
// keeps its first (cap-1) per-file lines plus its summary tail.
func TestBoundStatLines(t *testing.T) {
	if got := boundStatLines(nil); got != nil {
		t.Fatalf("boundStatLines(nil) = %v, want nil", got)
	}
	small := []string{" a | 1 +", " 2 files changed, 1 insertion(+), 1 deletion(-)"}
	if got := boundStatLines(small); len(got) != 2 {
		t.Errorf("boundStatLines(2 lines) = %v, want all 2", got)
	}
	var big []string
	for i := 0; i < 12; i++ {
		big = append(big, fmt.Sprintf(" file%02d.go | 3 +++", i))
	}
	big = append(big, "12 files changed, 36 insertions(+)")
	got := boundStatLines(big)
	if len(got) != receiptMaxStatLines {
		t.Fatalf("boundStatLines(13 lines) = %d lines, want %d", len(got), receiptMaxStatLines)
	}
	// git diff --stat prefixes every line with a space — the expected
	// literals carry it.
	if got[0] != " file00.go | 3 +++" || !strings.Contains(got[len(got)-1], "12 files changed") {
		t.Errorf("boundStatLines kept the wrong head/tail: %v", got)
	}
}

// TestRenderReceipt pins render()'s fixed form and its degradation: a fact
// that measured nothing is omitted, and a receipt with no facts renders "".
func TestRenderReceipt(t *testing.T) {
	if got := (turnReceipt{}).render(); got != "" {
		t.Fatalf("empty receipt renders %q, want \"\"", got)
	}
	got := turnReceipt{
		filesChanged: []string{" a | 1 +", "2 files changed"},
		gitWorkspace: true,
		verification: []receiptVerification{
			{role: "test", command: "go test ./...", exitCode: 1, elapsed: 420 * time.Millisecond},
			{role: "test", command: "go test ./...", notRun: "refused: the shell risk gate blocked this command"},
		},
		unformatted: []string{"main.go"},
	}.render()
	for _, want := range []string{"files changed:", "  a | 1 +", "verification:", "  test: go test ./... (exit 1, 0.42s)", "  test: go test ./... (refused: the shell risk gate blocked this command)", "unformatted: main.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("render() =\n%s\nwant it to contain %q", got, want)
		}
	}
	// A turn that measured only verification still renders a bounded block.
	many := turnReceipt{verification: make([]receiptVerification, 0, 9)}
	for i := 0; i < 9; i++ {
		many.verification = append(many.verification, receiptVerification{role: "test", command: "go test ./...", exitCode: 0, elapsed: time.Second})
	}
	if got := many.render(); !strings.Contains(got, "… 3 more") {
		t.Errorf("render() of 9 verification runs =\n%s\nwant the \"… 3 more\" summary", got)
	}
}

// TestFormatHookSkipSuppressedOnNoterSession is the hook-side regression the
// reviewer flagged: on a session that implements FormatHookNoter (the main
// CortexSession), a per-call `hook: "skip"` must lower the effective hook
// mode to off — the formatter must NOT run. Without the hookSkip fold the
// session's effective mode (all) would apply and the formatter would run
// anyway, silently overriding the documented opt-out.
func TestFormatHookSkipSuppressedOnNoterSession(t *testing.T) {
	dir := t.TempDir()
	cs := receiptSession(t, dir, true, true)
	// A per-file format command over .go files so the hook applies. (The
	// PerFile flag is the hook's own {file} detection: roleApplicable runs
	// the format role only on a per-file command — a whole-project format
	// has no argument to substitute for the one file just touched.)
	cs.projectCommands = projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "fmt-marker {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
	}
	ran := 0
	prev := tools.SetHookRunner(func(_ context.Context, _ []string, _ string) (time.Duration, string, error) {
		ran++
		return time.Millisecond, "", nil
	})
	t.Cleanup(func() { tools.SetHookRunner(prev) })
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// hookSkip=false: the formatter runs (the mode is all, the workspace is
	// trusted, the file is a .go).
	if note := cs.FormatHook(context.Background(), filepath.Join(dir, "main.go"), false); note == "" && ran == 0 {
		t.Fatal("FormatHook(hookSkip=false) neither ran the formatter nor noted it — want a run")
	}
	if ran == 0 {
		t.Fatalf("FormatHook(hookSkip=false) did not run the formatter (mode all, trusted, .go file)")
	}
	// hookSkip=true: the formatter must NOT run — the skip lowers the mode
	// to off. (The run count must not grow.)
	before := ran
	cs.FormatHook(context.Background(), filepath.Join(dir, "main.go"), true)
	if ran != before {
		t.Fatalf("FormatHook(hookSkip=true) ran the formatter %d time(s) — a `hook: \"skip\"` on a FormatHookNoter session must suppress it", ran-before)
	}
}

// TestTurnReceiptTranscriptNote pins the transcript surface: a tools-ran
// turn with a receipt writes a kindNote transcript entry carrying it, and the
// stored assistant message stays the model's verbatim reply (the receipt is
// a harness note, never folded into the reply).
func TestTurnReceiptTranscriptNote(t *testing.T) {
	dir := t.TempDir()
	cs := receiptSession(t, dir, true, true)
	res := receiptBashTurn(t, cs, []*AgentResponse{
		respWithCalls([]ToolCall{bashCall("t1", "passcheck")}),
		respWithAnswer("verified"),
	})
	if res.Receipt == "" {
		t.Fatal("TurnResult.Receipt empty for a tools-ran turn with a verification run")
	}
	if cs.transcript == nil {
		t.Fatal("transcript not started")
	}
	// Read the raw JSONL transcript from disk.
	path := filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	var entries []sessionEntry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad transcript line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		t.Fatal("no transcript entries")
	}
	var sawNote, sawVerbatim bool
	for _, e := range entries {
		// e.Message is an embedded transcript.Entry (not an exported
		// field): the QF1008-clean selector is e.Content / e.Role.
		if e.Kind == kindNote && strings.Contains(e.Content, "turn receipt:") && strings.Contains(e.Content, res.Receipt) {
			sawNote = true
		}
		if e.Kind == kindMessage && e.Role == "assistant" && e.Content == "verified" {
			sawVerbatim = true
		}
	}
	if !sawNote {
		t.Errorf("no kindNote transcript entry carrying the turn receipt; entries: %+v", entries)
	}
	if !sawVerbatim {
		t.Errorf("no verbatim assistant reply \"verified\" in the transcript; entries: %+v", entries)
	}
}

// TestTurnReceiptRealDispatcher covers the PRODUCTION wiring end to end:
// a REAL turn (scripted sender, no coderDispatcherOverride) in which the
// model issues the project's OWN test command and the real
// coderDispatcher's bash arm (loop.go — where the round-1 shadowed-err
// blocker lived) records it: receiptBash before the call, the real bash
// tool's real process result, and receiptBashOutcome with the gate's own
// structured outcome. Only the model and the classifier are stubbed.
func TestTurnReceiptRealDispatcher(t *testing.T) {
	t.Run("real wiring records the model's own failing run's exit", func(t *testing.T) {
		dir := t.TempDir()
		cs := receiptSession(t, dir, true, true) // default gate: failcheck/passcheck → Safe
		cs.classifyShell = func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
			return shellrisk.Safe, "stub: safe", nil
		}
		res := receiptBashTurn(t, cs, []*AgentResponse{
			respWithCalls([]ToolCall{bashCall("t1", "failcheck")}),
			respWithAnswer("done"),
		})
		if !strings.Contains(res.Receipt, "test: failcheck (exit 1") {
			t.Fatalf("TurnResult.Receipt = %q, want the real bash run's exit 1", res.Receipt)
		}
	})
	t.Run("a blocked test command records (not run: …), never an exit", func(t *testing.T) {
		dir := t.TempDir()
		cs := receiptSession(t, dir, true, true)
		cs.classifyShell = func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
			return shellrisk.Risky, "stub: risky", nil
		}
		// No approver (confirmRisky nil) → the real gateShell blocks.
		res := receiptBashTurn(t, cs, []*AgentResponse{
			respWithCalls([]ToolCall{bashCall("t1", "failcheck")}),
			respWithAnswer("the gate said no"),
		})
		if strings.Contains(res.Receipt, "exit 0") {
			t.Fatalf("TurnResult.Receipt = %q, a blocked check must not record exit 0", res.Receipt)
		}
		if !strings.Contains(res.Receipt, "test: failcheck (not run:") {
			t.Fatalf("TurnResult.Receipt = %q, want the run recorded as (not run: …)", res.Receipt)
		}
	})
}
