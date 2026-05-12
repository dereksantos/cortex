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

// New creates a Processor wired with the capture.event and observation.*
// projectors. The default journal class dirs (<ContextDir>/journal/capture/
// and <ContextDir>/journal/observation/) are registered automatically;
// additional projects' journals can be added via AddJournalDir.
//
// Future writer-classes (dream, reflect, resolve, think, feedback, eval)
// register their projectors here in their respective slices.
func New(cfg *config.Config, store *storage.Storage) *Processor {
	p := &Processor{
		cfg:     cfg,
		storage: store,
	}
	p.registry = journal.NewRegistry()
	p.registry.Register("capture.event", 1, p.projectCaptureEvent)
	for _, typ := range []string{
		journal.TypeObservationClaudeTranscript,
		journal.TypeObservationGitCommit,
		journal.TypeObservationMemoryFile,
	} {
		p.registry.Register(typ, 1, p.projectObservation)
	}
	p.registry.Register(journal.TypeDreamInsight, 1, p.projectDreamInsight)
	p.registry.Register(journal.TypeReflectRerank, 1, p.projectReflectRerank)
	p.registry.Register(journal.TypeResolveRetrieval, 1, p.projectResolveRetrieval)
	p.registry.Register(journal.TypeThinkSessionContext, 1, p.projectThinkSessionContext)
	for _, typ := range []string{
		journal.TypeFeedbackCorrection,
		journal.TypeFeedbackConfirmation,
		journal.TypeFeedbackRetraction,
	} {
		p.registry.Register(typ, 1, p.projectFeedback)
	}
	p.registry.Register(journal.TypeEvalCellResult, 1, p.projectEvalCellResult)

	if cfg != nil && cfg.ContextDir != "" {
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "capture"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "observation"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "dream"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "reflect"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "resolve"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "think"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "feedback"))
		p.AddJournalDir(filepath.Join(cfg.ContextDir, "journal", "eval"))
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

// projectEvalCellResult records a journaled eval cell result in storage's
// parallel eval-row list. The canonical eval persistence path remains
// internal/eval/v2/persist_cell.go for now; this projection lets
// `cortex journal verify` and counterfactual replay see eval rows
// through the same lens as the other writer-classes.
func (p *Processor) projectEvalCellResult(e *journal.Entry) error {
	payload, err := journal.ParseEvalCellResult(e)
	if err != nil {
		return fmt.Errorf("parse eval.cell_result at offset %d: %w", e.Offset, err)
	}
	if err := p.storage.RecordEvalCellResult(&storage.EvalCellResultRow{
		RunID:         payload.RunID,
		ScenarioID:    payload.ScenarioID,
		Harness:       payload.Harness,
		Provider:      payload.Provider,
		Model:         payload.Model,
		Strategy:      payload.ContextStrategy,
		TaskSuccess:   payload.TaskSuccess,
		TokensIn:      payload.TokensIn,
		TokensOut:     payload.TokensOut,
		CostUSD:       payload.CostUSD,
		LatencyMs:     payload.LatencyMs,
		JournalOffset: int64(e.Offset),
		RecordedAt:    e.TS,
	}); err != nil {
		return fmt.Errorf("record eval row at offset %d: %w", e.Offset, err)
	}
	return nil
}

// projectFeedback records a feedback.* entry to storage. The entry's Type
// (correction / confirmation / retraction) is preserved in the Feedback
// row; storage indexes by GradedID and tracks the retracted-id set.
func (p *Processor) projectFeedback(e *journal.Entry) error {
	payload, err := journal.ParseFeedback(e)
	if err != nil {
		return fmt.Errorf("parse feedback at offset %d: %w", e.Offset, err)
	}
	if err := p.storage.RecordFeedback(&storage.Feedback{
		Type:          e.Type,
		GradedID:      payload.GradedID,
		GradedOffset:  int64(payload.GradedOffset),
		Note:          payload.Note,
		Replacement:   payload.Replacement,
		Reason:        payload.Reason,
		SessionID:     payload.SessionID,
		JournalOffset: int64(e.Offset),
		RecordedAt:    e.TS,
	}); err != nil {
		return fmt.Errorf("record feedback at offset %d: %w", e.Offset, err)
	}
	return nil
}

// projectThinkSessionContext records a session-context snapshot.
func (p *Processor) projectThinkSessionContext(e *journal.Entry) error {
	payload, err := journal.ParseThinkSessionContext(e)
	if err != nil {
		return fmt.Errorf("parse think.session_context at offset %d: %w", e.Offset, err)
	}
	if err := p.storage.RecordSessionContextSnapshot(&storage.SessionContextSnapshot{
		TopicWeights:  payload.TopicWeights,
		RecentQueries: payload.RecentQueries,
		CachedQueries: payload.CachedQueries,
		SessionID:     payload.SessionID,
		JournalOffset: int64(e.Offset),
		RecordedAt:    e.TS,
	}); err != nil {
		return fmt.Errorf("record session_context at offset %d: %w", e.Offset, err)
	}
	return nil
}

// projectResolveRetrieval records one retrieval decision to storage's
// retrieval log (slice Z2).
func (p *Processor) projectResolveRetrieval(e *journal.Entry) error {
	payload, err := journal.ParseResolveRetrieval(e)
	if err != nil {
		return fmt.Errorf("parse resolve.retrieval at offset %d: %w", e.Offset, err)
	}
	if err := p.storage.RecordRetrieval(&storage.Retrieval{
		QueryText:     payload.QueryText,
		Decision:      payload.Decision,
		Confidence:    payload.Confidence,
		ResultCount:   payload.ResultCount,
		InjectedIDs:   payload.InjectedIDs,
		AvgScore:      payload.AvgScore,
		MaxScore:      payload.MaxScore,
		Reason:        payload.Reason,
		SessionID:     payload.SessionID,
		JournalOffset: int64(e.Offset),
		RecordedAt:    e.TS,
	}); err != nil {
		return fmt.Errorf("record retrieval at offset %d: %w", e.Offset, err)
	}
	return nil
}

// projectReflectRerank records each contradiction surfaced by a rerank
// entry to storage. The rerank itself is preserved by the journal; the
// projection extracts the contradictions sub-records into a queryable
// derived view (storage.GetContradictions).
func (p *Processor) projectReflectRerank(e *journal.Entry) error {
	payload, err := journal.ParseReflectRerank(e)
	if err != nil {
		return fmt.Errorf("parse reflect.rerank at offset %d: %w", e.Offset, err)
	}
	for _, c := range payload.Contradictions {
		if c.Reason == "" || len(c.IDs) == 0 {
			continue
		}
		if err := p.storage.RecordContradiction(&storage.Contradiction{
			IDs:           c.IDs,
			Reason:        c.Reason,
			JournalOffset: int64(e.Offset),
			QueryText:     payload.QueryText,
			RecordedAt:    e.TS,
		}); err != nil {
			return fmt.Errorf("record contradiction at offset %d: %w", e.Offset, err)
		}
	}
	return nil
}

// projectDreamInsight projects a dream.insight entry to storage. Calls
// StoreInsightWithSession with the payload's fields. Storage's insight
// log is append-only with soft-update semantics (op="update" records),
// so re-projection during rebuild is safe.
func (p *Processor) projectDreamInsight(e *journal.Entry) error {
	payload, err := journal.ParseDreamInsight(e)
	if err != nil {
		return fmt.Errorf("parse dream.insight at offset %d: %w", e.Offset, err)
	}
	p.storage.StoreInsightWithSession(
		payload.InsightID,
		payload.Category,
		payload.Content,
		payload.Importance,
		payload.Tags,
		payload.Reasoning,
		payload.SessionID,
		payload.SourceName,
	)
	return nil
}

// projectObservation projects an observation.* entry to storage. Idempotent
// via storage.RecordObservation's (URI, content_hash) dedup.
func (p *Processor) projectObservation(e *journal.Entry) error {
	payload, err := journal.ParseObservation(e)
	if err != nil {
		return fmt.Errorf("parse observation at offset %d: %w", e.Offset, err)
	}
	obs := &storage.Observation{
		Type:          e.Type,
		SourceName:    payload.SourceName,
		URI:           payload.URI,
		ContentHash:   payload.ContentHash,
		Size:          payload.Size,
		Modified:      payload.Modified,
		JournalOffset: int64(e.Offset),
		RecordedAt:    e.TS,
	}
	if _, err := p.storage.RecordObservation(obs); err != nil {
		return fmt.Errorf("record observation: %w", err)
	}
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
