// turn_lint_test.go is the SESSION-side pin for the turn-end lint pass
// (issue #129, piece 3): the run-list decisions live in
// internal/tools' turn_lint_test.go, and this file pins what the SESSION
// does with the pass — that edits reach the lint list, that a finding
// reaches the model in one tools-withheld finalize round, that the receipt
// rides TurnResult.LintReceipt and the journal capture summary, and that
// the fail-closed gates (hook mode "format", an untrusted workspace) give
// no extra round and an empty receipt. The turn is driven end to end
// through cs.Turn against a scripted model server (testwatch_test.go's
// scriptedModelServer), so turn.go's real Toolset construction (the coder
// dispatcher + the FinalizeHook) and its TurnResult assembly are the ones
// under test, not a test-side copy.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/tools"
)

// turnLintSession builds a quiet, CWD-anchored session rooted at root — the
// headless `cortex turn` shape — with the turn-end lint pass armed for a
// {dir} lint on .go files, exactly the shape the session's dispatcher and
// the pass expect. trusted controls the trust gate through the USER-level
// config (redirects CORTEX_HOME and writes the list — the authoritative
// source Config.TrustedList reads), and mode installs the process-wide hook
// ceiling exactly as NewCortexSession does (Config.postEditHookMode →
// tools.SetHookCeiling); the pass lints only in "all". The stub hookRunner
// (tools.SetHookRunner) records every lint argv it is asked to run and
// answers clean, so the tests assert WHICH commands ran rather than
// exec'ing a linter.
func turnLintSession(t *testing.T, root, srvURL string, mode tools.HookMode, trusted bool, rec *lintRunRecorder) *CortexSession {
	t.Helper()
	t.Chdir(root)
	corpusTrustUserConfig(t, root, trusted)

	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.Request.BaseURL = srvURL
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })

	cs.workspace = mustWorkspace(t, root)
	cs.EnableMemory() // wires the capturer (journal captures) the same way the REPL does
	// A {dir} lint over .go files ("go vet {dir}"-shaped), no format
	// command: the per-edit hook is a no-op (no format), so every run the
	// stub records is a turn-end pass run, and one run per distinct touched
	// dir is the dedup shape the session must get right.
	cs.projectCommands = goLintProjectCmds()
	cs.Config = &Config{}
	cs.Config.Project.TurnLintBudgetSec = 60
	tools.SetHookCeiling(mode)
	t.Cleanup(func() { tools.SetHookCeiling(tools.HookModeAll) })

	// The stub records the run and reports a finding (the output), so the
	// pass produces a receipt and the model gets a finalize round.
	prev := tools.SetHookRunner(func(ctx context.Context, argv []string, dir string) (time.Duration, string, error) {
		rec.record(argv)
		return 20 * time.Millisecond, "LINT-FINDING: unused variable in pkg", nil
	})
	t.Cleanup(func() { tools.SetHookRunner(prev) })
	return cs
}

// goLintProjectCmds is a Go-module-shaped command set with a PER-PACKAGE
// lint ({dir}) over .go files and no format command — the shape that makes
// "one lint run per touched dir" (vs one per file) distinguishable.
func goLintProjectCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Lint: projectcmd.Command{Cmd: "lint-marker {dir}", Extends: []string{".go"}, Source: "go.mod"},
	}
}

// lintRunRecorder is an argv recorder the stub hookRunner appends to; it
// also reports one LINT FINDING per run it is asked to make (the output), so
// the turn-end pass produces a receipt and the model gets a finalize round —
// the session-level delivery this test is pinning. (A clean stub would make
// the pass silent, and the receipt/finalize/journal surfaces would be
// unreachable; the finding is the test's stand-in for "the linter found
// something.")
type lintRunRecorder struct {
	ran []string
}

func (r *lintRunRecorder) record(argv []string) {
	r.ran = append(r.ran, strings.Join(argv, " "))
}

// corpusTrustUserConfig redirects CORTEX_HOME (t.Setenv, restored) and
// writes the user-level config's project.trusted: trusted lists root,
// untrusted leaves the list empty. The direct user read is the
// authoritative trust source (Config.TrustedList → trustFromUserConfig), so
// this is exactly what the operator persists — and what a repository can
// never write for itself.
func corpusTrustUserConfig(t *testing.T, root string, trusted bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	list := "[]"
	if trusted {
		list = `["` + root + `"]`
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"project": {"trusted": `+list+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// turnLintScript is the scripted model for TestTurnEndLintReceipt: rounds 0
// and 1 are the turn's two edit_file calls (one per round — the coder
// dispatcher dispatches each round's calls, and each lands one file), round
// 2 is the work summary, and round 3 is served ONLY when the harness has a
// note — the model's reply to the finalize note. A clean/gated turn stops at
// round 2 (3 round-trips).
var turnLintScript = []string{
	`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"pkg/a.go\",\"old_string\":\"package pkg\",\"new_string\":\"package pkg // 1\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
	`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c2","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"pkg/b.go\",\"old_string\":\"package pkg\",\"new_string\":\"package pkg // 2\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
	`{"choices":[{"index":0,"message":{"role":"assistant","content":"Tuned pkg/a.go and pkg/b.go."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
	`{"choices":[{"index":0,"message":{"role":"assistant","content":"The turn-end lint flagged one finding in pkg."},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`,
}

// TestTurnEndLintReceipt is the issue #129 piece 3 session-level contract,
// driven end to end through cs.Turn (the exact path a headless `cortex
// turn` driver uses) against a scripted model. It pins the behaviors the
// internal/tools tests cannot (they can't import cmd/cortex):
//
//   - two edit_file calls to the same dir give exactly ONE lint run (the
//     pass dedups by package dir);
//   - the model gets the lint note in one tools-withheld finalize round
//     (the wire for the last round carries it);
//   - TurnResult.LintReceipt is set to the pass's receipt;
//   - the journal capture summary contains "lint:";
//   - hook mode "format", or an untrusted workspace, gives NO extra round
//     and an empty LintReceipt.
func TestTurnEndLintReceipt(t *testing.T) {
	cases := []struct {
		name        string
		mode        tools.HookMode
		trusted     bool
		wantRuns    int
		wantRounds  int
		wantReceipt bool
	}{
		{name: "all-trusted-one-run-finalize-round", mode: tools.HookModeAll, trusted: true,
			wantRuns: 1, wantRounds: 4, wantReceipt: true},
		{name: "format-no-pass-no-extra-round", mode: tools.HookModeFormat, trusted: true,
			wantRuns: 0, wantRounds: 3, wantReceipt: false},
		{name: "untrusted-no-pass-no-extra-round", mode: tools.HookModeAll, trusted: false,
			wantRuns: 0, wantRounds: 3, wantReceipt: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case gets its OWN scripted server: the round counter and
			// the wire log are per-server, so the round-count assertion is
			// measured against this case's turn alone.
			srv, st := scriptedModelServer(t, turnLintScript)
			root := t.TempDir()
			// A Go module with two .go files in ONE package dir: the turn
			// edits both, so a per-file pass would lint twice and the {dir}
			// pass once — the dedup assertion.
			writeProjectFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
			if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeProjectFile(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
			writeProjectFile(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")

			rec := &lintRunRecorder{}
			cs := turnLintSession(t, root, srv.URL, tc.mode, tc.trusted, rec)

			res, err := cs.Turn(context.Background(), "tune the two files in pkg")
			if err != nil {
				t.Fatalf("Turn: %v", err)
			}

			// The dedup decision: two edits in one dir give exactly one
			// lint run (the {dir} pass) — in the trusted "all" case only.
			if len(rec.ran) != tc.wantRuns {
				t.Fatalf("lint runs = %v (%d), want %d", rec.ran, len(rec.ran), tc.wantRuns)
			}
			if len(rec.ran) > 0 && !strings.Contains(rec.ran[0], "./pkg") {
				t.Errorf("lint run argv = %q, want the {dir} substitution ./pkg", rec.ran[0])
			}

			// The round count is the extra-round assertion: a finding adds
			// exactly one tools-withheld finalize round (3 + 1); the gated
			// cases (format, untrusted) get none.
			if st.n != tc.wantRounds {
				t.Fatalf("model round-trips = %d, want %d", st.n, tc.wantRounds)
			}

			if tc.wantReceipt {
				// The model was handed the lint note in the finalize round:
				// the last wire carried it.
				if len(st.wires) != tc.wantRounds {
					t.Fatalf("wires recorded = %d, want %d", len(st.wires), tc.wantRounds)
				}
				wire := st.wires[len(st.wires)-1]
				if !strings.Contains(wire, "turn-end lint pass") {
					t.Errorf("the finalize round's wire did not carry the lint note — the model was never told")
				}
				if !strings.Contains(wire, "LINT-FINDING") {
					t.Errorf("the finalize round's wire did not carry the finding itself — %q", wire)
				}
				// The receipt rides TurnResult, with the finding.
				if res.LintReceipt == "" || !strings.Contains(res.LintReceipt, "lint:") {
					t.Errorf("TurnResult.LintReceipt = %q, want the \"lint: …\" receipt", res.LintReceipt)
				}
				if !strings.Contains(res.LintReceipt, "LINT-FINDING") {
					t.Errorf("TurnResult.LintReceipt = %q, want the finding folded in", res.LintReceipt)
				}
				// The journal capture summary carries it too.
				if got := lastCaptureResult(t, cs); !strings.Contains(got, "lint:") {
					t.Errorf("journal capture summary = %q, want it to contain \"lint:\"", got)
				}
			} else {
				if res.LintReceipt != "" {
					t.Errorf("TurnResult.LintReceipt = %q, want empty (the pass is gated)", res.LintReceipt)
				}
				if got := lastCaptureResult(t, cs); strings.Contains(got, "lint:") {
					t.Errorf("journal capture summary = %q, must not carry a lint line (the pass is gated)", got)
				}
			}
		})
	}
}
