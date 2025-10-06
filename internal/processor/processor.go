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
	cfg          *config.Config
	storage      *storage.Storage
	queue        *queue.Manager
	llm          *llm.OllamaClient
	running      bool
	workersCh    chan struct{}
	lastProcessed map[string]time.Time // file path -> last processed time (for deduplication)
}

// New creates a new Processor
func New(cfg *config.Config, store *storage.Storage, queueMgr *queue.Manager) *Processor {
	return &Processor{
		cfg:           cfg,
		storage:       store,
		queue:         queueMgr,
		llm:           llm.NewOllamaClient(cfg),
		running:       false,
		workersCh:     make(chan struct{}, 5), // Max 5 concurrent workers
		lastProcessed: make(map[string]time.Time),
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

	// Get file path for deduplication check
	filePath, hasFilePath := event.ToolInput["file_path"].(string)

	// Check for binary/generated files to skip
	if hasFilePath {
		skipExtensions := []string{
			".pyc", ".o", ".class",
			".png", ".jpg", ".jpeg", ".gif", ".svg",
			".zip", ".tar", ".gz", ".pdf",
		}
		for _, ext := range skipExtensions {
			if len(filePath) >= len(ext) && filePath[len(filePath)-len(ext):] == ext {
				return false
			}
		}

		// Special handling for lock files (can be .lock or -lock.json, etc.)
		lowerPath := toLower(filePath)
		if contains(lowerPath, ".lock") || contains(lowerPath, "-lock.") {
			return false
		}

		// Deduplication: Skip if same file was processed recently (within 30 seconds)
		if lastTime, exists := p.lastProcessed[filePath]; exists {
			if event.Timestamp.Sub(lastTime) < 30*time.Second {
				return false
			}
		}

		// Update last processed time
		p.lastProcessed[filePath] = event.Timestamp
	}

	// Analyze edits, writes, important commands
	return event.ToolName == "Edit" ||
		event.ToolName == "Write" ||
		event.ToolName == "Task" ||
		event.ToolName == "Bash"
}

// analyzeEvent analyzes a single event with LLM (async)
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

// AnalyzeEventSync analyzes a single event synchronously
func (p *Processor) AnalyzeEventSync(event *events.Event) error {
	// Check if we should analyze this event
	if !p.shouldAnalyze(event) {
		return fmt.Errorf("event type %s not eligible for analysis", event.ToolName)
	}

	// Check if Ollama is available
	if !p.llm.IsAvailable() {
		return fmt.Errorf("ollama not available")
	}

	// Analyze with LLM
	analysis, err := p.llm.AnalyzeEvent(event)
	if err != nil {
		return fmt.Errorf("llm analysis failed: %w", err)
	}

	// Store the insight
	if err := p.storeInsight(event.ID, analysis); err != nil {
		return fmt.Errorf("failed to store insight: %w", err)
	}

	return nil
}

// Helper functions for string operations
func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

// storeInsight stores an analysis result as an insight
func (p *Processor) storeInsight(eventID string, analysis *llm.Analysis) error {
	// Store the insight
	err := p.storage.StoreInsight(
		eventID,
		analysis.Category,
		analysis.Summary,
		analysis.Importance,
		analysis.Tags,
		analysis.Reasoning,
	)
	if err != nil {
		return err
	}

	// Extract and store entities from tags
	for _, tag := range analysis.Tags {
		entityID, err := p.storage.StoreEntity(analysis.Category, tag)
		if err != nil {
			log.Printf("Error storing entity: %v", err)
			continue
		}

		// Create relationship from event to entity
		if entityID > 0 {
			// Store this tag as an entity related to this category
			_ = p.storage.StoreRelationship(entityID, entityID, "tagged_in", eventID)
		}
	}

	return nil
}
