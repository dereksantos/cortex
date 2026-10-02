// turn_lint_test.go pins the turn-end lint pass's run list and budget
// (issue #129 piece 3): RunTurnEndLint (project_command_hook.go) is the
// engine of the pass, and this file pins its DECISIONS with a stub
// hookRunner that records argv — which files get linted, how often, and
// what the receipt says — not the execution. The session-side delivery
// (the finalize round, TurnResult.LintReceipt, the journal) is pinned by
// cmd/cortex's turn_lint_test.go.

package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// lintCmds is a Go-module-shaped command set with a PER-FILE lint ({file}):
// one run per distinct touched file — the shape that makes "one run per
// file" vs "one run per dir" distinguishable. The per-package ({dir}) shape
// is the existing goRepoCmds' "go vet {dir}".
func lintCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Lint: projectcmd.Command{Cmd: "lint-marker {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
	}
}

// TestRunTurnEndLintRunList pins the run-list decisions: dedup, per-dir vs
// per-file, and the fail-closed gates (trust, whole-project lint, budget).
func TestRunTurnEndLintRunList(t *testing.T) {
	t.Chdir(t.TempDir())
	// The stub records every argv and answers clean: the test asserts the
	// DECISION (which commands ran), not the execution.
	rec := &recordingHookRunner{}
	installHookRunner(t, rec)

	cases := []struct {
		name    string
		cmds    projectcmd.Commands
		files   []string
		trusted bool
		budget  time.Duration
		runs    int // want number of recorded runs
		want    string
	}{
		{
			name:    "per-file-one-run-per-distinct-file",
			cmds:    lintCmds(),
			files:   []string{"a/x.go", "a/y.go", "b/z.go"},
			trusted: true,
			budget:  time.Minute,
			runs:    3,
			want:    "", // a clean run is silent: no findings, no budget hit
		},
		{
			name:    "per-dir-one-run-per-distinct-dir",
			cmds:    goRepoCmds(),
			files:   []string{"a/x.go", "a/y.go", "b/z.go"},
			trusted: true,
			budget:  time.Minute,
			runs:    2, // two dirs (a, b): three files, one run per dir
			want:    "",
		},
		{
			name:    "untrusted-nothing-runs",
			cmds:    goRepoCmds(),
			files:   []string{"a/x.go"},
			trusted: false,
			budget:  time.Minute,
			runs:    0,
			want:    "",
		},
		{
			name: "whole-project-lint-never-auto-runs",
			cmds: projectcmd.Commands{
				Lint: projectcmd.Command{Cmd: "golangci-lint run", Source: "go.mod"},
			},
			files:   []string{"a/x.go"},
			trusted: true,
			budget:  time.Minute,
			runs:    0,
			want:    "",
		},
		{
			name:    "zero-budget-default-budget-runs",
			cmds:    goRepoCmds(),
			files:   []string{"a/x.go"},
			trusted: true,
			budget:  0, // 0 = the caller didn't arm a budget: the default (60s) applies
			runs:    1,
			want:    "",
		},
		{
			name:    "deadline-already-past-budget-exhausted",
			cmds:    goRepoCmds(),
			files:   []string{"a/x.go", "a/y.go"},
			trusted: true,
			budget:  -time.Second, // a negative budget is a deadline already in the past
			runs:    0,
			want:    "budget (1s) exhausted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The pass stats its touched files against root (the deleted-
			// file skip): root = the process CWD, and the touched paths are
			// CWD-relative (the CWD-implicit session shape) — so every file
			// must actually exist for its run to count.
			if err := os.MkdirAll("./a", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll("./b", 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"a/x.go", "a/y.go", "b/z.go"} {
				writeRepoFile(t, f, "package p\n")
			}

			rec.ran = nil
			got := RunTurnEndLint(context.Background(), tc.cmds, "", tc.files, tc.trusted, tc.budget)
			if len(rec.ran) != tc.runs {
				t.Fatalf("runs = %v (%d), want %d", rec.ran, len(rec.ran), tc.runs)
			}
			if tc.want == "" {
				if got != "" {
					t.Errorf("receipt = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("receipt = %q, want it to contain %q", got, tc.want)
			}
		})
	}

	// The per-dir case's argv: exactly one run per touched dir, first-touch
	// dir order.
	rec.ran = nil
	_ = RunTurnEndLint(context.Background(), goRepoCmds(), "", []string{"a/x.go", "a/y.go", "b/z.go"}, true, time.Minute)
	if len(rec.ran) != 2 {
		t.Fatalf("per-dir runs = %v, want 2 (one per touched dir)", rec.ran)
	}
	if !strings.Contains(rec.ran[0], "./a") || !strings.Contains(rec.ran[1], "./b") {
		t.Errorf("per-dir argv = %v, want ./a first (first-touch dir order) then ./b", rec.ran)
	}
}

// TestRunTurnEndLintDeletedFileIsNotLinted pins the removed-file fix: a
// touched file that no longer exists on disk (the turn wrote it, then
// remove_path'd it) is skipped — linting a missing path would report a
// spurious "could not run" finding for a file that is gone on purpose.
func TestRunTurnEndLintDeletedFileIsNotLinted(t *testing.T) {
	t.Chdir(t.TempDir())
	rec := &recordingHookRunner{}
	installHookRunner(t, rec)

	// CWD-implicit shape: root "" = the process CWD; write a file, stat it
	// (the touch happened while it existed), then remove it (the turn's
	// remove_path leg).
	writeRepoFile(t, "gone.go", "package main\n")
	if _, err := os.Stat("gone.go"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove("gone.go"); err != nil {
		t.Fatal(err)
	}

	if got := RunTurnEndLint(context.Background(), goRepoCmds(), "", []string{"gone.go"}, true, time.Minute); got != "" {
		t.Errorf("deleted file: receipt = %q, want empty (a removed file is not linted)", got)
	}
	if len(rec.ran) != 0 {
		t.Errorf("deleted file: runs = %v, want none", rec.ran)
	}

	// A mix: the deleted file is skipped, the live file is linted.
	writeRepoFile(t, "live.go", "package main\n")
	rec.ran = nil
	got := RunTurnEndLint(context.Background(), goRepoCmds(), "", []string{"gone.go", "live.go"}, true, time.Minute)
	if len(rec.ran) != 1 {
		t.Fatalf("mixed runs = %v, want 1 (the live file only)", rec.ran)
	}
	if got != "" {
		t.Errorf("mixed receipt = %q, want empty (the live file is clean)", got)
	}
}

// TestRunTurnEndLintSlowRunCutOffAtTurnBudget pins the budget fix: the
// deadline is not just a check between runs — it travels into each
// hookRunner call as the run's context, so a run in progress is cut off at
// the turn deadline even when the per-command budget (command_timeout_sec)
// is LONGER than the turn budget, and the receipt reports the CONFIGURED
// budget, not a clamped time.Until(deadline) (the old "after ~0s").
func TestRunTurnEndLintSlowRunCutOffAtTurnBudget(t *testing.T) {
	t.Chdir(t.TempDir())
	// The stub hangs until its ctx is canceled, then reports the run error
	// as the ctx's error — exactly how exec.CommandContext surfaces a
	// deadline (the run's output is empty, like a killed process). The pass
	// classifies a deadline-caused run error as a budget hit, not as a
	// per-file finding.
	slow := func(ctx context.Context, argv []string, dir string) (time.Duration, string, error) {
		select {
		case <-time.After(30 * time.Second):
			return 30 * time.Second, "", nil
		case <-ctx.Done():
			return 0, "", ctx.Err()
		}
	}
	orig := hookRunner
	hookRunner = slow
	t.Cleanup(func() { hookRunner = orig })

	writeRepoFile(t, "a.go", "package main\n")
	writeRepoFile(t, "b.go", "package main\n")

	start := time.Now()
	got := RunTurnEndLint(context.Background(), lintCmds(), "", []string{"a.go", "b.go"}, true, 150*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("the pass took %s; a slow linter must be cut off near the turn budget, not after the per-command 10s", elapsed)
	}
	if !strings.Contains(got, "turn lint budget (0.1s) exhausted — 1 of 2 runs not run") {
		t.Errorf("receipt = %q, want \"1 of 2 runs not run\" (the first run was cut off in progress, the second was never started)", got)
	}
	if !strings.Contains(got, "lint:") {
		t.Errorf("receipt = %q, want the \"lint: \" prefix", got)
	}
}
