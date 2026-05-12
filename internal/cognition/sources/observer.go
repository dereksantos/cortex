package sources

import (
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// Observer emits observation.* journal entries when a DreamSource reads
// from an external substrate. The observer records what was seen — source
// name + URI + content-hash + size + modified-time — never the substrate
// content itself (principle 3 in docs/journal.md: external producers
// retain ownership).
//
// Observations are best-effort: errors are logged but never surface to
// callers. A failing observer must not block Dream's actual work. Callers
// hold a nil-safe handle — *Observer methods short-circuit on nil receivers
// so sources can pass `observer: nil` to skip observation entirely.
//
// Idempotency is the projection layer's job (slice O3): the indexer
// dedups by (uri, content_hash). Observers can append the same observation
// repeatedly without harm; only new content-hashes create new derived
// rows.
type Observer struct {
	classDir string

	mu sync.Mutex
}

// NewObserver returns an Observer rooted at <cortexDir>/journal/observation/.
// Returns nil if cortexDir is empty — callers can pass nil to disable
// observation.
func NewObserver(cortexDir string) *Observer {
	if cortexDir == "" {
		return nil
	}
	return &Observer{
		classDir: filepath.Join(cortexDir, "journal", "observation"),
	}
}

// Observe appends one observation entry for a substrate read. typ must be
// one of journal.TypeObservation*. Best-effort: failures are logged.
func (o *Observer) Observe(typ, sourceName, uri string, content []byte, modified time.Time) {
	if o == nil {
		return
	}
	entry, err := journal.NewObservationEntry(typ, sourceName, uri, content, int64(len(content)), modified)
	if err != nil {
		log.Printf("observer: build entry: %v", err)
		return
	}

	// Serialize writer access across goroutines within a process. Cross-
	// process serialization uses the per-segment flock in journal.Writer.
	o.mu.Lock()
	defer o.mu.Unlock()

	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: o.classDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		log.Printf("observer: open writer: %v", err)
		return
	}
	defer w.Close()

	if _, err := w.Append(entry); err != nil {
		log.Printf("observer: append: %v", err)
	}
}
