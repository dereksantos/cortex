// lastrun.go — M6.6 (GOAL.md §6): the real LastRunLookup implementation
// M6.2's Scheduler only ever took as an injected function. GOAL.md §3 P6
// is explicit that next-run is DERIVED from journal history, not stored in
// loops.json (or anywhere else) — so this reads the same user-level
// loop.run journal internal/journal.AppendLoopRun writes to
// (cmd/cortex/loop_run.go, M6.3) rather than inventing a parallel
// run-state file.
package loops

import (
	"io"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/userhome"
)

// JournalLastRun implements LastRunLookup against the real user-level
// loop.run journal: it scans every loop.run entry (of any outcome —
// success, failed, or skipped all count as "it ran") and returns the most
// recent timestamp among those matching name. A journal that has never
// been written (no prior firing, ever, for this or any loop) or that has
// no entry for this name reports (zero, false) — the same "first-ever
// run" signal Scheduler.Due already treats as due immediately.
func JournalLastRun(name string) (time.Time, bool) {
	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		return time.Time{}, false
	}
	r, err := journal.NewReader(dir)
	if err != nil {
		return time.Time{}, false
	}
	defer r.Close()

	var last time.Time
	var found bool
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if e.Type != journal.TypeLoopRun {
			continue
		}
		p, err := journal.ParseLoopRun(e)
		if err != nil {
			continue
		}
		if p.Name != name {
			continue
		}
		if !found || e.TS.After(last) {
			last = e.TS
			found = true
		}
	}
	return last, found
}
