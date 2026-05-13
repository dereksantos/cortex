// Package capture provides fast event capture (<10ms target)
package capture

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// Capture handles fast event capture
type Capture struct {
	cfg *config.Config
}

// New creates a new Capture instance
func New(cfg *config.Config) *Capture {
	return &Capture{cfg: cfg}
}

// CaptureFromStdin reads an event from stdin and captures it
// This must be FAST (<10ms) to avoid blocking AI tools
func (c *Capture) CaptureFromStdin() error {
	start := time.Now()

	// Read from stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("no data received")
	}

	// Parse event
	event, err := events.FromJSON(data)
	if err != nil {
		return fmt.Errorf("failed to parse event: %w", err)
	}

	// Quick filter
	if !event.ShouldCapture(c.cfg.SkipPatterns) {
		// Silent skip - don't interrupt AI tool
		return nil
	}

	// Generate ID if not provided
	if event.ID == "" {
		event.ID = generateEventID()
	}

	// Append to journal (capture writer-class, fsync per entry)
	if err := c.writeToJournal(event); err != nil {
		return fmt.Errorf("failed to write to journal: %w", err)
	}

	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		// Log warning but don't fail
		c.logSlow(elapsed)
	}

	return nil
}

// CaptureEvent captures a pre-formed event
func (c *Capture) CaptureEvent(event *events.Event) error {
	// Quick filter
	if !event.ShouldCapture(c.cfg.SkipPatterns) {
		return nil
	}

	// Generate ID if not provided
	if event.ID == "" {
		event.ID = generateEventID()
	}

	// Append to journal
	return c.writeToJournal(event)
}

// writeToJournal serializes the event and appends it to the capture
// writer-class of the journal at <ContextDir>/journal/capture/. fsync is
// applied per entry so input loss on power-fail is bounded to the last
// in-flight write — see principle 4 in docs/journal.md.
func (c *Capture) writeToJournal(event *events.Event) error {
	classDir := filepath.Join(c.cfg.ContextDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		return fmt.Errorf("open journal writer: %w", err)
	}
	defer w.Close()

	payload, err := event.ToJSON()
	if err != nil {
		return fmt.Errorf("serialize event: %w", err)
	}
	entry := &journal.Entry{
		Type:    "capture.event",
		V:       1,
		Payload: payload,
	}
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("append to journal: %w", err)
	}
	return nil
}

// generateEventID creates a unique event ID
func generateEventID() string {
	timestamp := time.Now().Format("20060102-150405")

	// Generate random suffix
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp only
		return timestamp
	}

	return fmt.Sprintf("%s-%s", timestamp, hex.EncodeToString(b))
}

// logSlow logs slow capture operations
func (c *Capture) logSlow(duration time.Duration) {
	logFile := filepath.Join(c.cfg.ContextDir, "logs", "capture.log")

	// Ensure logs directory exists
	os.MkdirAll(filepath.Dir(logFile), 0755)

	// Append log entry
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return // Silent failure
	}
	defer f.Close()

	entry := fmt.Sprintf("%s SLOW_CAPTURE duration=%s\n",
		time.Now().Format(time.RFC3339), duration)
	f.WriteString(entry)
}
