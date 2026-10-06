package main

// Step 1 of issue #219: the turn receipt measures what the turn actually
// left in the workspace. This file pins the MEASUREMENT (the receipt's three
// facts — files changed, the model's own verification runs and their real
// exit codes, the files the format hook reported a problem with) and its
// SURFACE (TurnResult.Receipt on a tools-ran turn, empty otherwise), plus
// the hook-side regression: a per-call `hook: "skip"` on a session that
// implements FormatHookNoter suppresses the formatter.
//
// The verification fact is the model's own test/build runs (the bash
// recorder), NOT a harness run of the project's commands — the harness runs
// no project commands for the receipt (trust is the only gate for executing
// project-declared commands, and a measurement path that ran them itself
// would bypass that gate). The exit code is parsed from the bash tool's
// result text (the "[exit error: exit status N]" marker), never from a
// shadowed tool error.

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
	"github.com/dereksantos/cortex/internal/tools"
)

// receiptTestCmds is a command set whose test and build commands are exact
// `go test ./...` and `go build ./...` — the shape the bash recorder
// recognizes (receiptBashRole: an exact match or a prefix of the discovered
// command).
func receiptTestCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Test:  projectcmd.Command{Cmd: "go test ./...", Source: "go.mod"},
		Build: projectcmd.Command{Cmd: "go build ./...", Source: "go.mod"},
	}
}

// receiptSession builds a session over dir (a fresh temp dir by default, or
// a git repository when initGit) whose workdir, transcript, and project
// commands are wired so the receipt's measurements resolve. The workspace is
// EXPLICIT (the shape serve, --root, and tests all use): an explicit-root
// workspace is what the receipt's git read must measure. trusted controls
// the format hook's trust gate via the USER-level config
// (corpusTrustUserConfig — the authoritative trust source Config.WorkspaceTrusted
// reads); the hook ceiling is installed process-wide exactly as
// NewCortexSession does (SetHookCeiling(all)), and the hook state is
// session-allocated. The test and build commands are the discovered
// `go test ./...` / `go build ./...`; the format command is a per-file stub
// ({file} template, .go files) that a test-set hookRunner answers without
// exec'ing a formatter (the hook runs it through the internal tools' hookRunner
// seam).
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
	cs.projectCommands = projectcmd.Commands{
		Test:   projectcmd.Command{Cmd: "go test ./...", Source: "go.mod"},
		Build:  projectcmd.Command{Cmd: "go build ./...", Source: "go.mod"},
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
	}
	return cs
}

// receiptBashTurn drives one real turn on cs: a scripted sender that issues
// one round of tool calls (bashCall and/or writeFileCall), then a final
// answer. The coder dispatcher override intercepts the bash call — the
// receipt's BASH RECORDER still runs (it pairs the command with its outcome),
// but the tool's result text comes from the stub (bashResult), so the
// verification fact is exactly the exit code the stub reports. The
// write_file call (when present) runs through the REAL tool path (the
// format hook, the turn-end lint arm), so the unformatted fact is measured
// the way production measures it.
func receiptBashTurn(t *testing.T, cs *CortexSession, script []*AgentResponse, bashResult string) TurnResult {
	t.Helper()
	origDispatcher := cs.coderDispatcherOverride
	cs.coderDispatcherOverride = func() AgentDispatcher {
		return DispatchFunc(func(ctx context.Context, call ToolCall) string {
			if call.Function.Name == tools.FunctionBash {
				cmd, argErr := call.StringArg("command")
				if argErr == nil {
					cs.receiptBash(cmd)
				}
				cs.receiptBashOutcome(cmd, bashResult, nil, time.Millisecond)
				return bashResult
			}
			out, err := tools.Execute(ctx, call, cs)
			if err != nil {
				return "Error: " + err.Error()
			}
			return out
		})
	}
	t.Cleanup(func() { cs.coderDispatcherOverride = origDispatcher })
	cs.senderOverride = multiTurnScriptedSender(script)
	res, err := cs.Turn(context.Background(), "verify the build")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	return res
}

// TestTurnReceipt is the issue #219 acceptance table: (a) an edit plus a
// PASSING test run, (b) a FAILING run, (c) a turn that ran no verification
// (no tools at all — no receipt), (d) a non-git workspace (no files-changed
// section). Each case is a t.Run subtest; the git cases use a t.TempDir git
// repo, the non-git case a plain temp dir.
func TestTurnReceipt(t *testing.T) {
	// A stub hookRunner: a format run that LEAVES CHANGES (the "unformatted"
	// shape — the file's bytes differ after the run), recording every argv it
	// is asked to run.
	hookRan := []string{}
	prev := tools.SetHookRunner(func(_ context.Context, argv []string, _ string) (time.Duration, string, error) {
		hookRan = append(hookRan, strings.Join(argv, " "))
		p := argv[len(argv)-1]
		b, err := os.ReadFile(p)
		if err != nil {
			return time.Millisecond, "", err
		}
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			return time.Millisecond, "", err
		}
		return time.Millisecond, "", nil
	})
	t.Cleanup(func() { tools.SetHookRunner(prev) })

	cases := []struct {
		name            string
		git             bool
		trusted         bool
		script          []*AgentResponse
		bashResult      string
		wantReceipt     string // substring the receipt must contain ("" = assert the receipt is empty)
		wantNoReceipt   bool
		wantHookRuns    int
		wantUnformatted string
	}{
		{
			name:    "edit plus passing test run",
			git:     true,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{
					bashCall("t1", "go test ./..."),
					writeFileCall("w1", "main.go", "package main\n\nfunc main() {}\n"),
				}),
				respWithAnswer("verified the build"),
			},
			bashResult:      "ok  example.com  0.01s",
			wantReceipt:     "test: go test ./... (exit 0",
			wantHookRuns:    1,
			wantUnformatted: "main.go",
		},
		{
			name:    "failing test run reports the non-zero exit",
			git:     true,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{bashCall("t1", "go test ./...")}),
				respWithAnswer("the tests failed"),
			},
			bashResult:   "FAIL example.com [build failed]\nFAIL\n[exit error: exit status 1]",
			wantReceipt:  "test: go test ./... (exit 1",
			wantHookRuns: 0,
		},
		{
			name:          "no tools means no verification and no receipt",
			git:           true,
			trusted:       true,
			script:        []*AgentResponse{respWithAnswer("nothing to run")},
			bashResult:    "",
			wantNoReceipt: true,
			wantHookRuns:  0,
		},
		{
			name:    "non-git workspace has no files-changed section",
			git:     false,
			trusted: true,
			script: []*AgentResponse{
				respWithCalls([]ToolCall{bashCall("t1", "go test ./...")}),
				respWithAnswer("done"),
			},
			bashResult:   "ok  example.com  0.01s",
			wantReceipt:  "test: go test ./... (exit 0",
			wantHookRuns: 0,
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
			bashResult:      "",
			wantReceipt:     "files changed:",
			wantHookRuns:    0,
			wantUnformatted: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hookRan = nil
			dir := t.TempDir()
			cs := receiptSession(t, dir, tc.git, tc.trusted)
			res := receiptBashTurn(t, cs, tc.script, tc.bashResult)
			if tc.wantNoReceipt {
				if res.Receipt != "" {
					t.Fatalf("TurnResult.Receipt = %q, want empty (no tools ran — nothing measured)", res.Receipt)
				}
				return
			}
			if !strings.Contains(res.Receipt, tc.wantReceipt) {
				t.Fatalf("TurnResult.Receipt = %q, want it to contain %q", res.Receipt, tc.wantReceipt)
			}
			if tc.git && !strings.Contains(res.Receipt, "files changed:") {
				t.Errorf("TurnResult.Receipt = %q, want the files-changed section (git workspace)", res.Receipt)
			}
			if !tc.git && strings.Contains(res.Receipt, "files changed:") {
				t.Errorf("TurnResult.Receipt = %q, want NO files-changed section (non-git workspace)", res.Receipt)
			}
			if len(hookRan) != tc.wantHookRuns {
				t.Errorf("hook ran %d times (%v), want %d", len(hookRan), hookRan, tc.wantHookRuns)
			}
			if tc.wantUnformatted != "" {
				if !strings.Contains(res.Receipt, "unformatted: "+tc.wantUnformatted) {
					t.Errorf("TurnResult.Receipt = %q, want the unformatted fact naming %q", res.Receipt, tc.wantUnformatted)
				}
			}
		})
	}
}

// TestTurnReceiptNonZeroExitIsReal pins the blocker the reviewer found: the
// model-side bash recorder must parse the REAL exit code out of the bash
// tool's result text (the "[exit error: exit status N]" marker), never
// report a failed run as exit 0. A `go test ./...` that fails in the stub
// must surface "exit 1" (and the marker's real number when it is not 1).
func TestTurnReceiptNonZeroExitIsReal(t *testing.T) {
	dir := t.TempDir()
	cs := receiptSession(t, dir, true, true)
	cases := []struct {
		result string
		want   string
	}{
		{"FAIL example.com [build failed]\nFAIL\n[exit error: exit status 1]", "exit 1"},
		{"boom\n[exit error: exit status 3]", "exit 3"},
		{"ok  example.com  0.01s", "exit 0"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			res := receiptBashTurn(t, cs, []*AgentResponse{
				respWithCalls([]ToolCall{bashCall("t1", "go test ./...")}),
				respWithAnswer("ran the tests"),
			}, tc.result)
			if !strings.Contains(res.Receipt, "test: go test ./... ("+tc.want) {
				t.Fatalf("TurnResult.Receipt = %q, want %q", res.Receipt, tc.want)
			}
		})
	}
}

// TestReceiptBashRole pins the recorder's prefix matching: a command is a
// recognized run of the project's own test/build command when it is an exact
// match OR a prefix of it (the model ran the check with fewer flags). A
// foreign command is not this project's verification.
func TestReceiptBashRole(t *testing.T) {
	cs := &CortexSession{projectCommands: receiptTestCmds()}
	cases := []struct {
		command string
		role    string
		ok      bool
	}{
		{"go test ./...", "test", true},
		{"go test ./... -v", "test", true},
		{"go build ./...", "build", true},
		{"go test", "test", true},
		{"go vet ./...", "", false},
		{"rm -rf /", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		role, ok := cs.receiptBashRole(tc.command)
		if ok != tc.ok || string(role) != tc.role {
			t.Errorf("receiptBashRole(%q) = (%q, %v), want (%q, %v)", tc.command, role, ok, tc.role, tc.ok)
		}
	}
}

// TestParseBashExitCode pins the exit-code parse: the "[exit error: exit
// status N]" marker carries the process's real exit code; a result without
// the marker exited 0.
func TestParseBashExitCode(t *testing.T) {
	cases := []struct {
		in   string
		code int
		ok   bool
	}{
		{"ok  pkg  0.01s", 0, false},
		{"FAIL\n[exit error: exit status 1]", 1, true},
		{"boom\n[exit error: exit status 3]", 3, true},
		{"[exit error: exit status ]", 1, true},
		{"[exit error: exit status]", 1, true},
	}
	for _, tc := range cases {
		code, ok := parseBashExitCode(tc.in)
		if ok != tc.ok || code != tc.code {
			t.Errorf("parseBashExitCode(%q) = (%d, %v), want (%d, %v)", tc.in, code, ok, tc.code, tc.ok)
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
		verification: []receiptVerification{{role: "test", command: "go test ./...", exitCode: 1, elapsed: 420 * time.Millisecond}},
		unformatted:  []string{"main.go"},
	}.render()
	for _, want := range []string{"files changed:", "  a | 1 +", "verification:", "  test: go test ./... (exit 1, 0.42s)", "unformatted: main.go"} {
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
		respWithCalls([]ToolCall{bashCall("t1", "go test ./...")}),
		respWithAnswer("verified"),
	}, "ok  example.com  0.01s")
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
