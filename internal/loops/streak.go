// streak.go — D11's three-strike auto-disable tuning: how many of a loop's
// most recent runs failed in a row. The run history (JournalRunHistory,
// history.go) is the single source of truth — no separate counter to keep
// in sync with the journal, and a success (or a skipped-overlap firing)
// naturally resets the count since it isn't a "failed" outcome.
package loops

import "github.com/dereksantos/cortex/internal/journal"

// ConsecutiveFailures reports how many of name's most recent loop.run
// entries are outcome "failed", walking backward from the latest entry and
// stopping at the first entry that isn't — a "success resets the streak"
// (D11), and a skipped (overlap) entry breaks it too, since it isn't
// literally a consecutive failure either. A name with no run history at
// all reports 0.
func ConsecutiveFailures(name string) (int, error) {
	history, err := JournalRunHistory(name)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Outcome != journal.LoopOutcomeFailed {
			break
		}
		n++
	}
	return n, nil
}
