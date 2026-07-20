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
	"regexp"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

// loopSelfPacingInstruction is appended to every loop firing's prompt
// (D11's self-pacing tuning, borrowed from Claude Code's /loop skill): it
// states the capability as a principle, not a recipe — the model decides
// whether and how to use it. It leads with "this prompt recurs on a
// schedule" because a bare "you may end with NEXT or DONE" reads, to a
// small model, as describing a one-shot prompt: nothing tells it the SAME
// prompt fires again later, so a firing that finished its visible work for
// THIS turn can reasonably (and wrongly) answer DONE even though the loop's
// standing purpose is ongoing — observed live as a firing ending "DONE —
// this was a read-only summarization request...". DONE means the LOOP is
// permanently finished and must never fire again, not that this reply is
// done.
const loopSelfPacingInstruction = "This prompt recurs on a schedule, so finishing one firing does not mean the loop is done. End your final reply's last line with `NEXT: <n>m — <reason>` to adjust the next firing, or `DONE — <reason>` only if the loop's standing purpose is permanently complete and it should never fire again."

// maxNextMinutes is the self-pacing NEXT marker's clamp ceiling (24h) —
// paired with loops.CadenceFloorMinutes as the clamp floor.
const maxNextMinutes = 24 * 60

var (
	loopDoneMarkerRe = regexp.MustCompile(`(?i)^DONE\s*(?:—|-)\s*(.+)$`)
	loopNextMarkerRe = regexp.MustCompile(`(?i)^NEXT:\s*(\d+)\s*m\s*(?:—|-)\s*(.+)$`)
)

// loopMarker is a parsed NEXT/DONE self-pacing marker off a loop firing's
// final reply.
type loopMarker struct {
	Done        bool
	NextMinutes int
	Reason      string
}

// parseLoopMarker looks for a NEXT/DONE self-pacing marker on reply's
// tail — the last non-empty line, whitespace-trimmed. A malformed or
// absent marker reports ok=false rather than an error: a model that
// forgets or garbles the marker just falls back to the spec's own cadence
// (Scheduler.Due picks that up automatically since NextMinutes stays 0),
// it never fails the firing. A NEXT marker's minutes are clamped to
// [loops.CadenceFloorMinutes, maxNextMinutes].
func parseLoopMarker(reply string) (loopMarker, bool) {
	lines := strings.Split(reply, "\n")
	var tail string
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			tail = t
			break
		}
	}
	if tail == "" {
		return loopMarker{}, false
	}
	if m := loopDoneMarkerRe.FindStringSubmatch(tail); m != nil {
		return loopMarker{Done: true, Reason: strings.TrimSpace(m[1])}, true
	}
	if m := loopNextMarkerRe.FindStringSubmatch(tail); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return loopMarker{}, false
		}
		if n < loops.CadenceFloorMinutes {
			n = loops.CadenceFloorMinutes
		} else if n > maxNextMinutes {
			n = maxNextMinutes
		}
		return loopMarker{NextMinutes: n, Reason: strings.TrimSpace(m[2])}, true
	}
	return loopMarker{}, false
}

// RunLoopFiring runs one due loop.Spec firing to completion and always
// appends exactly one loop.run journal event (journal.AppendLoopRun) — on
// every exit path, including a project that fails to resolve or a turn
// that errors — so a firing that goes wrong is still visible in run
// history instead of silently vanishing. The returned error carries only a
// failure to WRITE that journal event (an infra problem the caller, the
// scheduler's driving loop, should surface); a failed or errored RUN is
// itself a normal, successfully-journaled outcome and returns nil.
//
// store persists the D11 terminal states finalizeLoopFiring derives from
// the outcome (a DONE marker, or a third consecutive failure) — may be nil
// where a caller doesn't care about that half (it just skips it).
//
// newSession is the sessionFactory seam (serve_session.go): production
// wires newProductionSession (real config/backend resolution, matching
// runTurnCLI/SessionManager.Create); tests inject a hermetic session
// pointed at a scripted httptest backend.
func RunLoopFiring(ctx context.Context, spec loops.Spec, reg registry.Registry, store loops.Store, newSession sessionFactory) error {
	// docs/learning-loop.md: a kind:"learn" spec runs the Learn subagent over
	// the project's journal instead of a coder Turn — same scheduler, cadence
	// floor, and three-strike disable (learn.go's runLearnFiring reuses
	// finalizeLoopFiring below), different firing engine. Scope is
	// orthogonal to Kind (docs/cross-source-learning.md piece 1's promotion
	// half): a kind:"learn" spec additionally carrying scope:"user" runs the
	// machine-wide LearnUser promotion pass instead of the per-project Learn
	// pass — same journaled-outcome contract, different firing engine again
	// (learn_user.go's runLearnUserFiring).
	if spec.Kind == loops.KindLearn {
		if spec.Scope == loops.ScopeUser {
			return runLearnUserFiring(ctx, spec, reg, store, newSession)
		}
		return runLearnFiring(ctx, spec, reg, store, newSession)
	}

	payload := journal.LoopRunPayload{Name: spec.Name, Project: spec.Project}

	proj, err := reg.Lookup(spec.Project)
	if err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("resolve project %q: %v", spec.Project, err)
		return finalizeLoopFiring(spec, store, payload, 0)
	}

	cs := newSession()
	strikes := cs.Config.loopAutoDisableStrikes()
	if err := applyProjectByName(cs, reg, spec.Project); err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("apply project: %v", err)
		return finalizeLoopFiring(spec, store, payload, strikes)
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
	prompt := spec.Prompt + "\n\n" + loopSelfPacingInstruction
	result, turnErr := cs.TurnWithBudget(ctx, prompt, spec.MaxTurns, spec.MaxTokens)
	if turnErr != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("turn: %v", turnErr)
		return finalizeLoopFiring(spec, store, payload, strikes)
	}
	if result.StopReason == "max-iter" || result.StopReason == "token-budget" {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = "budget"
		return finalizeLoopFiring(spec, store, payload, strikes)
	}

	if startErr == nil {
		if clean, cleanErr := gitCleanIn(proj.Root); cleanErr == nil && !clean {
			if head, commitErr := commitChangeIn(proj.Root, fmt.Sprintf("loop: %s", spec.Name)); commitErr == nil {
				payload.ChangeRef = branch + "@" + head
			}
		}
	}

	payload.Outcome = journal.LoopOutcomeSuccess
	// D11 self-pacing: a clean finish's own final reply may carry a
	// NEXT/DONE marker (loopSelfPacingInstruction). Only checked on the
	// success path — a turn that errored or hit a budget cap never reaches
	// here, so there's no organically-produced reply to trust a marker
	// from.
	if marker, ok := parseLoopMarker(result.Reply); ok {
		payload.NextReason = marker.Reason
		if marker.Done {
			payload.Done = true
		} else {
			payload.NextMinutes = marker.NextMinutes
		}
	}
	return finalizeLoopFiring(spec, store, payload, strikes)
}

// defaultLoopAutoDisableStrikes is D11's original tuning: 3 consecutive
// failed firings auto-disable a loop. Config-overridable via
// serve.loop_auto_disable_strikes (Config.loopAutoDisableStrikes).
const defaultLoopAutoDisableStrikes = 3

// finalizeLoopFiring appends payload as the firing's loop.run journal
// event, then applies whatever terminal state the outcome implies: a DONE
// marker or `strikes` consecutive "failed" outcomes in a row
// (loops.ConsecutiveFailures, D11) disables the spec in store so the
// scheduler stops driving it — both journaled by the very event
// finalizeLoopFiring just wrote, never a second invented one. store may be
// nil (some callers don't care about terminal-state persistence); a nil
// store just skips that half. strikes <= 0 falls back to
// defaultLoopAutoDisableStrikes (the RunLoopFiring call sites before a
// session — and therefore a Config — exists pass 0).
func finalizeLoopFiring(spec loops.Spec, store loops.Store, payload journal.LoopRunPayload, strikes int) error {
	if strikes <= 0 {
		strikes = defaultLoopAutoDisableStrikes
	}
	if err := journal.AppendLoopRun(payload); err != nil {
		return fmt.Errorf("failed to journal loop.run for %q: %w", spec.Name, err)
	}
	if store == nil {
		return nil
	}
	switch {
	case payload.Done:
		disableLoop(store, spec.Name, "done: "+payload.NextReason)
	case payload.Outcome == journal.LoopOutcomeFailed:
		if n, err := loops.ConsecutiveFailures(spec.Name); err == nil && n >= strikes {
			disableLoop(store, spec.Name, fmt.Sprintf("%d consecutive failures", strikes))
		}
	}
	return nil
}

// disableLoop flips the named spec's Enabled to false and stamps
// DisabledReason, via a fresh Store.Lookup so any other field a concurrent
// writer (the web UI's create/edit) changed survives the rewrite — mirrors
// handleSetLoopEnabled's lookup-flip-save shape (serve_loops.go). Best
// effort: a lookup or save failure here is not surfaced as a
// RunLoopFiring error (the firing itself already succeeded and is already
// journaled) — it just leaves the loop enabled for the scheduler to
// reconsider next tick.
func disableLoop(store loops.Store, name, reason string) {
	spec, err := store.Lookup(name)
	if err != nil {
		return
	}
	spec.Enabled = false
	spec.DisabledReason = reason
	_ = store.Save(spec)
}
