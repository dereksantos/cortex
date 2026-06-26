package cognition

import (
	"context"
	"os"
	"testing"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
)

// TestRetrieve_OnPhaseFires verifies that Full-mode retrieval reports its phases
// to Query.OnPhase in order, so the interactive REPL can show live progress
// ("recalling…" → "reranking…") instead of dead air during the reranker call.
func TestRetrieve_OnPhaseFires(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-phase-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.New(&config.Config{ContextDir: tempDir})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	c, err := New(store, nil, nil, nil, &config.Config{ContextDir: tempDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var phases []string
	_, err = c.Retrieve(context.Background(), cognition.Query{
		Text:    "anything",
		Limit:   5,
		OnPhase: func(p string) { phases = append(phases, p) },
	}, cognition.Full)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	want := []string{"reflex", "rerank", "resolve"}
	if len(phases) != len(want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Errorf("phase[%d] = %q, want %q (full sequence %v)", i, phases[i], want[i], phases)
		}
	}
}

// TestRetrieve_FastMode_NoRerankPhase confirms Fast mode does not report a
// synchronous "rerank" phase (Reflect runs async there), so the status row
// never mislabels a fast turn.
func TestRetrieve_FastMode_NoRerankPhase(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-phase-fast-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.New(&config.Config{ContextDir: tempDir})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	c, err := New(store, nil, nil, nil, &config.Config{ContextDir: tempDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var phases []string
	if _, err := c.Retrieve(context.Background(), cognition.Query{
		Text:    "anything",
		Limit:   5,
		OnPhase: func(p string) { phases = append(phases, p) },
	}, cognition.Fast); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	for _, p := range phases {
		if p == "rerank" {
			t.Errorf("Fast mode reported a synchronous rerank phase; phases=%v", phases)
		}
	}
}
