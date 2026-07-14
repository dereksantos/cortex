package loops

import "time"

// Clock returns the current time. Production wires time.Now; tests inject a
// fixed or stepped fake — GOAL.md M6.2 requires no test sleeps.
type Clock func() time.Time

// LastRunLookup returns the timestamp of a loop's most recent firing (of any
// outcome) and whether one is on record. Production derives this from the
// last loop.run journal event for the name (GOAL.md §3 P6: next-run is
// DERIVED, not a stored run-state field) — that real wiring lands with
// M6.3's firing machinery; M6.2 takes the lookup as an injected function so
// the due/not-due decision can be tested without the real journal.
type LastRunLookup func(name string) (time.Time, bool)

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
//   - disabled specs never fire (regardless of cadence).
//   - manual-only specs (IntervalMinutes == 0) never auto-fire; they only
//     run via an explicit run-now action, out of scope for Due.
//   - a spec with no last-run on record is due immediately (first-ever run).
//   - a spec with a last-run on record is due once Clock() has reached
//     last-run + IntervalMinutes.
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
		if last, ok := sch.LastRun(spec.Name); ok {
			next := last.Add(time.Duration(spec.IntervalMinutes) * time.Minute)
			if now.Before(next) {
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
