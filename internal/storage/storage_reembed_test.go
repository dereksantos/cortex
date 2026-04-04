package storage

import (
	"fmt"
	"os"
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
)

func testStorage(t *testing.T) *Storage {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := config.Default()
	cfg.ContextDir = tmpDir

	store, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return store
}

func TestModelNameMigration(t *testing.T) {
	store := testStorage(t)

	// Verify model_name column exists by attempting an insert with it
	_, err := store.db.Exec(`
		INSERT INTO embeddings (content_id, content_type, vector, model_name)
		VALUES (?, ?, ?, ?)
	`, "test-id", "event", []byte{0x00}, "test-model")
	if err != nil {
		t.Fatalf("model_name column should exist after migration: %v", err)
	}

	// Verify we can read it back
	var modelName string
	err = store.db.QueryRow("SELECT model_name FROM embeddings WHERE content_id = ?", "test-id").Scan(&modelName)
	if err != nil {
		t.Fatalf("failed to read model_name: %v", err)
	}
	if modelName != "test-model" {
		t.Errorf("expected model_name 'test-model', got %q", modelName)
	}
}

func TestStoreEmbeddingWithModel(t *testing.T) {
	store := testStorage(t)

	vec := []float32{0.1, 0.2, 0.3}
	err := store.StoreEmbeddingWithModel("content-1", "event", vec, "all-MiniLM-L12-v2")
	if err != nil {
		t.Fatalf("StoreEmbeddingWithModel failed: %v", err)
	}

	// Verify model_name was stored
	var modelName string
	err = store.db.QueryRow("SELECT model_name FROM embeddings WHERE content_id = ?", "content-1").Scan(&modelName)
	if err != nil {
		t.Fatalf("failed to query model_name: %v", err)
	}
	if modelName != "all-MiniLM-L12-v2" {
		t.Errorf("expected model_name 'all-MiniLM-L12-v2', got %q", modelName)
	}

	// Verify upsert works (INSERT OR REPLACE)
	vec2 := []float32{0.4, 0.5, 0.6}
	err = store.StoreEmbeddingWithModel("content-1", "event", vec2, "new-model")
	if err != nil {
		t.Fatalf("StoreEmbeddingWithModel upsert failed: %v", err)
	}

	err = store.db.QueryRow("SELECT model_name FROM embeddings WHERE content_id = ?", "content-1").Scan(&modelName)
	if err != nil {
		t.Fatalf("failed to query updated model_name: %v", err)
	}
	if modelName != "new-model" {
		t.Errorf("expected updated model_name 'new-model', got %q", modelName)
	}
}

func TestGetAllEmbeddingContentIDs(t *testing.T) {
	store := testStorage(t)

	// Initially empty
	contents, err := store.GetAllEmbeddingContentIDs()
	if err != nil {
		t.Fatalf("GetAllEmbeddingContentIDs failed: %v", err)
	}
	if len(contents) != 0 {
		t.Errorf("expected 0 contents, got %d", len(contents))
	}

	// Store several embeddings
	testCases := []struct {
		id      string
		ctype   string
	}{
		{"event-1", "event"},
		{"event-2", "event"},
		{"insight-1", "insight"},
	}

	for _, tc := range testCases {
		err := store.StoreEmbedding(tc.id, tc.ctype, []float32{0.1, 0.2})
		if err != nil {
			t.Fatalf("StoreEmbedding failed for %s: %v", tc.id, err)
		}
	}

	// Verify all returned
	contents, err = store.GetAllEmbeddingContentIDs()
	if err != nil {
		t.Fatalf("GetAllEmbeddingContentIDs failed: %v", err)
	}
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(contents))
	}

	// Verify content types are correct
	found := make(map[string]string)
	for _, c := range contents {
		found[c.ContentID] = c.ContentType
	}
	for _, tc := range testCases {
		if got, ok := found[tc.id]; !ok {
			t.Errorf("missing content ID %s", tc.id)
		} else if got != tc.ctype {
			t.Errorf("content ID %s: expected type %q, got %q", tc.id, tc.ctype, got)
		}
	}
}

func TestGetEmbeddingCount(t *testing.T) {
	store := testStorage(t)

	count, err := store.GetEmbeddingCount()
	if err != nil {
		t.Fatalf("GetEmbeddingCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Add some embeddings
	for i := 0; i < 5; i++ {
		store.StoreEmbedding(fmt.Sprintf("id-%d", i), "event", []float32{0.1})
	}

	count, err = store.GetEmbeddingCount()
	if err != nil {
		t.Fatalf("GetEmbeddingCount failed: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5, got %d", count)
	}
}
