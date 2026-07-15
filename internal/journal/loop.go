package journal

import (
	"encoding/json"
	"fmt"

	"github.com/dereksantos/cortex/internal/userhome"
)

// TypeLoopRun is the entry type for one Phase 6 loop firing outcome — a
// completed run, a run that failed (including budget-cap halts, M6.4), or a
// firing skipped because the previous run was still live (overlap
// suppression, M6.2). One event type covers all three so a reader building
// run history (M6.7's view-model) never has to reconcile separate schemas.
// Written to the user-level journal — rooted under internal/userhome,
// independent of any project's .cortex/journal — matching landscape.go's
// convention. See GOAL.md §3 P6 / docs/cortex-web.md D4/D11.
const TypeLoopRun = "loop.run"

// Loop run outcomes. "skipped" is M6.2's overlap-suppression case; "success"
// and "failed" are produced by the real firing machinery landing in M6.3/M6.4.
const (
	LoopOutcomeSuccess = "success"
	LoopOutcomeFailed  = "failed"
	LoopOutcomeSkipped = "skipped"
)

// LoopRunPayload records one loop firing's outcome. ChangeRef is the
// `cortex change` reference produced by a real run (M6.3); empty for a
// skipped firing. Reason carries a short machine string for why a firing
// didn't succeed ("overlap", "budget", or an error summary) — never file
// contents or prompt text, keeping the same names-only leak posture M2.3
// established for landscape events.
//
// NextMinutes/NextReason/Done are the D11 self-pacing tuning: a NEXT/DONE
// marker RunLoopFiring parsed off the model's own final reply (see
// cmd/cortex/loop_run.go's standing self-pacing instruction). NextMinutes
// is 0 when no NEXT marker fired (the spec's own interval applies, same as
// before); Done means the loop declared its work complete — the scheduler
// (internal/loops.Scheduler.Due) never auto-fires it again, and the spec
// store disables it (DisabledReason "done: <reason>"). NextReason carries
// whichever marker's reason text fired, empty when neither did.
type LoopRunPayload struct {
	Name        string `json:"name"`
	Project     string `json:"project"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
	ChangeRef   string `json:"change_ref,omitempty"`
	NextMinutes int    `json:"next_minutes,omitempty"`
	NextReason  string `json:"next_reason,omitempty"`
	Done        bool   `json:"done,omitempty"`
}

// NewLoopRunEntry builds a journal entry for one loop.run event.
func NewLoopRunEntry(p LoopRunPayload) (*Entry, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal loop.run: %w", err)
	}
	return &Entry{Type: TypeLoopRun, V: 1, Payload: data}, nil
}

// ParseLoopRun decodes a loop.run entry's payload.
func ParseLoopRun(e *Entry) (*LoopRunPayload, error) {
	if e.Type != TypeLoopRun {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeLoopRun)
	}
	var p LoopRunPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse loop.run: %w", err)
	}
	return &p, nil
}

// AppendLoopRun opens the user-level loop journal and appends one loop.run
// event. Mirrors AppendLandscapeScan's shape: FsyncPerBatch, since a
// loop.run event is regeneratable in spirit (the loop can be re-run) even
// though its outcome is the historical record M6.7's run-history view reads.
func AppendLoopRun(p LoopRunPayload) error {
	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		return fmt.Errorf("journal: resolve user-level loop journal dir: %w", err)
	}
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		return fmt.Errorf("journal: open user-level loop journal: %w", err)
	}
	defer w.Close()

	entry, err := NewLoopRunEntry(p)
	if err != nil {
		return err
	}
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("journal: append loop.run: %w", err)
	}
	return nil
}
