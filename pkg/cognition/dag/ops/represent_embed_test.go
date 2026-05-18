package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// stubEmbedder is a deterministic embedder for unit tests. The vector
// is just the byte values of the text padded/truncated to dim 4 —
// enough to verify shape + plumbing without dragging in a real model.
type stubEmbedder struct {
	dim       int
	available bool
	embedErr  error
}

func (s *stubEmbedder) IsEmbeddingAvailable() bool { return s.available }

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if s.embedErr != nil {
		return nil, s.embedErr
	}
	dim := s.dim
	if dim == 0 {
		dim = 4
	}
	out := make([]float32, dim)
	for i := 0; i < dim && i < len(text); i++ {
		out[i] = float32(text[i])
	}
	return out, nil
}

func TestNewEmbedHandler_singleText(t *testing.T) {
	h := NewEmbedHandler(EmbedConfig{Embedder: &stubEmbedder{dim: 4, available: true}})
	got, err := h(context.Background(), map[string]any{"text": "hello"}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vec, ok := got.Out["vector"].([]float32)
	if !ok {
		t.Fatalf("expected []float32 vector, got %T", got.Out["vector"])
	}
	if len(vec) != 4 {
		t.Fatalf("expected dim=4, got %d", len(vec))
	}
	if dim, _ := got.Out["dim"].(int); dim != 4 {
		t.Fatalf("expected dim out = 4, got %v", got.Out["dim"])
	}
}

func TestNewEmbedHandler_batchTexts(t *testing.T) {
	h := NewEmbedHandler(EmbedConfig{Embedder: &stubEmbedder{dim: 3, available: true}})
	got, err := h(context.Background(), map[string]any{"texts": []string{"a", "bb", "ccc"}}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vecs, ok := got.Out["vectors"].([][]float32)
	if !ok {
		t.Fatalf("expected [][]float32 vectors, got %T", got.Out["vectors"])
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 3 {
			t.Errorf("vector %d: expected dim=3, got %d", i, len(v))
		}
	}
}

func TestNewEmbedHandler_anyTextsSlice(t *testing.T) {
	// YAML decodes []string as []any of strings — make sure we coerce.
	h := NewEmbedHandler(EmbedConfig{Embedder: &stubEmbedder{dim: 2, available: true}})
	got, err := h(context.Background(), map[string]any{"texts": []any{"x", "y"}}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vecs, _ := got.Out["vectors"].([][]float32)
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
}

func TestNewEmbedHandler_noInput(t *testing.T) {
	h := NewEmbedHandler(EmbedConfig{Embedder: &stubEmbedder{available: true}})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "text") {
		t.Errorf("error should mention text input, got: %v", err)
	}
}

func TestNewEmbedHandler_noEmbedder(t *testing.T) {
	h := NewEmbedHandler(EmbedConfig{Embedder: nil})
	_, err := h(context.Background(), map[string]any{"text": "anything"}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when embedder is nil")
	}
	if !strings.Contains(err.Error(), "no embedder") {
		t.Errorf("error should mention missing embedder, got: %v", err)
	}
}

func TestNewEmbedHandler_unavailableEmbedder(t *testing.T) {
	h := NewEmbedHandler(EmbedConfig{Embedder: &stubEmbedder{available: false}})
	_, err := h(context.Background(), map[string]any{"text": "x"}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when embedder is unavailable")
	}
}

func TestEmbedSpec_registersCleanly(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(EmbedSpec(EmbedConfig{Embedder: &stubEmbedder{available: true}})); err != nil {
		t.Fatalf("register: %v", err)
	}
	spec, err := reg.Get("represent.embed")
	if err != nil {
		t.Fatalf("get represent.embed: %v", err)
	}
	if spec.Function != dag.FuncRepresent || spec.Op != "embed" {
		t.Errorf("unexpected qualified name: %s", spec.QualifiedName())
	}
	if spec.Cost.Tokens != 0 {
		t.Errorf("mechanical op should report 0 token cost hint, got %d", spec.Cost.Tokens)
	}
}
