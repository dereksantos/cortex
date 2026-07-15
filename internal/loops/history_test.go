// history_test.go — M6.7a (GOAL.md §6): JournalRunHistory is the new
// production journal reader the loops view-model needs (STATE.md's
// 2026-07-13 Next Up) — sibling to JournalLastRun (M6.6) but keeping every
// matching entry per name instead of collapsing to the most recent
// timestamp.
package loops

import (
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
)

// TestJournalRunHistoryReturnsEveryMatchingEntryInWriteOrder proves
// JournalRunHistory reports an empty (nil) slice against an empty journal,
// then every entry for a given name — in the journal's write order — while
// ignoring entries belonging to other loop names.
func TestJournalRunHistoryReturnsEveryMatchingEntryInWriteOrder(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	got, err := JournalRunHistory("nightly")
	if err != nil {
		t.Fatalf("JournalRunHistory(nightly) on an empty journal: err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("JournalRunHistory(nightly) on an empty journal = %+v, want empty", got)
	}

	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "other", Outcome: journal.LoopOutcomeSuccess}); err != nil {
		t.Fatalf("AppendLoopRun(other): %v", err)
	}
	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: journal.LoopOutcomeFailed, Reason: "budget"}); err != nil {
		t.Fatalf("AppendLoopRun(nightly, failed): %v", err)
	}
	if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: journal.LoopOutcomeSuccess, ChangeRef: "cortex/fix"}); err != nil {
		t.Fatalf("AppendLoopRun(nightly, success): %v", err)
	}

	got, err = JournalRunHistory("nightly")
	if err != nil {
		t.Fatalf("JournalRunHistory(nightly): err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("JournalRunHistory(nightly) = %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Outcome != journal.LoopOutcomeFailed || got[0].Reason != "budget" {
		t.Errorf("got[0] = %+v, want the failed/budget entry first (write order)", got[0])
	}
	if got[1].Outcome != journal.LoopOutcomeSuccess || got[1].ChangeRef != "cortex/fix" {
		t.Errorf("got[1] = %+v, want the success/cortex-fix entry second (write order)", got[1])
	}
	if got[1].Timestamp.Before(got[0].Timestamp) {
		t.Errorf("got[1].Timestamp %v is before got[0].Timestamp %v, want write order = chronological", got[1].Timestamp, got[0].Timestamp)
	}

	if got, err := JournalRunHistory("someone-else"); err != nil || len(got) != 0 {
		t.Errorf("JournalRunHistory(someone-else) = %+v, err = %v, want empty, nil (never ran)", got, err)
	}
}

// TestJournalRunHistoryCarriesSelfPacingMarkerFields is D11's tuning: the
// NEXT/DONE marker fields (NextMinutes/NextReason/Done) round-trip through
// JournalRunHistory exactly like Outcome/Reason/ChangeRef already do — the
// loops screen's run-history rows (cmd/cortex/webui/loops.js) need
// NextReason to show a marker's reason alongside its outcome.
func TestJournalRunHistoryCarriesSelfPacingMarkerFields(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	if err := journal.AppendLoopRun(journal.LoopRunPayload{
		Name: "nightly", Outcome: journal.LoopOutcomeSuccess,
		NextMinutes: 20, NextReason: "waiting on CI", Done: false,
	}); err != nil {
		t.Fatalf("AppendLoopRun: %v", err)
	}

	got, err := JournalRunHistory("nightly")
	if err != nil {
		t.Fatalf("JournalRunHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("JournalRunHistory(nightly) = %d entries, want 1", len(got))
	}
	if got[0].NextMinutes != 20 || got[0].NextReason != "waiting on CI" || got[0].Done {
		t.Errorf("got[0] = %+v, want NextMinutes=20 NextReason=%q Done=false", got[0], "waiting on CI")
	}
}
