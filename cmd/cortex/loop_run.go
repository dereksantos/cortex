// loop_run.go — M6.3 (GOAL.md §6, docs/cortex-web.md Phase 6): the real
// firing machinery internal/loops.Scheduler.Due (M6.2) decides to invoke.
// Reuses the Phase 3/4 seams rather than inventing a parallel mechanism
// (GOAL.md §1 pillar 3): applyProjectByName (project_workspace.go, M3.5)
// resolves the spec's project and targets a fresh headless session at its
// root exactly like SessionManager.Create (serve_session.go, M4.2b1) does;
// session.Turn (turn.go) runs the prompt; the dir-scoped change lifecycle
// (startChangeIn/commitChangeIn, change.go) lands any resulting edits as a
// reviewable `cortex change` scoped to the project root, per Phase 6's
// "each run lands as a reviewable cortex change" (auto-merge is explicitly
// out of scope — the branch is left for a human to review, matching
// change.go's existing no-push posture).
package main

import (
	"context"
	"fmt"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

// RunLoopFiring runs one due loop.Spec firing to completion and always
// appends exactly one loop.run journal event (journal.AppendLoopRun) — on
// every exit path, including a project that fails to resolve or a turn
// that errors — so a firing that goes wrong is still visible in run
// history instead of silently vanishing. The returned error carries only a
// failure to WRITE that journal event (an infra problem the caller, the
// scheduler's driving loop, should surface); a failed or errored RUN is
// itself a normal, successfully-journaled outcome and returns nil.
//
// newSession is the sessionFactory seam (serve_session.go): production
// wires newProductionSession (real config/backend resolution, matching
// runTurnCLI/SessionManager.Create); tests inject a hermetic session
// pointed at a scripted httptest backend.
func RunLoopFiring(ctx context.Context, spec loops.Spec, reg registry.Registry, newSession sessionFactory) error {
	payload := journal.LoopRunPayload{Name: spec.Name, Project: spec.Project}

	proj, err := reg.Lookup(spec.Project)
	if err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("resolve project %q: %v", spec.Project, err)
		return journal.AppendLoopRun(payload)
	}

	cs := newSession()
	if err := applyProjectByName(cs, reg, spec.Project); err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("apply project: %v", err)
		return journal.AppendLoopRun(payload)
	}
	cs.StartTranscript()
	defer cs.Close()

	// Best-effort: land the firing's edits on their own change branch,
	// scoped to the project root (not the process CWD). A dirty project
	// tree (e.g. a change already in flight) means no fresh branch can
	// start; the turn still runs, it just can't be captured as a change —
	// ChangeRef stays empty rather than the firing being refused outright.
	branch, startErr := startChangeIn(proj.Root, spec.Name)

	// D11's per-run budget caps (M6.4): spec.MaxTurns/MaxTokens (0 = the
	// engine's normal defaults) bound the turn's tool-call iterations and
	// cumulative token spend respectively. TurnWithBudget surfaces which
	// bound (if any) forced the stop via TurnResult.StopReason — a
	// bound-forced stop is not a Go error (the engine still finalizes and
	// answers), so it must be detected here, not via turnErr.
	result, turnErr := cs.TurnWithBudget(ctx, spec.Prompt, spec.MaxTurns, spec.MaxTokens)
	if turnErr != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("turn: %v", turnErr)
		return journal.AppendLoopRun(payload)
	}
	if result.StopReason == "max-iter" || result.StopReason == "token-budget" {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = "budget"
		return journal.AppendLoopRun(payload)
	}

	if startErr == nil {
		if clean, cleanErr := gitCleanIn(proj.Root); cleanErr == nil && !clean {
			if head, commitErr := commitChangeIn(proj.Root, fmt.Sprintf("loop: %s", spec.Name)); commitErr == nil {
				payload.ChangeRef = branch + "@" + head
			}
		}
	}

	payload.Outcome = journal.LoopOutcomeSuccess
	return journal.AppendLoopRun(payload)
}
