// embed.go — `cortex embed` exposes the embedding+vector-store substrate
// as a CLI primitive so benchmarks (MTEB) and scripts can build / probe
// the index without importing internal/storage or internal/llm.
//
// Two modes:
//   - default:   embed text, print the vector + model metadata as JSON
//   - --store:   embed text, write (doc_id, content_type, vec) into the
//                workdir's storage, print a small JSON confirmation
//
// Embedder resolution mirrors the daemon/ingest path: Ollama primary,
// Hugot fallback. Both pieces of metadata (model, provider) land in
// every output so downstream readers can attribute the score later
// (per eval-principles #3: emit versioning metadata).

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

func init() { Register(&EmbedCommand{}) }

// EmbedCommand implements `cortex embed`.
type EmbedCommand struct{}

// Name returns the command name.
func (c *EmbedCommand) Name() string { return "embed" }

// Description returns a brief description.
func (c *EmbedCommand) Description() string {
	return "Embed text to a vector (and optionally store under doc_id/content_type)"
}

// Execute parses flags and runs one of the two modes.
func (c *EmbedCommand) Execute(ctx *Context) error {
	flags := flag.NewFlagSet("embed", flag.ContinueOnError)
	text := flags.String("text", "", "Text to embed (required)")
	workdir := flags.String("workdir", "", "Open storage rooted at <workdir>/.cortex (required when --store is set)")
	store := flags.Bool("store", false, "Store the resulting vector instead of just emitting it")
	docID := flags.String("doc-id", "", "Content ID to store the embedding under (required when --store is set)")
	contentType := flags.String("content-type", "corpus", "Content type bucket for the stored embedding (only used with --store)")
	if err := flags.Parse(ctx.Args); err != nil {
		return err
	}
	if strings.TrimSpace(*text) == "" {
		return errors.New("--text is required")
	}
	if *store {
		if *workdir == "" {
			return errors.New("--store requires --workdir")
		}
		if *docID == "" {
			return errors.New("--store requires --doc-id")
		}
	}

	embedder, modelID, providerID := resolveEmbedder(ctx.Config)
	if embedder == nil || !embedder.IsEmbeddingAvailable() {
		return errors.New("no embedder available (install Ollama or Hugot)")
	}

	vec, err := embedder.Embed(context.Background(), *text)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if len(vec) == 0 {
		return errors.New("embedder returned empty vector")
	}

	if *store {
		_, st, err := openWorkdirContext(*workdir)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.StoreEmbedding(*docID, *contentType, vec); err != nil {
			return fmt.Errorf("store embedding: %w", err)
		}
		return emitEmbedStoreJSON(os.Stdout, *docID, *contentType, modelID, providerID, len(vec))
	}

	return emitEmbedJSON(os.Stdout, vec, modelID, providerID)
}

// resolveEmbedder mirrors internal/eval/benchmarks/mteb's embedderFactory
// and cmd/cortex/commands/ingest.go's embedder construction: Ollama
// primary, Hugot fallback. Returns the embedder ready-to-use plus the
// model + provider names so callers can stamp them on output.
//
// cfg may be nil; we fall back to config.Default() to pick up the
// canonical embedding-model name.
func resolveEmbedder(cfg *config.Config) (llm.Embedder, string, string) {
	if cfg == nil {
		cfg = config.Default()
	}
	ollama := llm.NewOllamaClient(cfg)
	if ollama.IsEmbeddingAvailable() {
		return ollama, cfg.OllamaEmbeddingModel, "ollama"
	}
	hugot := llm.NewHugotEmbedder()
	if hugot.IsEmbeddingAvailable() {
		return hugot, llm.DefaultHugotModel, "local"
	}
	// Return the unavailable one so the caller's IsEmbeddingAvailable()
	// check fails cleanly instead of nil-panicking.
	return hugot, llm.DefaultHugotModel, "local"
}

// embedJSONOutput is the contract emitted by `cortex embed` (no --store).
// Stable keys; vector is the full float slice (no truncation).
type embedJSONOutput struct {
	Vector   []float32 `json:"vector"`
	Dim      int       `json:"dim"`
	Model    string    `json:"model"`
	Provider string    `json:"provider"`
}

// embedStoreJSONOutput is the contract emitted by `cortex embed --store`.
// Smaller than embedJSONOutput because the vector itself isn't needed by
// the caller — it's already in storage.
type embedStoreJSONOutput struct {
	Stored      bool   `json:"stored"`
	DocID       string `json:"doc_id"`
	ContentType string `json:"content_type"`
	Dim         int    `json:"dim"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
}

func emitEmbedJSON(w io.Writer, vec []float32, model, provider string) error {
	return json.NewEncoder(w).Encode(embedJSONOutput{
		Vector:   vec,
		Dim:      len(vec),
		Model:    model,
		Provider: provider,
	})
}

func emitEmbedStoreJSON(w io.Writer, docID, contentType, model, provider string, dim int) error {
	return json.NewEncoder(w).Encode(embedStoreJSONOutput{
		Stored:      true,
		DocID:       docID,
		ContentType: contentType,
		Dim:         dim,
		Model:       model,
		Provider:    provider,
	})
}

