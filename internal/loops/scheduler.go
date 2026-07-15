package loops

import "time"

// Clock returns the current time. Production wires time.Now; tests inject a
// fixed or stepped fake — GOAL.md M6.2 requires no test sleeps.
type Clock func() time.Time

// LastRunInfo is the scheduling-relevant slice of a loop's most recent
// loop.run event: when it fired, and the D11 self-pacing marker (if any)
// the model's own final reply carried (cmd/cortex/loop_run.go). NextMinutes
// is 0 when no NEXT marker fired — the spec's own IntervalMinutes applies,
// unchanged from pre-D11 behavior. Done means the run ended with a DONE
// marker: the loop is finished and must never auto-fire again.
type LastRunInfo struct {
	At          time.Time
	NextMinutes int
	Done        bool
}

// LastRunLookup returns the scheduling-relevant info from a loop's most
// recent firing (of any outcome) and whether one is on record. Production
// derives this from the last loop.run journal event for the name (GOAL.md
// §3 P6: next-run is DERIVED, not a stored run-state field) — that real
// wiring lands with M6.3's firing machinery (lastrun.go's JournalLastRun);
// M6.2 takes the lookup as an injected function so the due/not-due decision
// can be tested without the real journal.
type LastRunLookup func(name string) (LastRunInfo, bool)

// NextRunAt reports when spec next becomes eligible to auto-fire, given the
// most recent loop.run event's LastRunInfo — the exact derivation
// Scheduler.Due applies, factored out so a caller that only wants to
// DISPLAY the answer (webui_loops.go's loops screen) never has to
// re-implement the rule and risk it drifting from what the scheduler will
// actually do. Reports ok=false when spec will never auto-fire again:
// manual-only (IntervalMinutes == 0), no prior run on record (nothing to
// compute FROM — Due itself treats that case as due immediately, not as a
// future instant), or the last run ended with a DONE marker. Otherwise the
// last run's own NEXT marker (info.NextMinutes) overrides the spec's
// interval for this one gap; absent a marker, the spec's IntervalMinutes
// applies exactly as before D11.
func NextRunAt(spec Spec, info LastRunInfo, hasLastRun bool) (time.Time, bool) {
	if spec.IntervalMinutes == 0 || !hasLastRun || info.Done {
		return time.Time{}, false
	}
	minutes := spec.IntervalMinutes
	if info.NextMinutes > 0 {
		minutes = info.NextMinutes
	}
	return info.At.Add(time.Duration(minutes) * time.Minute), true
}

// RunningCheck reports whether a loop's previous firing is still in flight —
// the overlap guard (docs/cortex-web.md Phase 6: "a firing is skipped, and
// journaled as skipped, while the previous run is live").
type RunningCheck func(name string) bool

// SkipRecorder records a firing that was due but skipped because of overlap.
// Production wires this to internal/journal.AppendLoopRun with
// Outcome=LoopOutcomeSkipped; tests may inject a fake to assert calls
// without touching disk, or the real journal (isolated via CORTEX_HOME) to
// prove the skip is actually journaled, not just decided in memory.
type SkipRecorder func(spec Spec, reason string) error

// Scheduler decides which loop specs are due to fire right now. It performs
// no firing itself — that's M6.3's `cortex change`-producing headless
// session machinery. Due is a pure per-tick decision: given a clock, a
// last-run lookup, and an overlap check, which specs (if any) should run.
type Scheduler struct {
	Clock   Clock
	LastRun LastRunLookup
	Running RunningCheck
	OnSkip  SkipRecorder
}

// Due returns the subset of specs that should fire on this tick:
//   - disabled specs never fire (regardless of cadence) — this also covers
//     a DONE-terminated loop once the spec store has recorded the
//     disable (D11), but see the next bullet for the belt-and-braces case.
//   - manual-only specs (IntervalMinutes == 0) never auto-fire, REGARDLESS
//     of any NEXT/DONE marker a run-now firing may have produced; they
//     only run via an explicit run-now action, out of scope for Due.
//   - a spec whose last run ended with a DONE marker never fires again,
//     even if (racily) still Enabled in the spec passed in — Due is the
//     scheduling authority, it doesn't trust the store to have caught up.
//   - a spec with no last-run on record is due immediately (first-ever run).
//   - a spec with a last-run on record is due once Clock() has reached
//     NextRunAt's answer: the last run's own NEXT marker (D11
//     self-pacing) if it set one, else last-run + spec.IntervalMinutes as
//     before.
//   - a spec that is otherwise due but whose previous firing is still
//     running (Running returns true) is skipped instead of returned: OnSkip
//     is called once with reason "overlap", and the spec is excluded from
//     the result.
//
// Returns an error only if OnSkip returns one (a skip that couldn't be
// journaled is surfaced, not swallowed).
func (sch *Scheduler) Due(specs []Spec) ([]Spec, error) {
	now := sch.Clock()
	var due []Spec
	for _, spec := range specs {
		if !spec.Enabled || spec.IntervalMinutes == 0 {
			continue
		}
		if info, ok := sch.LastRun(spec.Name); ok {
			if info.Done {
				continue
			}
			if next, ok := NextRunAt(spec, info, true); ok && now.Before(next) {
				continue
			}
		}
		if sch.Running != nil && sch.Running(spec.Name) {
			if sch.OnSkip != nil {
				if err := sch.OnSkip(spec, "overlap"); err != nil {
					return nil, err
				}
			}
			continue
		}
		due = append(due, spec)
	}
	return due, nil
}
