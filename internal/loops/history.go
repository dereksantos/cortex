// history.go — M6.7a (GOAL.md §6): the loops screen's run-history reader.
// The M6.6 LastRunLookup (lastrun.go, JournalLastRun) already opens the
// same user-level loop.run journal and collapses matching entries to the
// single most recent timestamp the scheduler needs. The view-model this
// file exists for (cmd/cortex/webui_loops.go) needs the full history per
// loop instead — every matching entry, not just the latest — so this is a
// sibling reader over the identical journal, not a replacement.
package loops

import (
	"fmt"
	"io"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/userhome"
)

// RunRecord is one loop.run journal entry for a single named loop —
// Timestamp comes from the journal entry envelope (journal.Entry.TS, set
// at append time), the rest from the entry's LoopRunPayload. NextMinutes/
// NextReason/Done mirror the D11 self-pacing marker fields
// (internal/journal/loop.go's LoopRunPayload) so the loops screen's
// run-history rows can show a NEXT/DONE marker's reason alongside its
// outcome, and buildLoopsViewModel (cmd/cortex/webui_loops.go) can derive
// next_run from the same data this reader already fetches, without a
// second journal pass.
type RunRecord struct {
	Timestamp   time.Time `json:"timestamp"`
	Outcome     string    `json:"outcome"`
	Reason      string    `json:"reason,omitempty"`
	ChangeRef   string    `json:"change_ref,omitempty"`
	NextMinutes int       `json:"next_minutes,omitempty"`
	NextReason  string    `json:"next_reason,omitempty"`
	Done        bool      `json:"done,omitempty"`
}

// JournalRunHistory returns every loop.run entry matching name, in the
// journal's write (chronological ascending) order. A journal that has
// never been written, or that has no entry for name, returns an empty
// (nil) slice and a nil error — the same "no history yet" signal
// JournalLastRun reports as (zero, false) for the same case.
func JournalRunHistory(name string) ([]RunRecord, error) {
	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve loop journal dir: %w", err)
	}
	r, err := journal.NewReader(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to open loop journal reader: %w", err)
	}
	defer r.Close()

	var out []RunRecord
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read loop journal entry: %w", err)
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
		out = append(out, RunRecord{
			Timestamp:   e.TS,
			Outcome:     p.Outcome,
			Reason:      p.Reason,
			ChangeRef:   p.ChangeRef,
			NextMinutes: p.NextMinutes,
			NextReason:  p.NextReason,
			Done:        p.Done,
		})
	}
	return out, nil
}
