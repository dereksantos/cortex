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
	"bufio"
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

// Execute parses flags and dispatches to one of the three modes:
// bulk (NDJSON in, count out), store (single text → stored), or
// default (single text → vector JSON).
func (c *EmbedCommand) Execute(ctx *Context) error {
	flags := flag.NewFlagSet("embed", flag.ContinueOnError)
	text := flags.String("text", "", "Text to embed (required unless --bulk)")
	workdir := flags.String("workdir", "", "Open storage rooted at <workdir>/.cortex (required with --store or --bulk)")
	store := flags.Bool("store", false, "Store the resulting vector instead of just emitting it")
	docID := flags.String("doc-id", "", "Content ID to store the embedding under (required when --store is set without --bulk)")
	contentType := flags.String("content-type", "corpus", "Content type bucket for stored embeddings")
	bulk := flags.Bool("bulk", false, "Read NDJSON {doc_id, content_type?, text} from stdin and store each. Implies --store.")
	if err := flags.Parse(ctx.Args); err != nil {
		return err
	}

	// Validate flags BEFORE resolving the embedder so a typo in a
	// benchmark wrapper still surfaces a clear flag error even on
	// machines without Ollama/Hugot installed (the embedder lookup
	// would otherwise mask the real issue).
	if *bulk {
		if *workdir == "" {
			return errors.New("--bulk requires --workdir")
		}
	} else {
		if strings.TrimSpace(*text) == "" {
			return errors.New("--text is required (or use --bulk for NDJSON stdin)")
		}
		if *store {
			if *workdir == "" {
				return errors.New("--store requires --workdir")
			}
			if *docID == "" {
				return errors.New("--store requires --doc-id")
			}
		}
	}

	embedder, modelID, providerID := resolveEmbedder(ctx.Config)
	if embedder == nil {
		return errors.New("no embedder available (install Ollama or Hugot)")
	}

	if *bulk {
		return executeBulkEmbed(*workdir, *contentType, embedder, modelID, providerID, os.Stdin, os.Stdout)
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

// bulkEmbedRequest is one NDJSON line in the --bulk stdin stream.
// content_type defaults to the CLI's --content-type flag when omitted
// so callers indexing a homogeneous corpus only need to declare it once.
type bulkEmbedRequest struct {
	DocID       string `json:"doc_id"`
	ContentType string `json:"content_type,omitempty"`
	Text        string `json:"text"`
}

// bulkEmbedSummary is the JSON object emitted on stdout after a bulk
// run completes. count carries the number of embeddings written so
// callers can verify nothing got silently dropped.
type bulkEmbedSummary struct {
	Stored   int    `json:"stored"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Dim      int    `json:"dim"`
}

// executeBulkEmbed reads NDJSON one line at a time, embeds + stores
// each, and emits a single summary JSON on success. The first parse or
// embed failure aborts the batch with a line-numbered error — partial
// state is left in storage (callers needing atomicity should write to
// a fresh workdir).
func executeBulkEmbed(workdir, defaultContentType string, embedder llm.Embedder, model, provider string, r io.Reader, w io.Writer) error {
	_, st, err := openWorkdirContext(workdir)
	if err != nil {
		return err
	}
	defer st.Close()

	scanner := bufio.NewScanner(r)
	// Corpus docs can be multi-KB (NFCorpus paragraphs run ~1-2KB);
	// allow up to 16MB per line to cover worst-case scientific abstracts.
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)

	var (
		count int
		dim   int
		line  int
	)
	ctx := context.Background()
	for scanner.Scan() {
		line++
		b := scanner.Bytes()
		if len(b) == 0 {
			continue
		}
		var req bulkEmbedRequest
		if err := json.Unmarshal(b, &req); err != nil {
			return fmt.Errorf("line %d: parse: %w", line, err)
		}
		if req.DocID == "" {
			return fmt.Errorf("line %d: doc_id is required", line)
		}
		if strings.TrimSpace(req.Text) == "" {
			return fmt.Errorf("line %d: text is required", line)
		}
		ct := req.ContentType
		if ct == "" {
			ct = defaultContentType
		}
		vec, err := embedder.Embed(ctx, req.Text)
		if err != nil {
			return fmt.Errorf("line %d: embed %q: %w", line, req.DocID, err)
		}
		if len(vec) == 0 {
			return fmt.Errorf("line %d: embed %q returned empty vector", line, req.DocID)
		}
		if dim == 0 {
			dim = len(vec)
		}
		if err := st.StoreEmbedding(req.DocID, ct, vec); err != nil {
			return fmt.Errorf("line %d: store %q: %w", line, req.DocID, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return json.NewEncoder(w).Encode(bulkEmbedSummary{
		Stored:   count,
		Model:    model,
		Provider: provider,
		Dim:      dim,
	})
}

// resolveEmbedder mirrors cmd/cortex/commands/ingest.go's embedder
// construction: Ollama primary, Hugot fallback. Returns the embedder
// ready-to-use plus the model + provider names so callers can stamp
// them on output.
//
// Returns (nil, "", "") when neither embedder is available so the
// caller's nil-check is the single source of truth — that lets
// staticcheck reason about the comparison correctly.
//
// cfg may be nil; we fall back to config.Default() for the canonical
// embedding-model name.
func resolveEmbedder(cfg *config.Config) (llm.Embedder, string, string) {
	if cfg == nil {
		cfg = config.Default()
	}
	if ollama := llm.NewOllamaClient(cfg); ollama.IsEmbeddingAvailable() {
		return ollama, cfg.OllamaEmbeddingModel, "ollama"
	}
	if hugot := llm.NewHugotEmbedder(); hugot.IsEmbeddingAvailable() {
		return hugot, llm.DefaultHugotModel, "local"
	}
	return nil, "", ""
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
