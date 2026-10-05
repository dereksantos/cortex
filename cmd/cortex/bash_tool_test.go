package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
)

// Shell metacharacters get an explicit, instructive rejection — the tool
// execs without a shell, so a passed-through `|` previously reached the
// binary as a literal arg and produced confusing downstream errors the
// model retried verbatim ("find: |: unknown primary").
// Shell syntax (pipes, redirects, chaining) now runs via `bash -c` when the
// risk gate permits it — the old "not supported" rejection is gone. The gate,
// not the tokenizer, is what governs whether a command runs.
func TestBashShellSyntax(t *testing.T) {
	stubSafe := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test: safe", nil
	}
	stubRisky := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Risky, "test: risky", nil
	}

	t.Run("pipe runs when the gate allows", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubSafe}
		args, _ := json.Marshal(map[string]string{"command": "echo hello | tr a-z A-Z"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "HELLO") {
			t.Errorf("pipe did not run through bash -c: %q", got)
		}
	})

	t.Run("chaining runs when the gate allows", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubSafe}
		args, _ := json.Marshal(map[string]string{"command": "echo a && echo b"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
			t.Errorf("chained command did not run: %q", got)
		}
	})

	t.Run("deny-floor blocks even when the classifier says safe", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cs := &CortexSession{classifyShell: stubSafe}
		args, _ := json.Marshal(map[string]string{"command": "echo x > /etc/cortex-should-never-write"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(strings.ToLower(got), "refused") {
			t.Errorf("deny-floor should refuse the redirect, got %q", got)
		}
	})

	t.Run("risky command runs after interactive yes", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) lineedit.ConfirmChoice { return lineedit.ConfirmYes }}
		args, _ := json.Marshal(map[string]string{"command": "echo confirmed | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "confirmed") {
			t.Errorf("approved risky command did not run: %q", got)
		}
	})

	t.Run("risky command refused after interactive no", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) lineedit.ConfirmChoice { return lineedit.ConfirmNo }}
		args, _ := json.Marshal(map[string]string{"command": "echo nope | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "nope") {
			t.Errorf("declined command should not have run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "declined") {
			t.Errorf("expected a declined message, got %q", got)
		}
	})

	t.Run("risky command blocked when headless (no approver)", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			confirmRisky: func(string) lineedit.ConfirmChoice { return lineedit.ConfirmYes }} // present but ignored when quiet
		args, _ := json.Marshal(map[string]string{"command": "echo headless | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "headless\n") {
			t.Errorf("headless risky command should not run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "block") {
			t.Errorf("expected a blocked message when headless, got %q", got)
		}
	})

	// M4.2: a subagent (e.g. the `agent` profile) has no human operator
	// mid-loop — Risky must fall straight to the headless-blocked shape, never
	// the interactive confirm prompt, regardless of confirmRisky/quiet.
	t.Run("risky command blocked inside a subagent regardless of confirmRisky", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) lineedit.ConfirmChoice {
			t.Fatal("confirmRisky must not be invoked for a subagent-depth call")
			return lineedit.ConfirmYes
		}}
		ctx := withSubagentDepth(context.Background(), 1)
		args, _ := json.Marshal(map[string]string{"command": "echo nested | cat"})
		got, err := tools.Execute(ctx, tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "nested\n") {
			t.Errorf("subagent-depth risky command should not run: %q", got)
		}
		// The blocked message is the shared reworded shape from
		// shellrisk.BlockedMessage (issue #169) — "this action is not
		// permitted in this session. Don't retry it with a different command
		// that has the same effect…" — not the old "no interactive approval"
		// wording.
		if !strings.Contains(got, "this action is not permitted in this session") {
			t.Errorf("expected the shared blocked message, got %q", got)
		}
	})

	// Control: an explicit depth-0 context (the coder's own top-level call)
	// keeps the interactive confirm path unchanged.
	t.Run("risky command at depth 0 still uses interactive confirm", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) lineedit.ConfirmChoice { return lineedit.ConfirmYes }}
		ctx := withSubagentDepth(context.Background(), 0)
		args, _ := json.Marshal(map[string]string{"command": "echo depth-zero | cat"})
		got, err := tools.Execute(ctx, tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "depth-zero") {
			t.Errorf("approved depth-0 risky command should have run: %q", got)
		}
	})

	// Phase 7 (docs/cortex-web.md, discord.go): approveRisky is gateShell's
	// approval path for a quiet-but-human-present session (Discord) —
	// checked independently of cs.quiet, unlike confirmRisky above.
	t.Run("risky command approved via approveRisky while quiet", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			approveRisky: func(context.Context, string, string) (bool, bool) { return true, false }}
		args, _ := json.Marshal(map[string]string{"command": "echo discord-approved | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "discord-approved") {
			t.Errorf("approveRisky-approved command should have run: %q", got)
		}
	})

	t.Run("risky command declined via approveRisky while quiet", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			approveRisky: func(context.Context, string, string) (bool, bool) { return false, false }}
		args, _ := json.Marshal(map[string]string{"command": "echo discord-denied | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "discord-denied") {
			t.Errorf("declined command should not have run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "declined") {
			t.Errorf("expected a declined message, got %q", got)
		}
	})

	// The timeout path must reproduce the exact headless-Blocked message
	// (session_core.go's approveRisky docstring) — indistinguishable from
	// "no approver at all" by design, unlike an explicit decline above.
	t.Run("risky command approveRisky timeout reproduces the headless-blocked message", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			approveRisky: func(context.Context, string, string) (bool, bool) { return false, true }}
		args, _ := json.Marshal(map[string]string{"command": "echo discord-timeout | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "discord-timeout") {
			t.Errorf("timed-out command should not have run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "block") {
			t.Errorf("expected the blocked message on timeout, got %q", got)
		}
		if strings.Contains(strings.ToLower(got), "declined") {
			t.Errorf("timeout must read as blocked, not declined: %q", got)
		}
	})

	// approveRisky must not be reachable inside a subagent, same as
	// confirmRisky (M4.2).
	t.Run("risky command blocked inside a subagent regardless of approveRisky", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			approveRisky: func(context.Context, string, string) (bool, bool) {
				t.Fatal("approveRisky must not be invoked for a subagent-depth call")
				return true, false
			}}
		ctx := withSubagentDepth(context.Background(), 1)
		args, _ := json.Marshal(map[string]string{"command": "echo nested-approver | cat"})
		got, err := tools.Execute(ctx, tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "nested-approver\n") {
			t.Errorf("subagent-depth risky command should not run: %q", got)
		}
	})
}

// Regression: a quoted grep pattern must actually match. Before the tokenizer
// fix, `grep -n "X" f` searched for the literal `"X"` (quotes included), found
// nothing, and the model looped on the identical command (2026-06-14).
func TestBashHonorsQuotedArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("func TestScroller(t *testing.T) {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{`grep -n Scroller f.txt`, `grep -n "Scroller" f.txt`} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", cmd, err)
		}
		if !strings.Contains(got, "Scroller") {
			t.Errorf("%q: got %q, want a line containing Scroller", cmd, got)
		}
	}
}

// TestBashGrepNoMatch pins grep's exit-1 no-match result: it must read as a
// content-free result, not a bare exit error the model can't distinguish
// from a broken command.
func TestBashGrepNoMatch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"command": `grep -n Absent f.txt`})
	got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "(no matches)" {
		t.Errorf("got %q, want %q", got, "(no matches)")
	}
	if strings.Contains(got, "exit error") {
		t.Errorf("grep no-match should not surface as an exit error: %q", got)
	}
}

// TestSameActionLedger pins the per-turn same-action gate (issue #169):
// once a command in an effect class is Blocked in a turn, a later command
// in the same class is refused before classification, an unrelated Safe
// command still runs, and the ledger resets on a new turn.
//
// The stub classifyShell returns Risky for every tracked effect class
// (git-history-write, hook-disabling, git-stash) so the first is blocked by
// the risk gate, recording its effect class in the ledger, but Safe for
// everything else (so an unrelated command still runs). Every later
// same-group command is refused by the ledger — not re-classified — and
// carries the same-action refusal; the first block of each class carries the
// shared risk message. git-stash is its OWN group (issue #201): a declined
// stash bars another stash form in the turn, but NOT a git commit.
func TestSameActionLedger(t *testing.T) {
	// Risky for the two tracked effect classes and for the network-access
	// control (curl), Safe for the rest — this mirrors the real gate's shape
	// for the commands the scenarios use (git history writes and hook-
	// disabling flags are Risky; curl is gray-zone Risky; `ls`/`git status`
	// are read-only Safe) and lets one stub drive every scenario.
	stub := func(_ context.Context, command, _ string) (shellrisk.Level, string, error) {
		if shellrisk.EffectClass(command) != "" {
			return shellrisk.Risky, "test: always risky", nil
		}
		if strings.Contains(command, "curl") {
			return shellrisk.Risky, "test: network access", nil
		}
		return shellrisk.Safe, "test: safe", nil
	}

	// step is one command in a scenario, with the expected gate outcome.
	type step struct {
		command string
		wantRun bool // true → gateShell returns ok=true (command may run)
		// wantMarker is the substring the blocked message must contain.
		// "" for a run. For a blocked command it distinguishes the shared
		// risk-blocked message ("blocked (risk:") from the same-action
		// refusal ("same action").
		wantMarker string
	}

	scenarios := []struct {
		name  string
		steps []step
	}{
		{
			name: "first blocked commit lets same-class variants through as Blocked in the same turn",
			steps: []step{
				{command: "git commit -m x", wantRun: false, wantMarker: "blocked (risk:"},
				{command: "git commit-tree HEAD", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				{command: "git commit --amend", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				// The issue's exact workaround: after a blocked commit, the hook-
				// disabling variants (a DIFFERENT class, one barred group) are
				// refused by the ledger too — not re-classified.
				{command: "git commit --no-verify -m x", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				{command: "git -c core.hooksPath=/tmp/x commit -m x", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				{command: "git update-ref refs/heads/main abc123", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				{command: "git reset --hard HEAD~1", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				// A Risky command in a DIFFERENT class (no tracked effect class
				// — network access) is not in the ledger; it is blocked by the
				// risk gate with the shared message, not the same-action refusal.
				{command: "curl http://example.com", wantRun: false, wantMarker: "blocked (risk:"},
			},
		},
		{
			name: "hook-disabling flag is refused by the risk gate, then the class is recorded",
			steps: []step{
				{command: "git commit --no-verify -m x", wantRun: false, wantMarker: "blocked (risk:"},
				// A later --no-verify variant (different subcommand) is in the
				// same hook-disabling class → refused by the ledger.
				{command: "git push --no-verify origin", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				// The barred group runs both ways: a blocked hook-disabling
				// command also bars a PLAIN same-effect history write, so a
				// re-worded `git commit` can't re-enter the classifier.
				{command: "git commit -m y", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
			},
		},
		{
			name: "unrelated Safe command still runs after a block in the same turn",
			steps: []step{
				{command: "git commit -m x", wantRun: false, wantMarker: "blocked (risk:"},
				{command: "ls", wantRun: true, wantMarker: ""},
				{command: "git status", wantRun: true, wantMarker: ""},
				// A same-class command after the Safe interlude is still refused.
				{command: "git commit --amend", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
			},
		},
		{
			// Issue #201: git-stash is its OWN effect class. A declined risky
			// `git stash` bars another stash form in the same turn (the ledger
			// keeps a refused stash from re-entering as a different stash
			// spelling), but it must NOT bar an unrelated `git commit` — a
			// stash is not a way to re-route a commit, and the shared-barred-
			// group over-blocking was the bug this scenario used to pin.
			name: "a blocked git stash bars other stash forms, not commits",
			steps: []step{
				{command: "git stash pop", wantRun: false, wantMarker: "blocked (risk:"},
				{command: "git stash push -m wip", wantRun: false, wantMarker: "same action as an earlier blocked command in this turn"},
				// An unrelated history write is NOT in the stash class — it
				// re-enters the risk gate (the stub classifies it Risky) and is
				// blocked there with the shared risk message, not the ledger's
				// same-action refusal.
				{command: "git commit -m x", wantRun: false, wantMarker: "blocked (risk:"},
				// A read-only stash form is NOT in the class — it runs.
				{command: "git stash list", wantRun: true, wantMarker: ""},
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cs := &CortexSession{classifyShell: stub, quiet: true, turnNo: 1}
			for i, s := range sc.steps {
				msg, ok := cs.gateShell(context.Background(), s.command)
				if ok != s.wantRun {
					t.Errorf("step %d (%q): ok = %v, want %v", i, s.command, ok, s.wantRun)
				}
				if !s.wantRun && !strings.Contains(msg, s.wantMarker) {
					t.Errorf("step %d (%q): blocked message %q does not contain %q", i, s.command, msg, s.wantMarker)
				}
			}
		})
	}
}

// TestSameActionLedger_ResetsOnNewTurn pins the ledger's turn scoping
// (issue #169): a block recorded in turn N does not leak into turn N+1. The
// turn's lifecycle stamps cs.turnNo at entry (turn.go) and clears it on exit;
// the ledger is keyed on turnNo and reset at turn start, so the same command
// that was blocked in one turn may be re-evaluated by the risk gate in the
// next (it is refused again by the gate, but NOT by the stale ledger).
func TestSameActionLedger_ResetsOnNewTurn(t *testing.T) {
	stubRisky := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Risky, "test: always risky", nil
	}
	cs := &CortexSession{classifyShell: stubRisky, quiet: true}

	// Turn 1: block a git commit; its effect class lands in the ledger.
	cs.turnNo = 1
	cs.sameActionBlocked = nil // turn-start reset (turn.go)
	msg, ok := cs.gateShell(context.Background(), "git commit -m x")
	if ok {
		t.Fatalf("turn 1: git commit should be blocked by the risk gate")
	}
	if !strings.Contains(msg, "blocked (risk:") {
		t.Errorf("turn 1: first block should carry the shared risk message, got %q", msg)
	}
	// A same-class variant in turn 1 is refused by the ledger.
	msg, ok = cs.gateShell(context.Background(), "git commit --amend")
	if ok {
		t.Fatalf("turn 1: git commit --amend should be blocked")
	}
	if !strings.Contains(msg, "same action as an earlier blocked command in this turn") {
		t.Errorf("turn 1: same-class variant should carry the same-action refusal, got %q", msg)
	}

	// Turn 2: a fresh turnNo + reset. The same command is re-evaluated by
	// the risk gate — blocked again by the gate (not the stale ledger).
	cs.turnNo = 2
	cs.sameActionBlocked = nil // turn-start reset (turn.go)
	msg, ok = cs.gateShell(context.Background(), "git commit -m x")
	if ok {
		t.Fatalf("turn 2: git commit should be blocked by the risk gate")
	}
	if strings.Contains(msg, "same action as an earlier blocked command in this turn") {
		t.Errorf("turn 2: stale ledger leaked across turns; message %q carries the same-action refusal", msg)
	}
	if !strings.Contains(msg, "blocked (risk:") {
		t.Errorf("turn 2: first block of the turn should carry the shared risk message, got %q", msg)
	}

	// Between turns (turnNo == 0) the ledger is inert: no same-action
	// refusal. A record made between turns is also dropped, not stored —
	// but only if the ledger map itself was cleared (turn.go's turn-start
	// reset sets it to nil); a stale non-nil map from a prior turn must not
	// receive a record while turnNo == 0. We model the reset by setting
	// sameActionBlocked = nil, exactly as turn.go does at turn start.
	cs.turnNo = 0
	cs.sameActionBlocked = nil // turn.go's turn-start reset
	if cs.sameActionBlockedInTurn(shellrisk.EffectGitHistoryWrite) {
		t.Errorf("ledger must be inert between turns (turnNo == 0)")
	}
	cs.recordSameActionBlock("git commit -m x")
	if len(cs.sameActionBlocked) != 0 {
		t.Errorf("recordSameActionBlock between turns must drop, not store: %v", cs.sameActionBlocked)
	}
}
