package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	stubSafe := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test: safe", nil
	}
	stubRisky := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
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
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool { return true }}
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
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool { return false }}
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
			confirmRisky: func(string) bool { return true }} // present but ignored when quiet
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
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool {
			t.Fatal("confirmRisky must not be invoked for a subagent-depth call")
			return true
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
		if !strings.Contains(strings.ToLower(got), "no interactive approval") {
			t.Errorf("expected the headless-blocked message, got %q", got)
		}
	})

	// Control: an explicit depth-0 context (the coder's own top-level call)
	// keeps the interactive confirm path unchanged.
	t.Run("risky command at depth 0 still uses interactive confirm", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool { return true }}
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

// grep's exit 1 means "no matches" — a content-free result, not a failure.
// It must read as such, not as a bare "[exit error: exit status 1]" the model
// can't distinguish from a broken command.
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
