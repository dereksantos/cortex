// Package capture provides fast event capture (<10ms target)
package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// embedWriteTimeout bounds the background embed-on-capture call. Generous
// relative to the Reflex read budget because this is off the hot path (the
// turn has already answered), but capped so a wedged embedder can't leak
// goroutines that outlive a long REPL session.
const embedWriteTimeout = 15 * time.Second

// Capture handles fast event capture
type Capture struct {
	cfg      *config.Config
	storage  *storage.Storage // optional; when non-nil CaptureEvent also writes events synchronously into storage so they are searchable within the same session
	embedder llm.Embedder     // optional; when set, captured events are embedded into storage (the write side of semantic search) asynchronously
}

// New creates a new Capture instance
func New(cfg *config.Config) *Capture {
	return &Capture{cfg: cfg}
}

// NewWithStorage wires a Capture to a Storage so events become
// searchable in-process the moment they are captured. Without this,
// captures only land in the journal (durable) and the Storage layer
// can't see them until a journal→storage projection step runs — which
// breaks intra-session learning (Think can't cache what Reflex can't
// find).
//
// store may be nil, in which case this behaves like New().
func NewWithStorage(cfg *config.Config, store *storage.Storage) *Capture {
	return &Capture{cfg: cfg, storage: store}
}

// SetStorage attaches a Storage after construction. Useful when the
// REPL builds its captureClient before its Storage is available (or
// the inverse). nil is allowed — clears the attachment.
func (c *Capture) SetStorage(store *storage.Storage) {
	c.storage = store
}

// SetEmbedder attaches an embedder so captured events become semantically
// searchable. Without it, capture only stores events for text search (Reflex
// falls back to lexical matching). nil is allowed — clears the attachment.
func (c *Capture) SetEmbedder(e llm.Embedder) {
	c.embedder = e
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

	// Journal first — durability before searchability. If the journal
	// write fails we surface the error; the storage write is a derived
	// projection and can be replayed from the journal later.
	if err := c.writeToJournal(event); err != nil {
		return err
	}

	// Storage write — makes the event findable by Reflex/cortex_search
	// in-process. Duplicate-id errors from StoreEvent mean the event
	// already landed (e.g. CaptureFromStdin → CaptureEvent paths both
	// firing on the same id); not fatal.
	if c.storage != nil {
		if err := c.storage.StoreEvent(event); err != nil {
			if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return fmt.Errorf("storage: %w", err)
			}
		}
		c.embedEvent(event)
	}
	return nil
}

// embedEvent asynchronously embeds a captured event and stores the vector so
// Reflex's semantic search can find it later. Fire-and-forget by design: the
// turn has already answered, embedding costs a network round-trip, and a
// failure must never break capture — so it runs in a bounded goroutine and is
// silently best-effort (text search remains the floor). A single-turn headless
// process may exit before the goroutine lands; that vector is simply absent
// until a future session re-captures or a backfill runs.
func (c *Capture) embedEvent(event *events.Event) {
	if c.embedder == nil || !c.embedder.IsEmbeddingAvailable() {
		return
	}
	text := searchableText(event)
	if strings.TrimSpace(text) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), embedWriteTimeout)
		defer cancel()
		vec, err := c.embedder.Embed(ctx, text)
		if err != nil || len(vec) == 0 {
			return
		}
		_ = c.storage.StoreEmbeddingVerified(event.ID, "event", vec, modelName(c.embedder), eventVerified(event))
	}()
}

// searchableText builds the string embedded for an event: the user's prompt
// (the retrieval key — where stated preferences and corrections live) joined
// with the captured result, so a semantically similar future prompt surfaces
// this turn. Mirrors what SearchByVector returns as content (ToolResult).
func searchableText(event *events.Event) string {
	var parts []string
	if event.ToolInput != nil {
		if p, ok := event.ToolInput["user_prompt"].(string); ok && p != "" {
			parts = append(parts, p)
		}
	}
	if event.ToolResult != "" {
		parts = append(parts, event.ToolResult)
	}
	return strings.Join(parts, "\n")
}

// modelName extracts the embedder's model id for provenance, when it exposes
// one. Empty otherwise — StoreEmbeddingWithModel treats "" as "unknown model".
func modelName(e llm.Embedder) string {
	if n, ok := e.(interface{ ModelName() string }); ok {
		return n.ModelName()
	}
	return ""
}

// eventVerified reads the capture's verified provenance bit (set by the loop
// from whether the turn ran tools). Absent/false → unverified.
func eventVerified(event *events.Event) bool {
	if event.Metadata == nil {
		return false
	}
	v, _ := event.Metadata["verified"].(bool)
	return v
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
