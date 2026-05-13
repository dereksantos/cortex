package commands

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// TestRetrievalStatsFromStorage drives the Z1 unification contract: the
// watch UI's RetrievalStats view is now sourced from storage projections
// of resolve.retrieval entries, not from retrieval_stats.json.
func TestRetrievalStatsFromStorage(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{ContextDir: tempDir, ProjectRoot: tempDir}
	if err := os.MkdirAll(filepath.Join(tempDir, "db"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	t.Run("empty store returns nil", func(t *testing.T) {
		if got := retrievalStatsFromStorage(store); got != nil {
			t.Errorf("want nil for empty store, got %+v", got)
		}
	})

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordRetrieval(&storage.Retrieval{
		QueryText:   "authentication flow",
		Decision:    "inject",
		ResultCount: 5,
		Mode:        "full",
		ResolveMs:   42,
		TotalMs:     180,
		RecordedAt:  now,
	}); err != nil {
		t.Fatalf("RecordRetrieval: %v", err)
	}

	got := retrievalStatsFromStorage(store)
	if got == nil {
		t.Fatal("want stats, got nil")
	}
	if got.LastQuery != "authentication flow" {
		t.Errorf("LastQuery=%q want authentication flow", got.LastQuery)
	}
	if got.LastMode != "full" {
		t.Errorf("LastMode=%q want full", got.LastMode)
	}
	if got.LastResolveMs != 42 {
		t.Errorf("LastResolveMs=%d want 42", got.LastResolveMs)
	}
	if got.LastResults != 5 {
		t.Errorf("LastResults=%d want 5", got.LastResults)
	}
	if got.LastDecision != "inject" {
		t.Errorf("LastDecision=%q want inject", got.LastDecision)
	}
	if got.TotalRetrievals != 1 {
		t.Errorf("TotalRetrievals=%d want 1", got.TotalRetrievals)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt=%v want %v", got.UpdatedAt, now)
	}
}

// TestRetrievalStatsFromStorage_NilStore documents the nil-store guard
// so the watch UI degrades gracefully when invoked outside a configured
// project (e.g., on bootstrap before Storage opens).
func TestRetrievalStatsFromStorage_NilStore(t *testing.T) {
	if got := retrievalStatsFromStorage(nil); got != nil {
		t.Errorf("want nil for nil store, got %+v", got)
	}
}
