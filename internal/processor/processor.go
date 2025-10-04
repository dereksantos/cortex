// Package processor provides async event processing with LLM analysis
package processor

import (
	"fmt"
	"log"
	"time"

	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Processor handles async event processing
type Processor struct {
	cfg       *config.Config
	storage   *storage.Storage
	queue     *queue.Manager
	llm       *llm.OllamaClient
	running   bool
	workersCh chan struct{}
}

// New creates a new Processor
func New(cfg *config.Config, store *storage.Storage, queueMgr *queue.Manager) *Processor {
	return &Processor{
		cfg:       cfg,
		storage:   store,
		queue:     queueMgr,
		llm:       llm.NewOllamaClient(cfg),
		running:   false,
		workersCh: make(chan struct{}, 5), // Max 5 concurrent workers
	}
}

// Start starts the processor
func (p *Processor) Start() error {
	if p.running {
		return fmt.Errorf("processor already running")
	}

	p.running = true
	log.Println("Processor started")

	// Start main processing loop
	go p.processLoop()

	return nil
}

// Stop stops the processor
func (p *Processor) Stop() {
	p.running = false
	log.Println("Processor stopped")
}

// processLoop is the main processing loop
func (p *Processor) processLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for p.running {
		select {
		case <-ticker.C:
			p.processBatch()
		}
	}
}

// processBatch processes a batch of events
func (p *Processor) processBatch() {
	// First, move events from queue to storage
	processed, err := p.queue.ProcessPending()
	if err != nil {
		log.Printf("Error processing queue: %v", err)
		return
	}

	if processed > 0 {
		log.Printf("Processed %d events from queue", processed)

		// Get recent events that need analysis
		events, err := p.storage.GetRecentEvents(processed)
		if err != nil {
			log.Printf("Error getting recent events: %v", err)
			return
		}

		// Analyze each event (with LLM if available)
		for _, event := range events {
			// Check if we should analyze this event
			if p.shouldAnalyze(event) {
				go p.analyzeEvent(event)
			}
		}
	}
}

// shouldAnalyze determines if an event should be analyzed
func (p *Processor) shouldAnalyze(event *events.Event) bool {
	// Skip routine operations
	if event.ToolName == "Read" || event.ToolName == "Grep" {
		return false
	}

	// Analyze edits, writes, important commands
	return event.ToolName == "Edit" ||
		event.ToolName == "Write" ||
		event.ToolName == "Task" ||
		event.ToolName == "Bash"
}

// analyzeEvent analyzes a single event with LLM
func (p *Processor) analyzeEvent(event *events.Event) {
	// Acquire worker slot
	p.workersCh <- struct{}{}
	defer func() { <-p.workersCh }()

	// Check if Ollama is available
	if !p.llm.IsAvailable() {
		log.Printf("Ollama not available, skipping analysis for event %s", event.ID)
		return
	}

	// Analyze with LLM
	analysis, err := p.llm.AnalyzeEvent(event)
	if err != nil {
		log.Printf("Error analyzing event %s: %v", event.ID, err)
		return
	}

	// Store the insight
	if err := p.storeInsight(event.ID, analysis); err != nil {
		log.Printf("Error storing insight for event %s: %v", event.ID, err)
		return
	}

	log.Printf("Analyzed event %s: %s (importance: %d)", event.ID, analysis.Category, analysis.Importance)
}

// storeInsight stores an analysis result as an insight
func (p *Processor) storeInsight(eventID string, analysis *llm.Analysis) error {
	// This would call storage.StoreInsight - we'll implement that next
	// For now, just log it
	log.Printf("Storing insight for event %s: %s", eventID, analysis.Summary)
	return nil
}
