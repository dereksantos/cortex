// plan_mode.go — issue #150: a plan-then-execute path for multi-part tasks.
//
// A single planning turn (tools withheld) is asked to produce a short,
// ordered list of steps; each step then runs as its OWN Turn on the same
// session, with the project's own checks run between steps. Running each
// step as a separate turn — rather than one long tool loop — is the point:
// it gives the context demotion of #131 a place to bite at every boundary
// (a turn is the only point a message can be folded into the outline), and
// it mirrors the step mode that proved out in the self-dev loop's run.sh
// (#137 / #141) instead of the one 180-call turn of #94 that dropped
// requirements.
//
// The checks come from the #129 project-commands idea — run the project's
// own test/build command after each step so a broken step is caught before
// the next one builds on it — implemented here as a self-contained
// discoverCheck (issue #129's projectcmd package is not yet in this branch,
// so we discover the same way it does: go.mod → go test ./...; package.json
// "test" script → npm test). Where no command can be found the check is
// skipped with a note rather than failing the step: a project without a
// recognizable toolchain still gets the plan-then-execute behavior. A check
// that times out is reported as a skip (never a failure) for the same
// reason: a check that didn't finish tells us nothing about the step. The
// check is also run once, as a BASELINE, before step 1; if the project's
// suite was already failing (or didn't finish) before the plan started, the
// between-step gate is disabled for the whole run and each step instead
// carries a short note explaining why no check ran — so a slow or already-
// broken suite can never mark step 1 failed and no-op the rest.
//
// A failed step (the step's turn errored, or the post-step check failed)
// stops the run and every later step is reported as not reached. A CANCELLED
// context (Ctrl-C / ESC mid-step) is treated as an interrupt rather than a
// step failure: the run returns the per-step report so far together with a
// context.Canceled error, so a caller (the REPL's afterTurn, the headless
// --plan path) can tell an interrupt apart from a genuine failure. The final
// reply is a per-step status list — done / failed / not reached — so a
// reader sees exactly where the run stopped without re-reading the
// transcript.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// planStepCap bounds how many steps a plan may declare (#150's "a small
// cap, e.g. 2–6"). Over the cap the plan is TRUNCATED, not rejected: the
// model often appends a trailing "done / wrap up" line that should never
// become a full turn, and an over-long plan is still a plan we can act on
// for the first N steps.
const planStepCap = 6

// planStepFloor is the minimum number of ordered steps that count as a real
// plan. A single "step" is just a one-shot task — there is no point
// planning it — so 0 or 1 parsed steps falls back to one plain turn doing
// the whole thing (#150: "unparseable, which falls back to a single turn").
const planStepFloor = 2

// noReproMarker is the output-shape convention a no-repro step reply must
// follow (issue #178). The principle (prompt.go's verifyBeforeFixPrinciple)
// says a reported problem that doesn't reproduce is finished by SAYING so
// with the evidence — but it does not name the words to say. This marker is
// those words: each step prompt (planStepPrompt) tells the model to lead such
// a reply with "Not reproduced:" + the evidence, and noReproNote anchors on
// the same words. The principle tells the model WHAT to conclude; this
// convention tells it HOW to write that conclusion down so the note survives
// into the per-step report and the later steps' prompts — one phrase, two
// places (each step prompt and the probe), no procedure.
//
// The marker belongs only in the step prompts, NOT in the planning
// instruction: the planning turn has no "step", cannot run tools, and its
// output-shape rules ("nothing before the list") contradict a reply led by
// "Not reproduced:" — a model that followed the marker there would produce a
// plan parsePlan cannot parse, and the run would silently fall back to a
// single turn. The planning prompt's verifyBeforeFixPrinciple line is enough
// to shape the plan; the output convention only means something on a step
// turn that has tools.
const noReproMarker = `If this step's outcome is that the reported problem does not reproduce, begin your reply with "Not reproduced:" followed by the evidence (the command you ran and what it showed).`

// planModeInstruction is the planning turn's prompt. It fixes the output
// shape parsePlan relies on: bare "N. text" lines, one per step, 2–6 of
// them. It asks for no tool use and no prose beyond the list so the reply
// stays mechanically parseable.
//
// The verify-before-fix principle (issue #178) restates prompt.go's
// verifyBeforeFixPrinciple — the same text every other turn gets in the base
// system prompt — as a single principle, not a task-shaped procedure:
// prompts state principles, and the procedure a model draws from them is its
// own. The planning turn is one model call, so it never reads its own
// earlier output; restating the principle in the prompt (rather than relying
// on the system prompt alone) keeps the plan shaped around observable bugs
// while the output-shape rules above stay the only other instruction in the
// turn.
const planModeInstruction = `You are planning a multi-part task. First produce ONLY a plan, then I will execute each step as its own turn.

Respond with a numbered list of steps, one per line, in this exact shape:

1. <first step>
2. <next step>
3. <...>

Rules:
- Give 2 to 6 steps (a small, ordered list — no more than 6).
- One line per step, starting at 1; nothing before the list, nothing after.
- No prose, no headings, no bullet markers — only "N. step" lines.
- Do not use any tools; just output the numbered list.
- ` + verifyBeforeFixPrinciple + ``

// planStepLineRe matches one ordered step: a line whose leading "N. " (a
// number, a dot, then at least one space) is followed by step text. The
// leading marker is captured in group 1 and the step text — which may itself
// start with digits or a dot, e.g. "3 new endpoints" or ".gitignore update" —
// in group 2; parsePlan takes group 2 verbatim. Lines that don't start at
// the column — a prose preamble, an indented sub-bullet, a markdown "1."
// with no space, an out-of-order number — are skipped rather than
// mis-parsed as a step.
var planStepLineRe = regexp.MustCompile(`^(\d+)\.\s+(\S.*)$`)

// parsePlan extracts the ordered step list from a planning turn's reply.
// It returns the trimmed step texts in order, capped at planStepCap. A
// reply with fewer than planStepFloor ordered lines yields 0 steps — the
// caller treats that as "no plan" and falls back to a single plain turn.
func parsePlan(reply string) []string {
	var steps []string
	for _, line := range strings.Split(reply, "\n") {
		trimmed := strings.TrimSpace(line)
		m := planStepLineRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[2])
		if text == "" {
			continue
		}
		steps = append(steps, text)
		if len(steps) >= planStepCap {
			break
		}
	}
	if len(steps) < planStepFloor {
		return nil
	}
	return steps
}

// stepStatus is one step's outcome in the final per-step report.
type stepStatus int

const (
	stepDone       stepStatus = iota // the step's turn completed AND its check passed (or had none)
	stepFailed                       // the step's turn errored, or its post-step check failed
	stepNotReached                   // a later step skipped because an earlier step failed
)

func (s stepStatus) String() string {
	switch s {
	case stepDone:
		return "done"
	case stepFailed:
		return "failed"
	default:
		return "not reached"
	}
}

// StepResult is one planned step's outcome.
type StepResult struct {
	Step   string     // the step text as planned
	Status stepStatus // done / failed / not reached
	Note   string     // a short detail: the check's output, or the turn's error
}

// PlanRunResult is the whole plan-then-execute run. A non-nil error from
// TurnWithPlan means the run itself failed (the planning turn could not run,
// or the fallback turn failed); the per-step report below is empty in that
// case.
type PlanRunResult struct {
	Planned bool // true when a real plan was produced; false for the single-turn fallback
	Steps   []StepResult
	Reply   string // the final per-step report (or the fallback turn's reply)
	// TestReceipt carries every executed turn's "tests changed" receipt
	// (issue #141's TurnResult.TestReceipt), one per line in run order, so
	// a plan run surfaces test loss to the REPL and `cortex turn --plan`
	// exactly as a single turn does. Empty when no turn lost a test.
	TestReceipt string
	// LintReceipt carries every executed turn's "lint: …" receipt (issue
	// #129 piece 3's TurnResult.LintReceipt), one per line in run order, so
	// a plan run surfaces turn-end lint findings to the REPL and
	// `cortex turn --plan` exactly as a single turn does.
	LintReceipt string
	// Receipt carries every executed turn's measurement-only receipt
	// (issue #219's TurnResult.Receipt), one per line in run order, so a
	// plan run surfaces what each turn left in the workspace exactly as a
	// single turn does. Empty when no turn measured anything.
	Receipt string
}

// addReceipt appends one turn's "tests changed" receipt (issue #141) to the
// plan run's, one receipt per line; an empty receipt adds nothing.
func (r *PlanRunResult) addReceipt(receipt string) {
	if receipt == "" {
		return
	}
	if r.TestReceipt != "" {
		r.TestReceipt += "\n"
	}
	r.TestReceipt += receipt
}

// addLintReceipt is addReceipt for the turn-end lint receipts (piece 3).
func (r *PlanRunResult) addLintReceipt(receipt string) {
	if receipt == "" {
		return
	}
	if r.LintReceipt != "" {
		r.LintReceipt += "\n"
	}
	r.LintReceipt += receipt
}

// addTurnReceipt is addReceipt for the measurement-only turn receipts
// (issue #219).
func (r *PlanRunResult) addTurnReceipt(receipt string) {
	if receipt == "" {
		return
	}
	if r.Receipt != "" {
		r.Receipt += "\n"
	}
	r.Receipt += receipt
}

// TurnWithPlan runs the plan-then-execute path for a multi-part task:
//
//  1. One planning turn (tools withheld) asks the model for a 2–6 step list.
//  2. parsePlan turns that reply into an ordered list; 0 steps means the
//     reply wasn't a list, so we fall back to a single plain Turn that does
//     the whole task (the "unparseable" case, #150).
//  3. Each step runs as its OWN Turn on this same session, with the
//     project's checks (runProjectCheck) run in between. A failed step
//     (turn error or check failure) stops the run; later steps are reported
//     as not reached.
//  4. The returned Reply is the deterministic per-step report (renderPlan
//     Report). When the task carries a `- [ ]` checklist (issue #220), the
//     run's checklist account is measured ONCE, deterministically, at the
//     run's END — checklistMissingItems off the run's task and the rendered
//     per-step report (every step's line — step text + note — rides in it, so
//     an item named in its own step's text or note is accounted for even
//     when no step's reply named it). No
//     extra model turn runs for it: the fact lands on
//     PlanRunResult.Receipt (addTurnReceipt) whether or not the steps ran
//     tools. The step prompts themselves carry NO checklist (cs.turn is
//     called with "" for checklistTask — a step that accounted for the
//     whole task's checklist would flag the items other steps own).
//
// attachment (issue #108), when non-empty, is the @path mention attachment
// for the task (see processMentions): it is prepended to the planning turn
// and the single-turn fallback, so the model sees the mentioned file content
// (or outline) — the steps themselves are unaffected (they run on the same
// session, where the attachment is already in context).
//
// Running each step as a separate turn — not one long tool loop — is what
// lets context demotion (#131) act at every boundary.
func (cs *CortexSession) TurnWithPlan(ctx context.Context, task string, attachment ...string) (out PlanRunResult, err error) {
	att := ""
	if len(attachment) > 0 {
		att = attachment[0]
	}
	prepend := func(input string) string {
		if att != "" {
			return att + "\n" + input
		}
		return input
	}
	// Issue #141: every turn this run executes (planning, fallback, each
	// step — failed or interrupted ones included, since Turn returns the
	// receipt on its error path too) contributes its "tests changed"
	// receipt; the deferred stamp puts them on whichever result the run
	// returns, so no return path can drop one.
	var receipts PlanRunResult
	// Issue #220: plan steps run WITHOUT the checklist injection. Each
	// step's input embeds the WHOLE overall task (planStepPrompt — the
	// requirement the step text doesn't restate, #94/#178), so extracting the
	// checklist from it would inject the checklist into every step, and
	// measuring per step would make every step account for the ENTIRE
	// checklist and flag the items other steps own as missing. Instead the
	// checklist is measured ONCE per run — deterministically, in the deferred
	// stamp below, off the run's task and the run's own rendered report (the
	// returned Reply): every step's line — step text + note — rides in it, so
	// an item named in its own step's text or note is accounted for even
	// when no step's reply named it. The deferred stamp covers EVERY return
	// path (a failed or interrupted run measures too — the run most in need
	// of the fact), exactly like the plain-turn rule in turn.go, which
	// measures before the error return.
	stepTurn := func(input string) (TurnResult, error) {
		res, turnErr := cs.turn(ctx, input, "", nil, 0, 0, FinalizeInteractive)
		receipts.addReceipt(res.TestReceipt)
		receipts.addLintReceipt(res.LintReceipt)
		receipts.addTurnReceipt(res.Receipt)
		return res, turnErr
	}
	// reportTurn is the checklist-carrying turn this run uses for the
	// FALLBACK single turn only: the whole task is one turn and its reply is
	// the run's final answer, so its turn injects the checklist into the
	// prompt (checklistTask = task) and measures the checklist fact against
	// the reply exactly like a plain turn. A planned run's checklist is
	// measured deterministically at the run's end instead (see below) — no
	// extra model turn.
	reportTurn := func(input string) (TurnResult, error) {
		res, turnErr := cs.turn(ctx, input, task, nil, 0, 0, FinalizeInteractive)
		receipts.addReceipt(res.TestReceipt)
		receipts.addLintReceipt(res.LintReceipt)
		receipts.addTurnReceipt(res.Receipt)
		return res, turnErr
	}
	defer func() {
		// Issue #220: a PLANNED run's checklist account is measured ONCE per
		// run, here, in the deferred stamp — deterministically, whether or
		// not any turn ran tools, and on EVERY return path (a failed or
		// interrupted run includes: the run most in need of the fact), just
		// like the plain-turn rule in turn.go, which measures before the
		// error return. The reply measured is the run's OWN rendered report
		// (out.Reply, the deterministic per-step report — renderPlanReport
		// emits only the header line and the per-step lines, with no task
		// text): every step's line carries the step text (so an item named in
		// its own step's text is accounted for even when no step's reply or
		// note named it) and the step's note (including an explicit not-done
		// note). An item no step's text or note names is reported missing on
		// PlanRunResult.Receipt through the same joined-receipt surface
		// (addTurnReceipt) as the per-step blocks — measurement only, exactly
		// like the other #219/#220 facts. The fallback (single-turn) run
		// (Planned false) is NOT measured here: its checklist-carrying turn
		// (reportTurn) measures the fact off the reply itself, exactly like a
		// plain turn.
		if out.Planned && len(out.Steps) > 0 {
			if missing := checklistMissingItems(task, out.Reply); len(missing) > 0 {
				receipts.addTurnReceipt(turnReceipt{checklistMissing: missing}.render())
			}
		}
		out.TestReceipt = receipts.TestReceipt
		out.LintReceipt = receipts.LintReceipt
		out.Receipt = receipts.Receipt
	}()

	// --- 1. Planning turn -----------------------------------------------
	// Withhold the session's tools for exactly the planning round-trip so
	// the model can only produce the list (it has nothing to call).
	//
	// runLoop stamps req.Tools = ts.Tools before EVERY send and only
	// restores it on clean exit — the empty final answer (planning's happy
	// path) leaves cs.Request.Tools nil for the REST of the session, and
	// every later coder turn inherits the toolless request. The fix: save
	// the session's OWN filtered list (NewCortexSession's IsToolEnabled
	// filtering, --project shape, allowDelete's remove exclusion), nil it
	// for the planning turn, then restore it explicitly the instant the
	// planning turn returns — before the parsePlan branch, the fallback,
	// or any step — so the steps and every later REPL turn see the tools
	// the user actually enabled. (Config-disabled tools stay withheld
	// because the saved list is the FILTERED one, not the full registry.)
	savedTools := cs.Request.Tools
	cs.Request.Tools = nil
	// The planning turn's prompt embeds the task too ("Task: " + task) — it
	// gets the same no-checklist treatment as the step turns (cs.turn with an
	// empty checklistTask): it produces a plan, not an account.
	planRes, planErr := cs.turn(ctx, prepend(planModeInstruction+"\n\nTask: "+task), "", nil, 0, 0, FinalizeInteractive)
	receipts.addReceipt(planRes.TestReceipt)
	receipts.addLintReceipt(planRes.LintReceipt)
	receipts.addTurnReceipt(planRes.Receipt)
	// Restore the session's own filtered tool list NOW — before any step or
	// fallback turn runs — because runLoop left cs.Request.Tools nil (it
	// stamps req.Tools = ts.Tools = nil on the tool-less planning turn).
	// A deferred restore would only fire at the end of TurnWithPlan, after
	// the steps had already run with no tools.
	cs.Request.Tools = savedTools

	if planErr != nil {
		// The planning turn could not run at all — surface it; there is no
		// plan to execute and (unlike an unparseable reply) no sensible
		// single-turn fallback to retry (the model is not reachable).
		return PlanRunResult{}, fmt.Errorf("failed to run the planning turn: %w", planErr)
	}

	// --- 2. Parse the plan -----------------------------------------------
	steps := parsePlan(planRes.Reply)
	if len(steps) == 0 {
		// The model's reply was not a parseable ordered list (prose, a
		// single "step", or tool-call markup). Fall back to doing the whole
		// task in one plain turn — the pre-step-mode behavior (#150). This
		// is the checklist-carrying turn of the run (reportTurn): the whole
		// task is one turn and its reply is the run's final answer.
		res, err := reportTurn(prepend(task))
		if err != nil {
			return PlanRunResult{Planned: false}, fmt.Errorf("planning reply was not a step list; the fallback single turn failed: %w", err)
		}
		return PlanRunResult{Planned: false, Steps: nil, Reply: res.Reply}, nil
	}

	// --- 3. Baseline check BEFORE step 1 ----------------------------------
	// The between-step check runs the project's FULL suite. If the project's
	// suite already fails (or times out) BEFORE the plan even starts, gating
	// every step on it would mark step 1 failed and no-op the rest even though
	// the steps themselves are fine. So we establish a baseline first: run the
	// check once, and if it isn't clean we run the steps WITHOUT the check
	// gate, attaching a short note to each step explaining why the check was
	// skipped (the pre-existing failure). A clean baseline means the between-
	// step check is meaningful again, so we keep gating.
	checkGated := true
	checkSkipNote := ""
	baseCmd, baseOut, baseOk, baseNote := cs.runProjectCheck(ctx)
	// "Clean" means the command actually RAN and exited 0: cmdLine non-empty
	// and ok. A timed-out (or otherwise skipped) baseline — ok=true but
	// cmdLine=="" — is NOT clean: the baseline tells us nothing about the
	// project's state, so gating on it would stack one full check timeout on
	// top of every step (each step waiting out another 90 s just to be
	// reported). Disarm the gate for the whole run, with a note naming why.
	if !baseOk {
		// The suite was already failing before we did anything. A real check
		// failure from a step is a signal the step itself broke the project,
		// but here nothing has run yet — so the failure is the BASELINE, and
		// gating every step on it would mark step 1 failed and no-op the rest.
		// Disable the gate for the whole run and note why on each step.
		checkGated = false
		checkSkipNote = fmt.Sprintf("check skipped: failing before plan (%s)", truncateNote(baseOut))
	} else if baseCmd == "" {
		// The baseline did not actually run (it timed out, or no command or
		// root could be discovered): it tells us nothing about the project's
		// state, so don't gate the steps on a check that would only time out
		// again. Disarm the gate and carry the baseline's own skip note — it
		// already says why (the timeout's error, the missing command) — on
		// every step instead of a generic one.
		checkGated = false
		checkSkipNote = baseNote
	}
	// --- 4. Execute each step as its own turn ----------------------------
	// earlierNotes carries each DONE step's note in order, so a later step's
	// prompt sees them (planStepPrompt): an earlier no-repro note (issue
	// #178) is the one that matters — it is what keeps a later "fix it" step
	// from running blind to the fact that the reported problem never showed
	// up. Failed/interrupted steps end the run, so only done notes accumulate.
	var earlierNotes []string
	stepResults := make([]StepResult, 0, len(steps))
	for i, step := range steps {
		// Issue #178: every step prompt carries the ORIGINAL task (not just
		// the step line) AND the verify-before-fix principle: demotion at
		// the turn boundaries (#131) can fold the planning turn — the only
		// place the full task text lived — into the outline, and a later step
		// must not run blind to the overall goal or the requirements the step
		// text didn't restate (#94's failure mode). planStepPrompt restates
		// both so each step turn (tools present) carries them, together with
		// the earlier done steps' notes (earlierNotes).
		//
		// Issue #220: the step runs through stepTurn — the checklist is NOT
		// injected into the step's prompt (a step that accounts for the whole
		// task's checklist would flag items other steps own), and the step's
		// turn measures no checklist fact (the run's own deferred stamp
		// measures it once, below).
		stepRes, err := stepTurn(planStepPrompt(task, i+1, len(steps), step, earlierNotes))
		if err != nil {
			// A cancelled context (Ctrl-C / ESC mid-step) is an INTERRUPT, not
			// a step failure: record the step and every later step, return the
			// rendered report, and wrap the cancel error so the caller
			// (errors.Is(err, context.Canceled)) can tell an interrupt apart
			// from a genuine step failure.
			if errors.Is(err, context.Canceled) {
				return interruptPlan(stepResults, steps, i, err)
			}
			stepResults = append(stepResults, StepResult{Step: step, Status: stepFailed, Note: err.Error()})
			for _, later := range steps[i+1:] {
				stepResults = append(stepResults, StepResult{Step: later, Status: stepNotReached})
			}
			// A genuine turn error (the model was unreachable, the turn blew a
			// budget, …) is surfaced to the caller in ADDITION to the per-step
			// report: the report tells the reader where it stopped, and the
			// error keeps the exit code non-zero for a headless driver.
			return PlanRunResult{Planned: true, Steps: stepResults, Reply: renderPlanReport(stepResults, len(steps))},
				fmt.Errorf("plan step %d failed: %w", i+1, err)
		}
		// Run the project's own checks after a successful step — but only when
		// the baseline said the suite was clean, so we don't blame a step for
		// a failure that pre-dated the plan (see the baseline above).
		//
		// The no-repro note is evaluated BEFORE the check branch, in BOTH of
		// them (issue #178): a failing baseline is exactly the state a
		// reported bug usually arrives in, and the step's own evidence that
		// the bug does not reproduce must not be thrown away just because no
		// check can gate the run.
		reproNote := noReproNote(stepRes.Reply)
		if checkGated {
			cmdLine, _, ok, note := cs.runProjectCheck(ctx)
			// A cancelled context (Ctrl-C) DURING the check is an INTERRUPT,
			// not a step outcome: the check's context was derived from the
			// run's, so it can report a timeout-skip (ok=true) even though the
			// parent was cancelled. Treat it the same as a step that was
			// cancelled — never mark the step done on a check that didn't run
			// to completion.
			if errors.Is(ctx.Err(), context.Canceled) {
				return interruptPlan(stepResults, steps, i, ctx.Err())
			}
			if !ok {
				// A real check failure means the step left the project broken —
				// stop and don't let the next step build on it.
				stepResults = append(stepResults, StepResult{Step: step, Status: stepFailed, Note: note})
				for _, later := range steps[i+1:] {
					stepResults = append(stepResults, StepResult{Step: later, Status: stepNotReached})
				}
				return PlanRunResult{Planned: true, Steps: stepResults, Reply: renderPlanReport(stepResults, len(steps))},
					fmt.Errorf("plan step %d failed its check: %s", i+1, note)
			}
			// On success the note is a SHORT fixed summary naming the command —
			// not the raw multi-line test output, which would turn every done
			// step's report line into a wall of "ok  pkg 0.3s" lines. Raw
			// output is kept only for failures (the note above). A SKIP (no
			// command discovered, an unresolvable root, or a timeout) is
			// reported as its own note verbatim — those notes already carry
			// the "check skipped: " prefix — and must never be rendered as a
			// "check passed" with a command the user believes ran.
			//
			// A no-repro verification (issue #178): the step's job was to
			// confirm a reported problem and it could not — its reply is the
			// evidence. Keep that note on the step so the report line says
			// what happened, not a bare "done" (nor a "check passed" that
			// would bury the verification outcome). The step is DONE, not
			// failed: nothing broke and no speculative fix was built.
			if reproNote != "" {
				stepResults = append(stepResults, StepResult{Step: step, Status: stepDone, Note: reproNote})
				earlierNotes = append(earlierNotes, reproNote)
				continue
			}
			sr := StepResult{Step: step, Status: stepDone, Note: note}
			if cmdLine != "" {
				// The command actually ran and exited 0: name it.
				sr.Note = "check passed (" + cmdLine + ")"
			}
			stepResults = append(stepResults, sr)
			earlierNotes = append(earlierNotes, sr.Note)
		} else {
			// The baseline failed: run the step but don't gate it. Attach the
			// baseline-skip note so the reader sees why no check ran — UNLESS
			// the step's reply is a no-repro verdict, which carries its own
			// evidence instead (see the reproNote check above).
			note := checkSkipNote
			if reproNote != "" {
				note = reproNote
			}
			stepResults = append(stepResults, StepResult{Step: step, Status: stepDone, Note: note})
			earlierNotes = append(earlierNotes, note)
		}
	}

	// --- 5. Final report ----------------------------------------------------
	// The run's answer is the DETERMINISTIC per-step report: no model turn
	// runs for it. (A model report turn would add a full tools-enabled turn
	// AFTER the per-step project checks — a turn that could edit files
	// unchecked — and would replace renderPlanReport's deterministic reply
	// with model text, costing an extra model call on every checklist plan
	// run.) The checklist measurement rides the deferred stamp above, so the
	// run measures it once, at the run's end, on every return path — this
	// return included.
	return PlanRunResult{Planned: true, Steps: stepResults, Reply: renderPlanReport(stepResults, len(steps))}, nil
}

// interruptPlan records the interrupted step (failed, with an 'interrupted'
// note) and every later step as not reached, renders the per-step report, and
// wraps the cancel error as an interrupt so the caller (the REPL's afterTurn,
// the headless --plan path) can tell an interrupt apart from a genuine step
// failure via errors.Is(err, context.Canceled).
func interruptPlan(stepResults []StepResult, steps []string, i int, err error) (PlanRunResult, error) {
	stepResults = append(stepResults, StepResult{Step: steps[i], Status: stepFailed, Note: "interrupted: " + err.Error()})
	for _, later := range steps[i+1:] {
		stepResults = append(stepResults, StepResult{Step: later, Status: stepNotReached})
	}
	return PlanRunResult{Planned: true, Steps: stepResults, Reply: renderPlanReport(stepResults, len(steps))},
		fmt.Errorf("plan interrupted at step %d: %w", i+1, err)
}

// noReproNote is a step's own verification note when its reply reports that
// the reported problem does NOT reproduce (issue #178). A non-empty result
// means "keep this note on the step" — it is the evidence the step's report
// line carries instead of a bare "done" (or a "check passed" that would bury
// the verification outcome).
//
// The probe is ANCHORED to the verdict the convention points at: a line that
// starts with "not reproduced" (case-insensitive), the phrasing noReproMarker
// (in each step prompt) tells the model to lead a no-repro reply with — so a real model's verdict, whatever the evidence,
// is recognized. Anchoring to the line start (rather than any substring
// match on "not reproduce") keeps a reply that merely ECHOES prompt or plan
// wording — "if the bug does not reproduce …", "I could not reproduce it at
// first, then reproduced it with -race" — from being misread as a no-repro
// verdict. A no-repro reply that states its verdict some other way (the
// model skipped the convention) is simply not flagged: the report line then
// falls back to the check summary, which is honest about what was verified.
func noReproNote(reply string) string {
	trimmed := strings.TrimSpace(reply)
	if trimmed == "" {
		return ""
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "not reproduced") {
			return truncateNote(trimmed)
		}
	}
	return ""
}

// planStepPrompt builds the prompt for ONE planned step's turn (issue #178).
// It restates BOTH the overall task and the verify-before-fix principle
// (prompt.go's verifyBeforeFixPrinciple, already in the base system prompt),
// so each step turn (tools present) carries them even after demotion folds
// the planning turn into the outline (#131 / #94's failure mode). The
// step's tools are present here (unlike the planning turn), so the model can
// and should run the test or command that confirms the problem.
//
// It also carries the no-repro output-shape convention (noReproMarker): the
// principle says a non-reproducing problem is finished by saying so with the
// evidence, but doesn't name the words — the marker tells the model to lead
// such a reply with "Not reproduced:" so noReproNote recognizes the verdict
// and carries it into the report and the later steps' prompts.
//
// And it restates the review-feedback principle (reviewFeedbackPrinciple,
// issue #162): a step turn is where a review round's findings actually get
// applied, and demotion at the turn boundaries (#131) can fold the review
// itself out of the window — leaving the step with a paraphrase of one or two
// findings and no obligation to account for the rest. That is how PR #158's
// placeholder survived two rounds and PR #145 applied both offered
// alternatives. The step prompt is the last place those findings can be
// pinned, so the standing principle rides with every step (same const the
// base system prompt carries).
//
// And it restates the locate-before-writing principle
// (locateBeforeWritingPrinciple, issue #224): a step turn is where new code —
// mostly test files — actually gets written, and turn-boundary demotion can
// fold the sessions' earlier grep/outline work out of the window, leaving the
// step to write against helpers, type shapes and paths it assumes exist. The
// tick 20261006T074738Z reviews are the case: one step wrote seven undefined
// identifiers at once and another carried a type-shape compile error from the
// previous session's step. The standing principle (same const the base system
// prompt carries) rides with every step so the locate-before-writing half of
// the guidance survives demotion.
//
// earlierNotes are the DONE steps' notes in order (skipped when empty): an
// earlier step's no-repro note (issue #178) must reach a later step, so a
// "fix it" step knows the bug never reproduced instead of running blind and
// building a speculative fix. Done-only notes are carried too: they are the
// outcome a later step builds on.
func planStepPrompt(task string, i, total int, step string, earlierNotes []string) string {
	p := fmt.Sprintf(
		"Overall task: %s\n\nPlan step %d of %d: %s\n\n%s\n\n%s\n\n%s\n\n%s\n\n%s",
		task, i, total, step, verifyBeforeFixPrinciple, blockedCheckPrinciple, reviewFeedbackPrinciple, locateBeforeWritingPrinciple, noReproMarker,
	)
	if len(earlierNotes) > 0 {
		p += "\n\nEarlier steps:" + notesList(earlierNotes)
	}
	return p
}

// notesList renders earlier steps' notes as numbered lines — "1. note" — so
// a step's prompt can carry them without prose a model must parse around.
func notesList(notes []string) string {
	var b strings.Builder
	for i, n := range notes {
		fmt.Fprintf(&b, "\n%d. %s", i+1, n)
	}
	return b.String()
}

// truncateNote bounds a raw note (check output) embedded in a skip note, so a
// chatty test run can't bloat the final report or the stored transcript.
func truncateNote(s string) string {
	if len(s) > planCheckNoteCap {
		return s[:planCheckNoteCap] + "…"
	}
	return s
}

// renderPlanReport renders the final per-step answer: a summary header
// (done/total, plus "stopped" when a step failed) then one line per step —
// "done"/"failed"/"not reached" — with a note where there is one.
func renderPlanReport(steps []StepResult, total int) string {
	done, failed := 0, 0
	for _, s := range steps {
		switch s.Status {
		case stepDone:
			done++
		case stepFailed:
			failed++
		}
	}

	status := "completed"
	if failed > 0 {
		status = "stopped"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Plan-then-execute (%s): %d/%d steps done", status, done, total)
	for i, s := range steps {
		line := fmt.Sprintf("%d. [%s] %s", i+1, s.Status, s.Step)
		if s.Note != "" {
			line += " — " + s.Note
		}
		b.WriteString("\n")
		b.WriteString(line)
	}
	return b.String()
}

// runProjectCheckStub, when non-nil, replaces runProjectCheck's body — the
// seam tests use to force a step's check to fail (or pass) without running a
// real go test/npm test in the test's working directory. Nil in production.
// It mirrors runProjectCheck's return signature: (cmdLine, out, ok, note).
var runProjectCheckStub func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string)

// runProjectCheck runs the project's own test/build command, mirroring
// #129's "run the project's checks after edits". It returns (cmdLine, out,
// ok, note):
//
//   - cmdLine: the discovered command ("go test ./..." / "npm test"), or
//     "" when nothing could be discovered. The caller uses it to name the
//     command in a step's done-note.
//   - ok: false ONLY when the check FAILED (a real non-zero exit). A missing
//     command, a project root that could not be resolved, and a TIMEOUT are
//     all reported as ok=true with a "check skipped: …" note instead: a check
//     that can't run (or didn't finish in time) tells us nothing about the
//     step, so it must not mark the step failed. Every skip also returns
//     cmdLine="" — a skip never names a command, so the caller can tell
//     "ran and passed" (cmdLine set) apart from "skipped" (cmdLine empty) and
//     report each correctly.
//   - out: the check's trimmed combined output (or the command line when the
//     output was empty), for the failure note.
//   - note: a short human summary — the check's trimmed output on success,
//     the failure detail on failure, or "check skipped: <why>".
//
// A missing command is a SKIP, never a failure: a project with no
// recognizable toolchain still gets plan-then-execute.
func (cs *CortexSession) runProjectCheck(ctx context.Context) (cmdLine, out string, ok bool, note string) {
	if runProjectCheckStub != nil {
		return runProjectCheckStub(cs, ctx)
	}
	root, ok := cs.checkRoot()
	if !ok {
		return "", "", true, "check skipped: no project root"
	}
	cmdLine, discovered := discoverCheck(root)
	if !discovered {
		return "", "", true, "check skipped: no test/build command found for this project"
	}

	checkCtx, cancel := context.WithTimeout(ctx, planCheckTimeout)
	defer cancel()
	c := exec.CommandContext(checkCtx, "sh", "-c", cmdLine)
	c.Dir = root
	outBytes, err := c.CombinedOutput()
	out = strings.TrimSpace(string(outBytes))
	if out == "" {
		out = cmdLine
	}
	// A timeout (or any ctx cancellation) means the check didn't finish — we
	// can't tell a real failure from a slow suite — so report it as a SKIP
	// (ok=true) with a note rather than failing the step. A step must not be
	// blamed for a check that hung or was interrupted.
	if checkCtx.Err() != nil {
		return "", out, true, fmt.Sprintf("check skipped: %s (%s)", cmdLine, checkCtx.Err())
	}
	if err != nil {
		if len(out) > planCheckNoteCap {
			out = out[:planCheckNoteCap] + "…"
		}
		return cmdLine, out, false, "check failed: " + out
	}
	return cmdLine, out, true, out
}

// checkRoot resolves the directory to run checks in: an explicit workspace
// (--project / serve / loop firing) uses its own root; a CWD-derived
// workspace falls back to the current working directory.
func (cs *CortexSession) checkRoot() (string, bool) {
	if cs.workspace != nil && cs.workspace.Root != "" {
		return cs.workspace.Root, true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return cwd, true
}

// planCheckTimeout caps a single post-step check at a hard deadline. It does
// NOT promise the check is a "quick sanity run" — it runs the project's FULL
// test command ("go test ./..." or "npm test") discovered by discoverCheck,
// so a slow suite can legitimately approach this ceiling. It only bounds how
// long one check may run; when a check exceeds it we report the check as
// SKIPPED (not failed), so a slow suite can never block the plan run.
const planCheckTimeout = 90 * time.Second

// planCheckNoteCap bounds how much check output a step note carries, so a
// chatty test run can't bloat the final report or the stored transcript.
const planCheckNoteCap = 400

// discoverCheck finds the project's own test command (#129's convention):
// a go.mod means it's a Go project → "go test ./..."; a package.json with a
// "test" script → "npm test". Returns ("", false) when no recognized
// manifest supplies a check, in which case the caller skips rather than
// fails.
func discoverCheck(root string) (string, bool) {
	if fileExists(filepath.Join(root, "go.mod")) {
		return "go test ./...", true
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if t := strings.TrimSpace(pkg.Scripts["test"]); t != "" {
				return "npm test", true
			}
		}
	}
	return "", false
}

// fileExists reports whether path exists (any type); a helper so
// discoverCheck's go.mod probe reads as intent rather than a Stat/IsExist
// idiom.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
