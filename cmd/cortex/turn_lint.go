// turn_lint.go is the session side of the turn-end lint pass (issue #129,
// piece 3): lint moved off the per-edit hook — clippy/eslint are slow and
// noisy per edit — and now runs ONCE per turn, at the clean-finalize point,
// over the distinct files the turn touched.
//
// The pass rides the SAME delivery path as the #145 test-loss receipt: the
// turn's FinalizeHook (turn.go) consults turnLintAtFinalize alongside
// testwatchFinalizeNote, so findings reach the model in one more
// tools-withheld round while it can still fix them (the model is asked to
// restate its complete answer accounting for the findings, the same way it
// is asked to account for a removed test). The turn's lintReceipt then
// rides TurnResult.LintReceipt (the REPL and `cortex turn` print it to the
// human) and is folded into the capture summary (the journal record shows
// it). A turn with no findings gets NO extra round and NO receipt — the
// clean case is byte-identical to the pre-pass behavior.
//
// Gates, in order (all fail closed, the per-edit hook's contract):
//   - trust: an untrusted workspace runs nothing — trust is the hard gate,
//     ahead of the mode;
//   - mode: only "all" lints (the effective mode — the configured ceiling
//     folded with a REPL /hook lowering; "format" and "off" skip the pass);
//   - files: the turn's distinct touched files (workdir-resolved, first-
//     touch order), deduplicated by package dir for per-package ({dir})
//     commands — several edits in one turn produce exactly one lint run
//     per touched dir;
//   - budget: a TOTAL lint budget per turn (project.turn_lint_budget_sec,
//     default 60s) caps the whole pass; a slow linter is cut off there and
//     the receipt says so.
package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/tools"
)

// lintTouchedPath records a path the turn's mutating calls are about to
// touch, for the turn-end lint pass. It is the per-edit counterpart of
// touchFile (the testwatch snapshot): touchFile keys the testguard scan
// (test files, scratch files) and lintTouchedPath keys the lint pass (every
// file the turn writes or edits, in first-touch order). Both are armed from
// the coder dispatcher, before the tool runs. Missing files are recorded
// too — a write_file that creates a file is the pass's main input.
//
// The key is the workdir-resolved path (cs.Workdir()-anchored, like
// resolveWorkdir in the tools package), normalized with filepath.Clean:
// "pkg/a.go", "./pkg/a.go", and (an anchored) "/root/pkg/a.go" are one
// entry, so a turn that spells the same file two ways lints it once.
func (cs *CortexSession) lintTouchedPath(path string) {
	if cs == nil || path == "" {
		return
	}
	rel := path
	if filepath.IsAbs(path) {
		if wd := cs.Workdir(); wd != "" {
			r, err := filepath.Rel(wd, path)
			if err != nil || r == "" || strings.HasPrefix(r, "..") {
				return // outside the workspace: the pass is scoped to it
			}
			rel = r
		} else {
			return // no anchor: an absolute path is out of reach (same rule as touchFile)
		}
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, seen := range cs.turnLinter.touched {
		if seen == rel {
			return
		}
	}
	cs.turnLinter.touched = append(cs.turnLinter.touched, rel)
}

// turnLintAtFinalize is the turn-end lint pass's FinalizeHook contribution
// (the #145 delivery path): it runs the pass for the turn's touched files
// and, when it found something, returns the note the engine hands the model
// in one more tools-withheld round — the model's final answer then accounts
// for the findings (restated, like the test-loss receipt's ask). It also
// stores the turn's lintReceipt (the durable fact for TurnResult and the
// journal). An empty return means "no findings, no extra round": nothing
// touched, mode not "all", untrusted, no lint command, or a clean run.
func (cs *CortexSession) turnLintAtFinalize(ctx context.Context) string {
	receipt := cs.runTurnLint(ctx)
	if receipt == "" {
		return ""
	}
	cs.lintReceipt = receipt
	return "Before you finish: the turn-end lint pass (your project's lint, run once over the files you touched) reported the following. You have no tools in this round, so you cannot fix anything now — restate your complete final answer: first your summary of what you changed and why, then, for each finding, state it and what would fix it (a short fix plan the user or a follow-up turn can act on). Lint findings left unfixed are findings the reviewer will see: " + receipt
}

// runTurnLint runs the turn-end lint pass for the turn's touched files and
// returns its receipt ("lint: …" or ""), applying the gates in order:
// mode "all" (the effective mode — the configured ceiling folded with a
// /hook lowering), workspace trust, a lint command with a target
// ({file}/{dir}), at least one applicable touched file, and the turn's
// total lint budget (project.turn_lint_budget_sec, 0 = the default 60s —
// runTurnLint passes it through and RunTurnEndLint converts it to a
// deadline that cuts off a run in progress, not just unstarted ones). It
// never fails: every degradation is folded into the receipt or silently
// skipped, the per-edit hook's contract.
func (cs *CortexSession) runTurnLint(ctx context.Context) string {
	if tools.EffectiveHookMode(cs.hookState) != tools.HookModeAll {
		return "" // "format" and "off" skip the turn-end lint
	}
	if !cs.WorkspaceTrusted() {
		return "" // trust is the hard gate — untrusted runs nothing
	}
	files := cs.turnLinter.touched
	// budgetSec 0 = "not armed by NewCortexSession" (hand-built test
	// sessions): pass it through and let RunTurnEndLint fall back to the
	// default total budget.
	receipt := tools.RunTurnEndLint(ctx, cs.projectCommands, cs.Workdir(), files, true, time.Duration(cs.turnLinter.budgetSec)*time.Second)
	cs.lintReceipt = receipt
	return receipt
}
