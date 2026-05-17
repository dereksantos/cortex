package capture

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	pkgcognition "github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// TestCapture_RoundTripsThroughCortex is the smoke test for the
// auto-capture → cortex_search pipeline: a captured event under a
// shared Storage must be findable by Cortex.Retrieve against the SAME
// Storage. Without this round-trip working, the REPL's auto-capture
// plumbing is invisible to cortex_search (the symptom we saw on the
// first cloud ABR run).
func TestCapture_RoundTripsThroughCortex(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-capture-rt-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir, ProjectRoot: tempDir}

	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	cortex, err := intcognition.New(store, nil, nil, cfg)
	if err != nil {
		t.Fatalf("cognition.New: %v", err)
	}

	cap := NewWithStorage(cfg, store)

	// Capture an event the way REPL captureTurn does: EventToolUse
	// with content in ToolInput["content"] and a separate prompt /
	// result pair. Words like "authentication" / "JWT" should be the
	// retrievable signal.
	event := &events.Event{
		ID:        "evt-rt-1",
		Source:    events.SourceGeneric,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "cortex-repl",
		ToolInput: map[string]interface{}{
			"type":        "repl_turn",
			"content":     "User: we use JWT for authentication, not sessions\nAssistant: Got it, captured.",
			"user_prompt": "we use JWT for authentication, not sessions",
		},
		ToolResult: "captured the JWT auth decision",
		Context: events.EventContext{
			SessionID:   "session-rt-1",
			ProjectPath: tempDir,
		},
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent: %v", err)
	}

	// Now retrieve. If the auto-capture wire works, this should NOT
	// be empty — the captured event mentions "authentication" and
	// "JWT" multiple times, which is the canonical "this would match
	// a Reflex text search" signal.
	res, err := cortex.Retrieve(context.Background(), pkgcognition.Query{
		Text:  "authentication",
		Limit: 10,
	}, pkgcognition.Fast)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res == nil || len(res.Results) == 0 {
		t.Fatalf("retrieval returned empty — captures are not visible to retrieval. The auto-capture wire is broken.")
	}

	// Verify the matched result is the one we captured.
	found := false
	for _, r := range res.Results {
		if strings.Contains(r.Content, "JWT") || strings.Contains(r.Content, "authentication") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("retrieval returned %d result(s) but none mention JWT/authentication; content didn't carry through capture→storage→reflex", len(res.Results))
		for i, r := range res.Results {
			t.Logf("  result %d: category=%s content=%q", i, r.Category, r.Content)
		}
	}
}
