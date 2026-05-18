package ops

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/config"
)

// newSeededStorage builds a temp Storage with three embeddings whose
// vectors collide on known dimensions so the cosine similarity ordering
// is deterministic.
func newSeededStorage(t *testing.T) *storage.Storage {
	t.Helper()
	dir, err := os.MkdirTemp("", "cortex-ops-search-*")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s, err := storage.New(&config.Config{ContextDir: dir})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// vec_a aligns with [1,0,0,0]; vec_b with [0,1,0,0]; vec_c with [1,1,0,0]/sqrt(2)
	must := func(err error) {
		if err != nil {
			t.Fatalf("seed embedding: %v", err)
		}
	}
	must(s.StoreEmbedding("doc_a", "event", []float32{1, 0, 0, 0}))
	must(s.StoreEmbedding("doc_b", "event", []float32{0, 1, 0, 0}))
	must(s.StoreEmbedding("doc_c", "event", []float32{0.7071, 0.7071, 0, 0}))
	return s
}

func TestNewVectorSearchHandler_returnsRanked(t *testing.T) {
	s := newSeededStorage(t)
	h := NewVectorSearchHandler(VectorSearchConfig{Storage: s})

	// Query aligned with doc_a; doc_c should come second (~0.707 sim);
	// doc_b last (0 sim).
	got, err := h(context.Background(), map[string]any{
		"query_vector": []float32{1, 0, 0, 0},
		"limit":        3,
		"threshold":    0.0,
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ids, _ := got.Out["ids"].([]string)
	if len(ids) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(ids), ids)
	}
	if ids[0] != "doc_a" {
		t.Errorf("top result: expected doc_a, got %s (full=%v)", ids[0], ids)
	}
	if ids[1] != "doc_c" {
		t.Errorf("second result: expected doc_c, got %s (full=%v)", ids[1], ids)
	}
}

func TestNewVectorSearchHandler_respectsLimit(t *testing.T) {
	s := newSeededStorage(t)
	h := NewVectorSearchHandler(VectorSearchConfig{Storage: s})
	got, err := h(context.Background(), map[string]any{
		"query_vector": []float32{1, 0, 0, 0},
		"limit":        1,
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ids, _ := got.Out["ids"].([]string)
	if len(ids) != 1 {
		t.Errorf("expected 1 result, got %d: %v", len(ids), ids)
	}
}

func TestNewVectorSearchHandler_respectsThreshold(t *testing.T) {
	s := newSeededStorage(t)
	h := NewVectorSearchHandler(VectorSearchConfig{Storage: s})
	got, err := h(context.Background(), map[string]any{
		"query_vector": []float32{1, 0, 0, 0},
		"limit":        10,
		"threshold":    0.9, // only doc_a clears
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ids, _ := got.Out["ids"].([]string)
	if len(ids) != 1 || ids[0] != "doc_a" {
		t.Errorf("expected only [doc_a], got %v", ids)
	}
}

func TestNewVectorSearchHandler_acceptsFloat64Slice(t *testing.T) {
	// YAML-decoded vectors land as []any of float64 — make sure the
	// coercer accepts both shapes.
	s := newSeededStorage(t)
	h := NewVectorSearchHandler(VectorSearchConfig{Storage: s})
	_, err := h(context.Background(), map[string]any{
		"query_vector": []any{1.0, 0.0, 0.0, 0.0},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("expected []any coercion to succeed, got: %v", err)
	}
}

func TestNewVectorSearchHandler_noStorage(t *testing.T) {
	h := NewVectorSearchHandler(VectorSearchConfig{Storage: nil})
	_, err := h(context.Background(), map[string]any{"query_vector": []float32{1, 0}}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when storage is nil")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error should mention storage, got: %v", err)
	}
}

func TestNewVectorSearchHandler_missingVector(t *testing.T) {
	s := newSeededStorage(t)
	h := NewVectorSearchHandler(VectorSearchConfig{Storage: s})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when query_vector absent")
	}
}

func TestVectorSearchSpec_registersCleanly(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(VectorSearchSpec(VectorSearchConfig{Storage: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	spec, err := reg.Get("remember.vector_search")
	if err != nil {
		t.Fatalf("get remember.vector_search: %v", err)
	}
	if spec.Function != dag.FuncRemember || spec.Op != "vector_search" {
		t.Errorf("unexpected qualified name: %s", spec.QualifiedName())
	}
}
