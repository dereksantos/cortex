// search_vector.go — `cortex search-vector` exposes the primitive
// vector-search call (Storage.SearchByVector) as a CLI command so
// benchmarks (MTEB) can probe the retrieval machinery directly,
// bypassing the cognitive pipeline (Reflex/Reflect/Resolve).
//
// Two query modes:
//   - --text STRING:  embed the text then search (one-shot convenience;
//                     model + provider land in the JSON output for
//                     attribution, per eval-principles #3)
//   - --vector JSON:  search by a pre-computed vector (faster when the
//                     caller already has the embedding cached)
//
// --content-type filters results to a single bucket (e.g. "corpus")
// so MTEB doesn't have to defensively filter post-hoc.

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
)

func init() { Register(&SearchVectorCommand{}) }

// SearchVectorCommand implements `cortex search-vector`.
type SearchVectorCommand struct{}

// Name returns the command name.
func (c *SearchVectorCommand) Name() string { return "search-vector" }

// Description returns a brief description.
func (c *SearchVectorCommand) Description() string {
	return "Vector-search the workdir's storage (--text or --vector); JSON output"
}

// Execute parses flags and runs one search.
func (c *SearchVectorCommand) Execute(ctx *Context) error {
	flags := flag.NewFlagSet("search-vector", flag.ContinueOnError)
	workdir := flags.String("workdir", "", "Open storage rooted at <workdir>/.cortex (required)")
	text := flags.String("text", "", "Query text — embed and search in one shot")
	vectorJSON := flags.String("vector", "", "Pre-computed query vector as JSON array (alternative to --text)")
	topK := flags.Int("top-k", 10, "Maximum number of results to return")
	threshold := flags.Float64("threshold", 0.0, "Minimum similarity score to include (0.0 = no filter)")
	contentType := flags.String("content-type", "", "Filter results to a single content type bucket (empty = no filter)")
	if err := flags.Parse(ctx.Args); err != nil {
		return err
	}
	if *workdir == "" {
		return errors.New("--workdir is required")
	}
	hasText := *text != ""
	hasVec := *vectorJSON != ""
	if hasText == hasVec {
		return errors.New("exactly one of --text or --vector is required")
	}
	if *topK <= 0 {
		return errors.New("--top-k must be > 0")
	}

	_, st, err := openWorkdirContext(*workdir)
	if err != nil {
		return err
	}
	defer st.Close()

	var (
		qvec     []float32
		modelID  string
		provider string
	)
	if hasText {
		embedder, m, p := resolveEmbedder(ctx.Config)
		if embedder == nil || !embedder.IsEmbeddingAvailable() {
			return errors.New("no embedder available (install Ollama or Hugot)")
		}
		v, err := embedder.Embed(context.Background(), *text)
		if err != nil {
			return fmt.Errorf("embed query: %w", err)
		}
		qvec = v
		modelID = m
		provider = p
	} else {
		if err := json.Unmarshal([]byte(*vectorJSON), &qvec); err != nil {
			return fmt.Errorf("parse --vector: %w", err)
		}
		if len(qvec) == 0 {
			return errors.New("--vector decoded to an empty array")
		}
	}

	start := time.Now()
	results, err := st.SearchByVector(qvec, *topK, *threshold)
	if err != nil {
		return fmt.Errorf("search-vector: %w", err)
	}
	elapsed := time.Since(start)

	return emitSearchVectorJSON(os.Stdout, results, *contentType, *topK, elapsed, modelID, provider)
}

// searchVectorResult is one entry in the --json output. Content is
// included when the storage carries it (event-type embeddings) and
// omitted otherwise (corpus embeddings — MTEB looks up by ID).
type searchVectorResult struct {
	ContentID   string  `json:"content_id"`
	ContentType string  `json:"content_type"`
	Score       float64 `json:"score"`
	Content     string  `json:"content,omitempty"`
}

// searchVectorOutput is the top-level shape of `cortex search-vector`
// stdout. Stable contract; model+provider are populated only when the
// query came from --text (i.e. the CLI embedded it for the caller).
type searchVectorOutput struct {
	Results   []searchVectorResult `json:"results"`
	K         int                  `json:"k"`
	ElapsedMs int64                `json:"elapsed_ms"`
	Model     string               `json:"model,omitempty"`
	Provider  string               `json:"provider,omitempty"`
}

func emitSearchVectorJSON(
	w io.Writer,
	results []storage.VectorSearchResult,
	contentTypeFilter string,
	k int,
	elapsed time.Duration,
	model, provider string,
) error {
	filtered := make([]searchVectorResult, 0, len(results))
	for _, r := range results {
		if contentTypeFilter != "" && r.ContentType != contentTypeFilter {
			continue
		}
		filtered = append(filtered, searchVectorResult{
			ContentID:   r.ContentID,
			ContentType: r.ContentType,
			Score:       r.Similarity,
			Content:     r.Content,
		})
		if len(filtered) >= k {
			break
		}
	}
	return json.NewEncoder(w).Encode(searchVectorOutput{
		Results:   filtered,
		K:         k,
		ElapsedMs: elapsed.Milliseconds(),
		Model:     model,
		Provider:  provider,
	})
}
