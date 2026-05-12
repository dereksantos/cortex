// Package processor projects journal entries to derived state in SQLite
// and routes the freshly-projected events through the cognition pipeline.
//
// Pre-journal, this package drained .cortex/queue/ via queue.ProcessPending.
// Post-C2 it runs a journal.Indexer per writer-class instead — see
// docs/journal.md for the CQRS commitment. The queue package will be
// removed in slice C6 once no callers remain.
package processor

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// EventCallback is invoked with the events projected in a batch. Cortex's
// cognition pipeline (Dream, Think) consumes this.
type EventCallback func([]*events.Event)

// Processor runs journal indexers on a tick, projecting capture.event
// entries (and, post slices O/D/R/Z/T/B/E, other writer-classes) to SQLite,
// then notifies the cognition pipeline with the events it just stored.
type Processor struct {
	cfg     *config.Config
	storage *storage.Storage
	running atomic.Bool

	registry *journal.Registry
	indexers []*journal.Indexer

	eventCallback EventCallback

	batchMu     sync.Mutex
	batchEvents []*events.Event
}

// New creates a Processor wired with the capture.event projector. The
// default journal class dir (<ContextDir>/journal/capture/) is registered
// automatically; additional projects' journals can be added via
// AddJournalDir.
func New(cfg *config.Config, store *storage.Storage) *Processor {
	p := &Processor{
		cfg:     cfg,
		storage: store,
	}
	p.registry = journal.NewRegistry()
	p.registry.Register("capture.event", 1, p.projectCaptureEvent)

	if cfg != nil && cfg.ContextDir != "" {
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "capture"))
	}
	return p
}

// projectCaptureEvent stores a capture.event entry's payload to SQLite and
// records the event for the post-batch cognition callback. Idempotent:
// storage.StoreEvent uses INSERT OR REPLACE by event ID so re-projection
// during rebuild does not double-count.
func (p *Processor) projectCaptureEvent(e *journal.Entry) error {
	ev, err := events.FromJSON(e.Payload)
	if err != nil {
		return fmt.Errorf("parse capture.event payload at offset %d: %w", e.Offset, err)
	}
	if err := p.storage.StoreEvent(ev); err != nil {
		return fmt.Errorf("store event %s: %w", ev.ID, err)
	}
	p.batchMu.Lock()
	p.batchEvents = append(p.batchEvents, ev)
	p.batchMu.Unlock()
	return nil
}

// AddJournalDir adds an additional writer-class directory to project from
// each tick. Used for multi-project setups where each project has its own
// .cortex/journal/capture/.
func (p *Processor) AddJournalDir(classDir string) {
	p.indexers = append(p.indexers, journal.NewIndexer(journal.IndexerOpts{
		ClassDir:  classDir,
		Registry:  p.registry,
		OnUnknown: journal.UnknownLogAndSkip,
	}))
}

// AddQueueDir is a deprecated compatibility shim for the pre-journal API.
// dir is expected to be a .cortex/queue/ path; the corresponding sibling
// .cortex/journal/capture/ is registered instead. Removed in slice C6.
func (p *Processor) AddQueueDir(dir string) {
	contextDir := filepath.Dir(dir)
	p.AddJournalDir(filepath.Join(contextDir, "journal", "capture"))
}

// Start starts the processor's tick loop.
func (p *Processor) Start() error {
	if !p.running.CompareAndSwap(false, true) {
		return fmt.Errorf("processor already running")
	}
	log.Println("Processor started")
	go p.processLoop()
	return nil
}

// Stop stops the tick loop. Idempotent.
func (p *Processor) Stop() {
	p.running.Store(false)
	log.Println("Processor stopped")
}

// SetEventCallback wires the cognition pipeline. Called with the events
// projected during each batch.
func (p *Processor) SetEventCallback(cb EventCallback) {
	p.eventCallback = cb
}

// RunBatch projects a single tick of work synchronously. Exposed so the
// `cortex ingest` one-shot command (slice C3) can drive the same indexer
// without starting the loop.
func (p *Processor) RunBatch() (int, error) {
	return p.processBatchSync()
}

func (p *Processor) processLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for p.running.Load() {
		<-ticker.C
		if _, err := p.processBatchSync(); err != nil {
			log.Printf("processor: %v", err)
		}
	}
}

// processBatchSync runs each indexer once and dispatches the cognition
// callback if events were projected. Returns the total entry count handled
// (including LogAndSkip'd unknown entries).
func (p *Processor) processBatchSync() (int, error) {
	total := 0
	var firstErr error
	for _, ix := range p.indexers {
		n, err := ix.RunOnce()
		total += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if total > 0 {
		log.Printf("Projected %d journal entries", total)
		events := p.drainBatchEvents()
		if p.eventCallback != nil && len(events) > 0 {
			p.eventCallback(events)
		}
	}
	return total, firstErr
}

func (p *Processor) drainBatchEvents() []*events.Event {
	p.batchMu.Lock()
	defer p.batchMu.Unlock()
	out := p.batchEvents
	p.batchEvents = nil
	return out
}
