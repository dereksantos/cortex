// lastrun_test.go — M6.6 (GOAL.md §6): next-run must be DERIVED from the
// real loop.run journal (GOAL.md §3 P6), not any in-memory scheduler
// state, so a fresh Scheduler ("restart") sees the same due/not-due
// verdict a long-running one would. JournalLastRun is the production
// LastRunLookup implementation the M6.2 Scheduler tests only ever took as
// an injected fake; these tests exercise it against a real (CORTEX_HOME-
// isolated) journal, then prove the Scheduler-level no-double-fire
// behavior end to end with a clock anchored close to the firing moment
// rather than a sleep.
package loops

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// TestJournalLastRunReturnsMostRecentMatchingName proves JournalLastRun
// reports "not found" against an empty journal, then the most recent
// timestamp among possibly-several entries for a given name — ignoring
// entries belonging to other loop names entirely.
func TestJournalLastRunReturnsMostRecentMatchingName(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	if _, found := JournalLastRun("nightly"); found {
		t.Fatalf("JournalLastRun(nightly) on an empty journal: found = true, want false")
	}

	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "other", Outcome: journal.LoopOutcomeSuccess}); err != nil {
		t.Fatalf("AppendLoopRun(other): %v", err)
	}
	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: journal.LoopOutcomeFailed}); err != nil {
		t.Fatalf("AppendLoopRun(nightly, failed): %v", err)
	}
	first, found := JournalLastRun("nightly")
	if !found {
		t.Fatalf("JournalLastRun(nightly) after one entry: found = false, want true")
	}

	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: journal.LoopOutcomeSuccess}); err != nil {
		t.Fatalf("AppendLoopRun(nightly, success): %v", err)
	}
	second, found := JournalLastRun("nightly")
	if !found {
		t.Fatalf("JournalLastRun(nightly) after two entries: found = false, want true")
	}
	if second.At.Before(first.At) {
		t.Errorf("second last-run TS %v is before first %v, want the most recent", second.At, first.At)
	}

	if _, found := JournalLastRun("someone-else"); found {
		t.Errorf("JournalLastRun(someone-else): found = true, want false (never ran)")
	}
}

// TestJournalLastRunReportsOnlyTheLatestEntrysMarker is D11's self-pacing
// tuning: NextMinutes/Done must come from the single most recent entry,
// never aggregated or carried forward from an earlier one — an older DONE
// followed by a newer ordinary run must NOT still report Done=true.
func TestJournalLastRunReportsOnlyTheLatestEntrysMarker(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: journal.LoopOutcomeSuccess, Done: true, NextReason: "shipped"}); err != nil {
		t.Fatalf("AppendLoopRun(done): %v", err)
	}
	info, found := JournalLastRun("nightly")
	if !found || !info.Done {
		t.Fatalf("JournalLastRun(nightly) after DONE = %+v, found=%v, want Done=true", info, found)
	}

	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: journal.LoopOutcomeSuccess, NextMinutes: 30}); err != nil {
		t.Fatalf("AppendLoopRun(next): %v", err)
	}
	info, found = JournalLastRun("nightly")
	if !found {
		t.Fatal("JournalLastRun(nightly) after NEXT: found = false, want true")
	}
	if info.Done {
		t.Error("JournalLastRun(nightly) after a newer NEXT run still reports Done=true, want false — must not carry forward the older DONE")
	}
	if info.NextMinutes != 30 {
		t.Errorf("NextMinutes = %d, want 30", info.NextMinutes)
	}
}

// TestSchedulerRestartAcrossProcessDoesNotDoubleFire is the M6.6 DoD
// proper: two Scheduler instances ("restart") sharing the same on-disk
// journal via JournalLastRun and a clock anchored near the firing moment
// (never a real sleep) must agree — the second does not re-fire
// immediately after the first's firing lands in the journal, yet a later
// instance still fires once the clock has advanced past the next due
// time, proving next-run survives a restart rather than being lost or
// re-computed from nothing.
func TestSchedulerRestartAcrossProcessDoesNotDoubleFire(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	base := time.Now().UTC()
	nightly := Spec{Name: "nightly", Project: "blog", IntervalMinutes: 15, Enabled: true}

	// First "process": no prior run on record for this name, fires
	// immediately (Scheduler.Due's documented first-ever-run behavior).
	sch1 := &Scheduler{
		Clock:   fixedClock(base),
		LastRun: JournalLastRun,
		Running: func(string) bool { return false },
	}
	due, err := sch1.Due([]Spec{nightly})
	if err != nil {
		t.Fatalf("Due() (first process) error = %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("Due() (first process) = %v, want [nightly] (never run before)", due)
	}

	// The firing lands a loop.run event in the real journal — the same
	// mechanism RunLoopFiring (cmd/cortex/loop_run.go, M6.3) uses in
	// production.
	if err := journal.AppendLoopRun(journal.LoopRunPayload{
		Name: "nightly", Project: "blog", Outcome: journal.LoopOutcomeSuccess,
	}); err != nil {
		t.Fatalf("AppendLoopRun: %v", err)
	}

	// "Restart": a brand-new Scheduler instance, same on-disk journal,
	// clock still anchored near the firing moment — must NOT re-fire.
	sch2 := &Scheduler{
		Clock:   fixedClock(base.Add(1 * time.Second)),
		LastRun: JournalLastRun,
		Running: func(string) bool { return false },
	}
	due, err = sch2.Due([]Spec{nightly})
	if err != nil {
		t.Fatalf("Due() (restarted, too soon) error = %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() (restarted, too soon) = %v, want none — next-run must survive the restart", due)
	}

	// Advance the clock past the interval: a further-restarted Scheduler
	// must fire again — next-run was derived, not lost across restarts.
	sch3 := &Scheduler{
		Clock:   fixedClock(base.Add(16 * time.Minute)),
		LastRun: JournalLastRun,
		Running: func(string) bool { return false },
	}
	due, err = sch3.Due([]Spec{nightly})
	if err != nil {
		t.Fatalf("Due() (after interval elapsed) error = %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("Due() (after interval elapsed) = %v, want [nightly]", due)
	}
}
