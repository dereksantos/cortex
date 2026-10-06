package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// hookCeilingGuard is a process-wide mutex for tests that mutate the
// package-wide hook-mode state (activeCeiling) — it makes the mode tests
// safe under `go test -race` regardless of Go's intra-package test
// parallelism, since the ceiling is read by every tool call.
var hookCeilingGuard sync.Mutex

// withHookCeiling pins the process-wide ceiling to m for the duration of
// the test, restoring the previous value afterward (t.Cleanup: restored
// even when the test fails or panics).
func withHookCeiling(t *testing.T, m HookMode) {
	t.Helper()
	hookCeilingGuard.Lock()
	prev := activeCeiling
	SetHookCeiling(m)
	t.Cleanup(func() {
		SetHookCeiling(prev)
		hookCeilingGuard.Unlock()
	})
}

// recordingHookRunner is a stub hookRunner that records every argv it was
// asked to run and answers with a fixed (elapsed, out, err). The mode
// tests use it to assert WHICH commands the hook decided to run — the
// decision, not the execution.
type recordingHookRunner struct {
	ran []string // one "argv[0] argv[1] ..." line per call
}

func (r *recordingHookRunner) run(ctx context.Context, argv []string, dir string) (time.Duration, string, error) {
	if len(argv) > 0 {
		line := argv[0]
		for _, a := range argv[1:] {
			line += " " + a
		}
		r.ran = append(r.ran, line)
	}
	return 20 * time.Millisecond, "", nil
}

// installHookRunner swaps the package hookRunner for r for the duration of
// the test.
func installHookRunner(t *testing.T, r *recordingHookRunner) {
	t.Helper()
	orig := hookRunner
	hookRunner = r.run
	t.Cleanup(func() { hookRunner = orig })
}

// hookModeTestCmds is a command set whose format and lint are BOTH
// applicable to .go files, so a mode test can tell from the recorded argv
// which steps ran: "fmt-ran" is the format step, "lint-ran" the lint step.
func hookModeTestCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "fmt-ran {file}", PerFile: true, Extends: []string{".go"}, Source: "config.json"},
		Lint:   projectcmd.Command{Cmd: "lint-ran {file}", PerFile: true, Extends: []string{".go"}, Source: "config.json"},
	}
}

// writeHookProbe performs a write_file through the real Execute path on a
// temp dir and returns the tool result.
func writeHookProbe(t *testing.T, deps hookDeps, content string) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "probe.go", "content": content}), deps)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	return out
}

// TestParseHookMode pins the string→mode map: the empty (absent) sentinel is
// the default all, the three named modes parse (case-insensitively), and an
// unrecognized value is the safe off — a typo must never enable a hook the
// operator did not name.
func TestParseHookMode(t *testing.T) {
	cases := []struct {
		in   string
		want HookMode
	}{
		{"", HookModeAll},
		{"  ", HookModeAll},
		{"all", HookModeAll},
		{"ALL", HookModeAll},
		{"format", HookModeFormat},
		{"Format", HookModeFormat},
		{"off", HookModeOff},
		{"OFF", HookModeOff},
		{"ful", HookModeOff}, // a typo is the safe off
		{"everything", HookModeOff},
	}
	for _, tc := range cases {
		if got := ParseHookMode(tc.in); got != tc.want {
			t.Errorf("ParseHookMode(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestSetModeMonotoneDown pins SetMode's contract: it only accepts a
// MORE-restrictive value (off=2 > format=1 > all=0), and a less-restrictive
// request is a silent no-op.
func TestSetModeMonotoneDown(t *testing.T) {
	t.Run("off-then-all-keeps-off", func(t *testing.T) {
		s := &PostEditHookState{}
		s.SetMode(HookModeOff)
		if s.SessionMode() != HookModeOff {
			t.Fatalf("after SetMode(off): session mode = %d, want off", s.SessionMode())
		}
		s.SetMode(HookModeAll)
		if s.SessionMode() != HookModeOff {
			t.Fatalf("after SetMode(all): session mode = %d, want off (a raise is a no-op)", s.SessionMode())
		}
	})

	t.Run("all-then-format-lowers", func(t *testing.T) {
		s := &PostEditHookState{}
		if s.SessionMode() != HookModeAll {
			t.Fatalf("untouched session mode = %d, want all", s.SessionMode())
		}
		s.SetMode(HookModeFormat)
		if s.SessionMode() != HookModeFormat {
			t.Fatalf("after SetMode(format): session mode = %d, want format", s.SessionMode())
		}
		s.SetMode(HookModeAll)
		if s.SessionMode() != HookModeFormat {
			t.Fatalf("after SetMode(all): session mode = %d, want format (a raise is a no-op)", s.SessionMode())
		}
	})

	t.Run("format-then-off-lowers", func(t *testing.T) {
		s := &PostEditHookState{}
		s.SetMode(HookModeFormat)
		s.SetMode(HookModeOff)
		if s.SessionMode() != HookModeOff {
			t.Fatalf("after SetMode(format, off): session mode = %d, want off", s.SessionMode())
		}
	})

	t.Run("nil-state-is-a-noop", func(t *testing.T) {
		var s *PostEditHookState
		s.SetMode(HookModeOff) // must not panic
		if s.SessionMode() != HookModeAll {
			t.Fatalf("nil-state SessionMode() = %d, want all", s.SessionMode())
		}
	})

	t.Run("does-not-clamp-at-the-ceiling", func(t *testing.T) {
		withHookCeiling(t, HookModeAll)
		s := &PostEditHookState{}
		s.SetMode(HookModeOff)
		if s.SessionMode() != HookModeOff {
			t.Fatalf("session mode = %d, want off (SetMode stores what was asked; the ceiling is folded in at read time)", s.SessionMode())
		}
		if EffectiveHookMode(s) != HookModeOff {
			t.Fatalf("effective = %d, want off", EffectiveHookMode(s))
		}
	})
}

// TestEffectiveHookModeCeilingFold pins effectiveMode: the effective mode
// is the MORE-restrictive of (session mode, ceiling) — a session can hold a
// mode less restrictive than the ceiling, but never operates more permissively
// than the ceiling allows. (This is the regression for the round-2
// blocker: the comparison used to keep the SMALLER of the two, so a
// configured "off" ceiling was silently ignored.)
func TestEffectiveHookModeCeilingFold(t *testing.T) {
	t.Run("ceiling-off-wins-over-session-all", func(t *testing.T) {
		withHookCeiling(t, HookModeOff)
		s := &PostEditHookState{}
		if got := EffectiveHookMode(s); got != HookModeOff {
			t.Fatalf("effective = %d, want off (session all + ceiling off)", got)
		}
	})

	t.Run("ceiling-format-wins-over-session-all", func(t *testing.T) {
		withHookCeiling(t, HookModeFormat)
		s := &PostEditHookState{}
		if got := EffectiveHookMode(s); got != HookModeFormat {
			t.Fatalf("effective = %d, want format (session all + ceiling format)", got)
		}
	})

	t.Run("session-off-wins-over-ceiling-all", func(t *testing.T) {
		withHookCeiling(t, HookModeAll)
		s := &PostEditHookState{}
		s.SetMode(HookModeOff)
		if got := EffectiveHookMode(s); got != HookModeOff {
			t.Fatalf("effective = %d, want off (session off + ceiling all)", got)
		}
	})

	t.Run("nil-state-reads-the-bare-ceiling", func(t *testing.T) {
		withHookCeiling(t, HookModeFormat)
		var s *PostEditHookState
		if got := EffectiveHookMode(s); got != HookModeFormat {
			t.Fatalf("effective(nil state) = %d, want format (the bare ceiling)", got)
		}
	})
}

// TestHookModeByTrustAndCeiling is the requested matrix: mode {off,
// format, all} × trusted {true, false}, driven through the real write_file
// path. The stub hookRunner records argv, so the test asserts the hook's
// DECISION (which commands ran), not the execution. Piece 3: the per-edit
// hook is FORMAT-ONLY — lint runs once at the turn end (RunTurnEndLint), so
// "all" runs the format step per edit exactly like "format" did before, and
// the turn-end pass's mode gate ("all" only) is pinned separately by the
// turn-lint tests in cmd/cortex.
func TestHookModeByTrustAndCeiling(t *testing.T) {
	for _, mode := range []HookMode{HookModeOff, HookModeFormat, HookModeAll} {
		for _, trusted := range []bool{false, true} {
			name := "mode-" + hookModeName(mode) + "-trusted=" + boolName(trusted)
			t.Run(name, func(t *testing.T) {
				withHookCeiling(t, mode)
				rec := &recordingHookRunner{}
				installHookRunner(t, rec)

				deps := hookDeps{cmds: hookModeTestCmds(), trusted: trusted, state: &PostEditHookState{}}
				writeHookProbe(t, deps, "package main\n")

				switch {
				case !trusted:
					// Untrusted is the hard gate: NOTHING runs in any mode.
					if len(rec.ran) != 0 {
						t.Fatalf("untrusted: commands ran = %v, want none", rec.ran)
					}
				case mode == HookModeOff:
					if len(rec.ran) != 0 {
						t.Fatalf("off: commands ran = %v, want none", rec.ran)
					}
				default: // format AND all: the per-edit hook runs the format
					// step only (lint is the turn-end pass's job, piece 3).
					if len(rec.ran) != 1 || !strings.HasPrefix(rec.ran[0], "fmt-ran ") {
						t.Fatalf("mode %s: commands ran = %v, want only the per-edit format step", hookModeName(mode), rec.ran)
					}
				}
			})
		}
	}
}

func hookModeName(m HookMode) string {
	switch m {
	case HookModeOff:
		return "off"
	case HookModeFormat:
		return "format"
	default:
		return "all"
	}
}

func boolName(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestPerCallHookSkip pins `hook: "skip"`: it lowers the hook to off for
// THAT call only — the mode ceiling and the session mode are untouched, and
// the next call without skip behaves exactly as the mode dictates. It is
// tested for both write_file and edit_file, in the three ceilings.
func TestPerCallHookSkip(t *testing.T) {
	for _, mode := range []HookMode{HookModeAll, HookModeFormat, HookModeOff} {
		t.Run("ceiling-"+hookModeName(mode), func(t *testing.T) {
			withHookCeiling(t, mode)
			rec := &recordingHookRunner{}
			installHookRunner(t, rec)
			deps := hookDeps{cmds: hookModeTestCmds(), trusted: true, state: &PostEditHookState{}}

			root := t.TempDir()
			t.Chdir(root)
			writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
			// Start with an editable, already-formatted file.
			writeRepoFile(t, filepath.Join(root, "s.go"), "package main\n\nvar v = 1\n")

			// First call: hook: "skip" → nothing runs, whatever the mode.
			out1, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile, map[string]any{
				"path": "s.go", "content": "package main\n\nvar v = 2\n", "hook": "skip",
			}), deps)
			if err != nil {
				t.Fatalf("write_file: %v", err)
			}
			if len(rec.ran) != 0 {
				t.Fatalf("skipped call: commands ran = %v, want none", rec.ran)
			}
			_ = out1

			// Second call: no skip → the mode's per-edit commands run (the
			// skip must not have leaked into the next call). Piece 3: the
			// per-edit hook is format-only, so "all" runs the same one format
			// step as "format" — the turn-end lint pass is the "all" half that
			// moved to finalize, pinned by cmd/cortex's turn-lint tests.
			_, _, err = Execute(context.Background(), callArgs(t, FunctionEditFile, map[string]any{
				"path":       "s.go",
				"old_string": "var v = 2",
				"new_string": "var v = 3",
			}), deps)
			if err != nil {
				t.Fatalf("edit_file: %v", err)
			}
			want := 0
			if mode != HookModeOff {
				want = 1
			}
			if len(rec.ran) != want {
				t.Fatalf("call after skip: commands ran = %v, want %d (mode %s)", rec.ran, want, hookModeName(mode))
			}
			if data, _ := os.ReadFile(filepath.Join(root, "s.go")); !strings.Contains(string(data), "var v = 3") {
				t.Errorf("second edit must have landed, file = %q", data)
			}
		})
	}
}

// TestPostEditHookRealTimeout drives the REAL timeout path (no stub):
// project.command_timeout_sec = 1s (tools.Configure) and a format command
// that actually sleeps 5s. runHookDirect must cut it off at ~1s with
// errHookTimeout, and the hook note must name the command AND the budget
// ("sleep timed out after 1s"). The edit still succeeds and the file is
// left as written.
func TestPostEditHookRealTimeout(t *testing.T) {
	orig := active
	Configure(Limits{HookCommandBudgetSec: 1})
	t.Cleanup(func() { Configure(orig) })

	root := t.TempDir()
	t.Chdir(root)
	writeRepoFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "sleep 5", PerFile: true, Extends: []string{".go"}, Source: "config.json"},
	}

	start := time.Now()
	elapsed, out, err := runHookDirect(context.Background(), []string{"sleep", "5"}, root)
	if !errors.Is(err, errHookTimeout) {
		t.Fatalf("runHookDirect err = %v, want errHookTimeout", err)
	}
	if elapsed < time.Second || elapsed > 2*time.Second {
		t.Fatalf("elapsed = %v, want between 1s and 2s", elapsed)
	}
	if wall := time.Since(start); wall > 2*time.Second {
		t.Fatalf("real wall time = %v, want <= ~2s (the budget must cut the sleep off)", wall)
	}
	_ = out

	// End-to-end through the hook: the note names the command and the
	// budget, and the write still succeeds.
	out2, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
		map[string]any{"path": "z.go", "content": "package main\n"}), hookDeps{cmds: cmds, trusted: true})
	if err != nil {
		t.Fatalf("a hook timeout must not fail the write, got %v", err)
	}
	if !strings.Contains(out2, "sleep timed out after 1s") {
		t.Fatalf("timeout note must say \"sleep timed out after 1s\", got %q", out2)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "z.go")); string(data) != "package main\n" {
		t.Errorf("the file must be left as written when the hook times out")
	}
}

// TestPostEditHookBudgetDefault pins that the hook's per-command budget is
// read from the configured Limits (project.command_timeout_sec → tools'
// HookCommandBudgetSec), not a hardcoded 10s.
func TestPostEditHookBudgetDefault(t *testing.T) {
	if got, want := hookCommandBudget(), 10*time.Second; got != want {
		t.Fatalf("default hookCommandBudget() = %v, want %v", got, want)
	}
	orig := active
	Configure(Limits{HookCommandBudgetSec: 3})
	defer Configure(orig)
	if got, want := hookCommandBudget(), 3*time.Second; got != want {
		t.Fatalf("after Configure(3s): hookCommandBudget() = %v, want %v", got, want)
	}
}
