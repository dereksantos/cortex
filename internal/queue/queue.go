// Package queue manages the event queue processing
package queue

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// Manager handles queue operations
type Manager struct {
	cfg     *config.Config
	storage *storage.Storage
}

// New creates a new queue Manager
func New(cfg *config.Config, store *storage.Storage) *Manager {
	return &Manager{
		cfg:     cfg,
		storage: store,
	}
}

// ProcessPending processes all pending events in the queue
func (m *Manager) ProcessPending() (int, error) {
	pendingDir := filepath.Join(m.cfg.ContextDir, "queue", "pending")
	processingDir := filepath.Join(m.cfg.ContextDir, "queue", "processing")
	processedDir := filepath.Join(m.cfg.ContextDir, "queue", "processed")

	// Get all pending files
	files, err := filepath.Glob(filepath.Join(pendingDir, "*.json"))
	if err != nil {
		return 0, fmt.Errorf("failed to list pending files: %w", err)
	}

	processed := 0
	for _, file := range files {
		// Move to processing
		filename := filepath.Base(file)
		processingFile := filepath.Join(processingDir, filename)

		if err := os.Rename(file, processingFile); err != nil {
			continue // Skip if can't move
		}

		// Read and parse event
		data, err := os.ReadFile(processingFile)
		if err != nil {
			os.Rename(processingFile, file) // Move back to pending
			continue
		}

		event, err := events.FromJSON(data)
		if err != nil {
			os.Rename(processingFile, file) // Move back to pending
			continue
		}

		// Store in database
		if err := m.storage.StoreEvent(event); err != nil {
			os.Rename(processingFile, file) // Move back to pending
			continue
		}

		// Move to processed
		processedFile := filepath.Join(processedDir, filename)
		if err := os.Rename(processingFile, processedFile); err != nil {
			// Already in DB, so just delete
			os.Remove(processingFile)
		}

		processed++
	}

	return processed, nil
}

// GetPendingCount returns the number of pending events
func (m *Manager) GetPendingCount() (int, error) {
	pendingDir := filepath.Join(m.cfg.ContextDir, "queue", "pending")
	files, err := filepath.Glob(filepath.Join(pendingDir, "*.json"))
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

// CleanProcessed removes old processed events
func (m *Manager) CleanProcessed(maxAge int) error {
	processedDir := filepath.Join(m.cfg.ContextDir, "queue", "processed")

	// For now, just remove all processed files
	// In production, would check timestamps
	files, err := filepath.Glob(filepath.Join(processedDir, "*.json"))
	if err != nil {
		return err
	}

	for _, file := range files {
		os.Remove(file)
	}

	return nil
}
