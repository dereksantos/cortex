package loops

import (
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
)

// TestConsecutiveFailuresCountsTrailingFailedRunsAndResetsOnSuccess proves
// D11's three-strike tuning: the count walks backward from the latest
// entry, stops at the first non-failed outcome (a success resets the
// streak to 0, per D11's own wording), and a name with no history at all
// reports 0 rather than erroring.
func TestConsecutiveFailuresCountsTrailingFailedRunsAndResetsOnSuccess(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	if n, err := ConsecutiveFailures("nightly"); err != nil || n != 0 {
		t.Fatalf("ConsecutiveFailures(nightly) on empty history = %d, err = %v, want 0, nil", n, err)
	}

	appendRun := func(outcome string) {
		t.Helper()
		if err := journal.AppendLoopRun(journal.LoopRunPayload{Name: "nightly", Outcome: outcome}); err != nil {
			t.Fatalf("AppendLoopRun(%s): %v", outcome, err)
		}
	}

	appendRun(journal.LoopOutcomeSuccess)
	appendRun(journal.LoopOutcomeFailed)
	if n, err := ConsecutiveFailures("nightly"); err != nil || n != 1 {
		t.Fatalf("ConsecutiveFailures after [success,failed] = %d, err = %v, want 1", n, err)
	}

	appendRun(journal.LoopOutcomeFailed)
	if n, err := ConsecutiveFailures("nightly"); err != nil || n != 2 {
		t.Fatalf("ConsecutiveFailures after [success,failed,failed] = %d, err = %v, want 2", n, err)
	}

	appendRun(journal.LoopOutcomeFailed)
	if n, err := ConsecutiveFailures("nightly"); err != nil || n != 3 {
		t.Fatalf("ConsecutiveFailures after three trailing failures = %d, err = %v, want 3", n, err)
	}

	appendRun(journal.LoopOutcomeSuccess)
	if n, err := ConsecutiveFailures("nightly"); err != nil || n != 0 {
		t.Fatalf("ConsecutiveFailures after a trailing success = %d, err = %v, want 0 (streak reset)", n, err)
	}
}
