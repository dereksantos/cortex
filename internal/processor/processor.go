// Package processor provides async event processing with queue management
package processor

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// EventCallback is called when events are processed from the queue.
// This allows external components (like Cortex) to ingest events.
type EventCallback func([]*events.Event)

// Processor handles async event processing.
// Queue management and routing to cognition pipeline.
// LLM analysis is handled by cognitive modes (Dream, Think) not here.
type Processor struct {
	cfg     *config.Config
	storage *storage.Storage
	queue   *queue.Manager
	running atomic.Bool

	// Additional queue directories to process (for multi-project support)
	extraQueueDirs []string

	// Callback for routing events through cognition pipeline
	eventCallback EventCallback
}

// New creates a new Processor
func New(cfg *config.Config, store *storage.Storage, queueMgr *queue.Manager) *Processor {
	return &Processor{
		cfg:     cfg,
		storage: store,
		queue:   queueMgr,
		// running zero-value is false
	}
}

// AddQueueDir adds an additional queue directory to process each tick.
// The directory should contain pending/, processing/, and processed/ subdirectories.
func (p *Processor) AddQueueDir(dir string) {
	p.extraQueueDirs = append(p.extraQueueDirs, dir)
}

// Start starts the processor
func (p *Processor) Start() error {
	if !p.running.CompareAndSwap(false, true) {
		return fmt.Errorf("processor already running")
	}

	log.Println("Processor started")

	// Start main processing loop
	go p.processLoop()

	return nil
}

// Stop stops the processor
func (p *Processor) Stop() {
	p.running.Store(false)
	log.Println("Processor stopped")
}

// SetEventCallback sets a callback to be called when events are processed.
// This allows routing events through the cognition pipeline.
func (p *Processor) SetEventCallback(cb EventCallback) {
	p.eventCallback = cb
}

// processLoop is the main processing loop
func (p *Processor) processLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for p.running.Load() {
		<-ticker.C
		p.processBatch()
	}
}

// processBatch processes a batch of events from all queue directories
// and routes them through the cognition pipeline.
func (p *Processor) processBatch() {
	totalProcessed := 0

	// Process default queue
	processed, err := p.queue.ProcessPending()
	if err != nil {
		log.Printf("Error processing default queue: %v", err)
	} else {
		totalProcessed += processed
	}

	// Process additional project queues
	for _, dir := range p.extraQueueDirs {
		n, err := p.queue.ProcessPendingAt(dir)
		if err != nil {
			log.Printf("Error processing queue at %s: %v", dir, err)
			continue
		}
		totalProcessed += n
	}

	if totalProcessed > 0 {
		log.Printf("Processed %d events from queue(s)", totalProcessed)

		// Get recent events for routing through cognition
		events, err := p.storage.GetRecentEvents(totalProcessed)
		if err != nil {
			log.Printf("Error getting recent events: %v", err)
			return
		}

		// Route events through cognition pipeline
		// Analysis is handled by cognitive modes (Dream, Think, etc.)
		if p.eventCallback != nil {
			p.eventCallback(events)
		}
	}
}
