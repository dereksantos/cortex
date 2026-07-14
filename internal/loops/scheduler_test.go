package loops

import (
	"errors"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/userhome"
)

func fixedClock(t time.Time) Clock {
	return func() time.Time { return t }
}

// TestSchedulerDueFiresWhenIntervalElapsed covers both "due" cases GOAL.md
// M6.2 asks for: a spec whose last run is old enough, and a spec that has
// never run before (always due on its first tick).
func TestSchedulerDueFiresWhenIntervalElapsed(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	nightly := Spec{Name: "nightly", Project: "blog", IntervalMinutes: 60, Enabled: true}
	firstEver := Spec{Name: "first-ever", Project: "blog", IntervalMinutes: 60, Enabled: true}

	sch := &Scheduler{
		Clock: fixedClock(now),
		LastRun: func(name string) (time.Time, bool) {
			if name == "nightly" {
				return now.Add(-61 * time.Minute), true
			}
			return time.Time{}, false
		},
		Running: func(string) bool { return false },
	}

	due, err := sch.Due([]Spec{nightly, firstEver})
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("Due() = %v, want both nightly and first-ever", due)
	}
}

// TestSchedulerNotDueSkipsWhenIntervalNotElapsed proves a spec whose last
// run is too recent is excluded, without being reported as a skip (skip is
// reserved for the overlap case).
func TestSchedulerNotDueSkipsWhenIntervalNotElapsed(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	nightly := Spec{Name: "nightly", Project: "blog", IntervalMinutes: 60, Enabled: true}

	skipCalls := 0
	sch := &Scheduler{
		Clock: fixedClock(now),
		LastRun: func(string) (time.Time, bool) {
			return now.Add(-10 * time.Minute), true
		},
		Running: func(string) bool { return false },
		OnSkip:  func(Spec, string) error { skipCalls++; return nil },
	}

	due, err := sch.Due([]Spec{nightly})
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() = %v, want none (interval not elapsed)", due)
	}
	if skipCalls != 0 {
		t.Errorf("OnSkip called %d times, want 0 — not-yet-due is not a skip", skipCalls)
	}
}

// TestSchedulerDisabledNeverFires proves a disabled spec is excluded even
// when its cadence would otherwise be due, and never reported as a skip.
func TestSchedulerDisabledNeverFires(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	disabled := Spec{Name: "disabled", Project: "blog", IntervalMinutes: 60, Enabled: false}

	skipCalls := 0
	sch := &Scheduler{
		Clock: fixedClock(now),
		LastRun: func(string) (time.Time, bool) {
			return now.Add(-2 * time.Hour), true
		},
		Running: func(string) bool { return false },
		OnSkip:  func(Spec, string) error { skipCalls++; return nil },
	}

	due, err := sch.Due([]Spec{disabled})
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() = %v, want none (disabled)", due)
	}
	if skipCalls != 0 {
		t.Errorf("OnSkip called %d times, want 0 — disabled is not a skip", skipCalls)
	}
}

// TestSchedulerManualOnlyNeverAutoDue proves a zero-IntervalMinutes
// (manual-run-only, D10) spec never fires from Due, no matter how long ago
// it last ran or whether it's enabled.
func TestSchedulerManualOnlyNeverAutoDue(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	manual := Spec{Name: "manual", Project: "blog", IntervalMinutes: 0, Enabled: true}

	sch := &Scheduler{
		Clock:   fixedClock(now),
		LastRun: func(string) (time.Time, bool) { return time.Time{}, false },
		Running: func(string) bool { return false },
	}

	due, err := sch.Due([]Spec{manual})
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() = %v, want none (manual-only never auto-fires)", due)
	}
}

// TestSchedulerOverlapSkipsAndJournalsSkip covers the overlap leg of M6.2's
// DoD end-to-end against the real journal (CORTEX_HOME-isolated, matching
// internal/journal's landscape.scan test pattern): a spec that is due but
// whose previous firing is still running is excluded from Due's result and
// a loop.run(outcome=skipped) event is actually persisted, not just decided
// in memory.
func TestSchedulerOverlapSkipsAndJournalsSkip(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	nightly := Spec{Name: "nightly", Project: "blog", IntervalMinutes: 60, Enabled: true}

	sch := &Scheduler{
		Clock: fixedClock(now),
		LastRun: func(string) (time.Time, bool) {
			return now.Add(-2 * time.Hour), true
		},
		Running: func(name string) bool { return name == "nightly" },
		OnSkip: func(spec Spec, reason string) error {
			return journal.AppendLoopRun(journal.LoopRunPayload{
				Name:    spec.Name,
				Project: spec.Project,
				Outcome: journal.LoopOutcomeSkipped,
				Reason:  reason,
			})
		},
	}

	due, err := sch.Due([]Spec{nightly})
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() = %v, want none (overlap in flight)", due)
	}

	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		t.Fatalf("resolve loop journal dir: %v", err)
	}
	r, err := journal.NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	e, err := r.Next()
	if err != nil {
		t.Fatalf("Next() error = %v, want a persisted loop.run skip event", err)
	}
	got, err := journal.ParseLoopRun(e)
	if err != nil {
		t.Fatalf("ParseLoopRun: %v", err)
	}
	if got.Name != "nightly" || got.Outcome != journal.LoopOutcomeSkipped || got.Reason != "overlap" {
		t.Errorf("journaled payload = %+v, want {Name:nightly Outcome:skipped Reason:overlap ...}", *got)
	}
}

// TestSchedulerOverlapPropagatesOnSkipError proves a failure to journal the
// skip surfaces as an error from Due, rather than being silently swallowed.
func TestSchedulerOverlapPropagatesOnSkipError(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	nightly := Spec{Name: "nightly", Project: "blog", IntervalMinutes: 60, Enabled: true}
	boom := errors.New("boom")

	sch := &Scheduler{
		Clock: fixedClock(now),
		LastRun: func(string) (time.Time, bool) {
			return now.Add(-2 * time.Hour), true
		},
		Running: func(string) bool { return true },
		OnSkip:  func(Spec, string) error { return boom },
	}

	_, err := sch.Due([]Spec{nightly})
	if !errors.Is(err, boom) {
		t.Errorf("Due() error = %v, want errors.Is(err, boom)", err)
	}
}
