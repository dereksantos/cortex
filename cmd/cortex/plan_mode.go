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
// recognizable toolchain still gets the plan-then-execute behavior.
//
// A failed step (the step's turn errored, or the post-step check failed)
// stops the run and every later step is reported as not reached. The final
// reply is a per-step status list — done / failed / not reached — so a
// reader sees exactly where the run stopped without re-reading the
// transcript.
package main

import (
	"context"
	"encoding/json"
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

// planModeInstruction is the planning turn's prompt. It fixes the output
// shape parsePlan relies on: bare "N. text" lines, one per step, 2–6 of
// them. It asks for no tool use and no prose beyond the list so the reply
// stays mechanically parseable.
const planModeInstruction = `You are planning a multi-part task. First produce ONLY a plan, then I will execute each step as its own turn.

Respond with a numbered list of steps, one per line, in this exact shape:

1. <first step>
2. <next step>
3. <...>

Rules:
- Give 2 to 6 steps (a small, ordered list — no more than 6).
- One line per step, starting at 1; nothing before the list, nothing after.
- No prose, no headings, no bullet markers — only "N. step" lines.
- Do not use any tools; just output the numbered list.`

// planStepLineRe matches one ordered step: a line whose leading "N. " (a
// number, a dot, then at least one space) is followed by step text. Lines
// that don't start at the column — a prose preamble, an indented sub-bullet,
// a markdown "1." with no space, an out-of-order number — are skipped rather
// than mis-parsed as a step.
var planStepLineRe = regexp.MustCompile(`^\d+\.\s+\S`)

// parsePlan extracts the ordered step list from a planning turn's reply.
// It returns the trimmed step texts in order, capped at planStepCap. A
// reply with fewer than planStepFloor ordered lines yields 0 steps — the
// caller treats that as "no plan" and falls back to a single plain turn.
func parsePlan(reply string) []string {
	var steps []string
	for _, line := range strings.Split(reply, "\n") {
		trimmed := strings.TrimSpace(line)
		if !planStepLineRe.MatchString(trimmed) {
			continue
		}
		text := strings.TrimSpace(strings.TrimLeft(trimmed, "0123456789. \t"))
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
//  4. The returned Reply lists every step as done / failed / not reached.
//
// Running each step as a separate turn — not one long tool loop — is what
// lets context demotion (#131) act at every boundary.
func (cs *CortexSession) TurnWithPlan(ctx context.Context, task string) (PlanRunResult, error) {
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
	planRes, planErr := cs.Turn(ctx, planModeInstruction+"\n\nTask: "+task)
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
		// task in one plain turn — the pre-step-mode behavior (#150).
		res, err := cs.Turn(ctx, task)
		if err != nil {
			return PlanRunResult{Planned: false}, fmt.Errorf("planning reply was not a step list; the fallback single turn failed: %w", err)
		}
		return PlanRunResult{Planned: false, Steps: nil, Reply: res.Reply}, nil
	}

	// --- 3. Execute each step as its own turn ----------------------------
	stepResults := make([]StepResult, 0, len(steps))
	stopped := false
	for i, step := range steps {
		if stopped {
			stepResults = append(stepResults, StepResult{Step: step, Status: stepNotReached})
			continue
		}
		// Every step prompt carries the ORIGINAL task, not just the step
		// line: demotion at the turn boundaries (#131) can fold the planning
		// turn — the only place the full task text lived — into the outline,
		// and a later step must not run blind to the overall goal or the
		// requirements the step text didn't restate (#94's failure mode).
		_, err := cs.Turn(ctx, fmt.Sprintf("Overall task: %s\n\nPlan step %d of %d: %s", task, i+1, len(steps), step))
		if err != nil {
			note := err.Error()
			stepResults = append(stepResults, StepResult{Step: step, Status: stepFailed, Note: note})
			stopped = true
			continue
		}
		// Run the project's own checks after a successful step. A failure
		// here means the step left the project broken — stop, and don't let
		// the next step build on it.
		out, ok, note := cs.runProjectCheck(ctx)
		if !ok {
			stepResults = append(stepResults, StepResult{Step: step, Status: stepFailed, Note: note})
			stopped = true
			continue
		}
		sr := StepResult{Step: step, Status: stepDone}
		if out != "" {
			sr.Note = out
		}
		stepResults = append(stepResults, sr)
	}

	// --- 4. Final per-step report ----------------------------------------
	return PlanRunResult{Planned: true, Steps: stepResults, Reply: renderPlanReport(stepResults, len(steps))}, nil
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
var runProjectCheckStub func(cs *CortexSession, ctx context.Context) (out string, ok bool, note string)

// runProjectCheck runs the project's own test/build command after a step
// completes, mirroring #129's "run the project's checks after edits". It
// returns (output, ok, note): ok is false when the check FAILED (the step
// is then marked failed), and note carries a short human summary — the
// check's trimmed output, the failure, or "skipped: <why>" when no command
// could be discovered. A missing command is a SKIP, never a failure: a
// project with no recognizable toolchain still gets plan-then-execute.
func (cs *CortexSession) runProjectCheck(ctx context.Context) (out string, ok bool, note string) {
	if runProjectCheckStub != nil {
		return runProjectCheckStub(cs, ctx)
	}
	root, ok := cs.checkRoot()
	if !ok {
		return "", true, "check skipped: no project root"
	}
	cmdLine, discovered := discoverCheck(root)
	if !discovered {
		return "", true, "check skipped: no test/build command found for this project"
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
	if checkCtx.Err() == context.DeadlineExceeded {
		return out, false, "check timed out"
	}
	if err != nil {
		if len(out) > planCheckNoteCap {
			out = out[:planCheckNoteCap] + "…"
		}
		return out, false, "check failed: " + out
	}
	if len(out) > planCheckNoteCap {
		out = out[:planCheckNoteCap] + "…"
	}
	return out, true, out
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

// planCheckTimeout caps a single post-step check. A check is a quick
// format/lint/test sanity run between steps, not the full suite; a project
// whose "test" script hangs must not stall the whole plan run.
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
