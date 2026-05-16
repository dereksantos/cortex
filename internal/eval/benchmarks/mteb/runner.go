package mteb

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// defaultPassingNDCG is the per-task floor used to set TaskSuccess on
// the emitted CellResult. NFCorpus published numbers across embedders
// span ~0.20–0.35; 0.20 is "any retrieval better than near-random" and
// is the most conservative passing threshold consistent with the
// brief's "documented as 'any retrieval better than near-random'".
const defaultPassingNDCG = 0.20

// scoreK is the rank cutoff for all three reported metrics. MTEB's
// canonical headline number is @10.
const scoreK = 10

// embedderFactory builds the indexing + query embedder. Overridable in
// tests so unit runs don't need to hit Ollama or download Hugot models.
//
// Matches Cortex's standard embedder stack (see daemon/query/session
// command setups): Ollama primary, Hugot fallback. The first one that
// reports IsEmbeddingAvailable() wins. The returned modelID + provider
// land in CellResult.Notes so MTEB numbers are interpretable across
// runs with different embedders.
var embedderFactory = func() (llm.Embedder, string, string) {
	cfg := config.Default()
	ollama := llm.NewOllamaClient(cfg)
	if ollama.IsEmbeddingAvailable() {
		return ollama, cfg.OllamaEmbeddingModel, evalv2.ProviderOllama
	}
	hugot := llm.NewHugotEmbedder()
	if hugot.IsEmbeddingAvailable() {
		return hugot, llm.DefaultHugotModel, evalv2.ProviderLocal
	}
	// Return a deliberately-unavailable embedder so Run surfaces a
	// clean "no embedder" error rather than panicking on Embed.
	return hugot, llm.DefaultHugotModel, evalv2.ProviderLocal
}

// reflectFactory builds a reranker bound to provider. If provider is
// nil it falls back to whatever LLM the local environment offers
// (Ollama → Anthropic), so an operator running `--benchmark mteb
// --rerank` doesn't need to wire env.Provider through the CLI by hand.
// nil-on-no-LLM is intentional: the runner downgrades to no-rerank
// with a stderr note rather than failing the run.
var reflectFactory = func(provider llm.Provider) reranker {
	if provider == nil {
		provider = defaultLocalProvider()
	}
	if provider == nil {
		return nil
	}
	r := intcognition.NewReflect(provider)
	return reflectAdapter{r: r}
}

// defaultLocalProvider returns the first available LLM provider: Ollama
// first (most likely already running in the operator's dev env), then
// Anthropic if an API key is set. Returns nil when neither is
// available — the caller's --rerank request will be a no-op with a
// warning rather than a hard failure.
//
// Honors CORTEX_MTEB_RERANK_MODEL to override the Ollama model used for
// reranking without changing the global default (which other commands
// share). Useful for probing rerank quality across models without
// edits — e.g. CORTEX_MTEB_RERANK_MODEL=gemma2:2b.
func defaultLocalProvider() llm.Provider {
	cfg := config.Default()
	if m := os.Getenv("CORTEX_MTEB_RERANK_MODEL"); m != "" {
		cfg.OllamaModel = m
	}
	ollama := llm.NewOllamaClient(cfg)
	if ollama.IsAvailable() {
		return ollama
	}
	anthropic := llm.NewAnthropicClient(cfg)
	if anthropic.IsAvailable() {
		return anthropic
	}
	return nil
}

// reranker is the narrow interface the runner needs from cognition.Reflect.
// Defined here so tests can stub it without spinning up an LLM.
type reranker interface {
	Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error)
}

type reflectAdapter struct{ r *intcognition.Reflect }

func (a reflectAdapter) Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error) {
	return a.r.Reflect(ctx, q, candidates)
}

// RunOpts carries the runner-tunable knobs the CLI plumbs through.
// Embedded inside Instance.Payload at Run time via the runOpts type
// assertion fallback path below.
type RunOpts struct {
	Rerank   bool
	Embedder string // optional: forces a specific embedder model ID; empty = default
}

// Run executes one MTEB task instance end-to-end.
//
// Phase A flow:
//  1. Fresh <workdir>/.cortex/ for the per-instance storage.
//  2. Embed each corpus doc, StoreEmbedding(doc_id, "corpus", vec).
//  3. For each query: embed, SearchByVector(qvec, K, 0.0), take ids.
//  4. Optionally rerank top-K via cognition.Reflect.
//  5. Aggregate NDCG@10, MRR@10, Recall@10 over scored queries.
//  6. Emit ONE CellResult per task.
//
// Retrieval latency (NOT indexing) is what lands in LatencyMs; indexing
// is one-time amortization and would dwarf real retrieval cost.
func (b *Benchmark) Run(ctx context.Context, inst benchmarks.Instance, env benchmarks.Env) (*evalv2.CellResult, error) {
	task, ok := inst.Payload.(*Task)
	if !ok || task == nil {
		return nil, fmt.Errorf("mteb: Run got payload of type %T, want *Task", inst.Payload)
	}
	if env.Workdir == "" {
		tmp, err := os.MkdirTemp("", "mteb-run-*")
		if err != nil {
			return nil, fmt.Errorf("mkdir workdir: %w", err)
		}
		env.Workdir = tmp
	}

	store, err := openIsolatedStorage(env.Workdir)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	embedder, modelID, providerID := embedderFactory()
	if embedder == nil || !embedder.IsEmbeddingAvailable() {
		// Be loud: MTEB without an embedder measures nothing useful.
		return nil, errors.New("mteb: no embedder available; install Hugot or Ollama")
	}

	if env.Verbose {
		fmt.Fprintf(os.Stderr, "[mteb/%s] indexing %d docs with %s (%s)…\n",
			task.Name, len(task.Corpus), modelID, providerID)
	}

	indexStart := time.Now()
	if err := indexCorpus(ctx, store, embedder, task.Corpus, env.Verbose); err != nil {
		return nil, fmt.Errorf("index corpus: %w", err)
	}
	indexDur := time.Since(indexStart)

	// Resolve the reranker once per Run; the runner-level RunOpts decide
	// whether the Reflect call actually fires per query.
	var rr reranker
	runOpts := extractRunOpts(env)
	if runOpts.Rerank {
		rr = reflectFactory(env.Provider)
		if rr == nil && env.Verbose {
			fmt.Fprintln(os.Stderr, "[mteb] --rerank set but no provider; skipping rerank")
		}
	}

	retrieveStart := time.Now()
	var (
		sumNDCG, sumMRR, sumRecall float64
		scored                     int
	)
	for _, q := range task.Queries {
		gold := task.Qrels[q.ID]
		if len(gold) == 0 {
			// No judged docs for this query — MTEB convention is to skip.
			continue
		}
		ranked, err := retrieveForQuery(ctx, store, embedder, q.Text, scoreK)
		if err != nil {
			return nil, fmt.Errorf("retrieve %s: %w", q.ID, err)
		}
		if rr != nil {
			ranked = rerankTopK(ctx, rr, q, ranked, task.Corpus)
		}
		sumNDCG += NDCG(ranked, gold, scoreK)
		sumMRR += MRR(ranked, gold, scoreK)
		sumRecall += Recall(ranked, gold, scoreK)
		scored++
	}
	retrieveDur := time.Since(retrieveStart)

	meanNDCG := safeDiv(sumNDCG, scored)
	meanMRR := safeDiv(sumMRR, scored)
	meanRecall := safeDiv(sumRecall, scored)

	cell := cellResultBase()
	cell.RunID = uuid.NewString()
	cell.Timestamp = time.Now().UTC().Format(time.RFC3339)
	cell.Benchmark = Name
	cell.ScenarioID = "mteb/" + task.Name
	cell.Provider = providerID
	cell.Model = modelID
	cell.CortexVersion = "mteb-phase-a"
	cell.LatencyMs = retrieveDur.Milliseconds()
	cell.TaskSuccess = meanNDCG >= defaultPassingNDCG
	if cell.TaskSuccess {
		cell.TestsPassed = 1
	} else {
		cell.TestsFailed = 1
	}
	cell.Notes = fmt.Sprintf(
		"NDCG@%d=%.4f MRR@%d=%.4f Recall@%d=%.4f queries=%d embedder=%s/%s index=%s retrieve=%s rerank=%t",
		scoreK, meanNDCG, scoreK, meanMRR, scoreK, meanRecall,
		scored, providerID, modelID, indexDur.Round(time.Millisecond),
		retrieveDur.Round(time.Millisecond), rr != nil,
	)

	if env.Verbose {
		fmt.Fprintf(os.Stderr, "[mteb/%s] %s\n", task.Name, cell.Notes)
	}
	return cell, nil
}

// indexCorpus embeds and inserts every corpus doc. The skeleton's
// storage layer keys embeddings on (content_id, content_type). We use
// content_type="corpus" so MTEB embeddings never collide with the
// existing "event"/"insight" namespaces.
//
// Idempotent: if the storage already has at least len(corpus)
// embeddings (e.g. the operator re-ran against a warm workdir),
// indexing is skipped. The embeddings JSONL is append-only, so
// re-indexing would still produce correct results — but at ~75ms per
// embed call, skipping a no-op re-index saves multiple minutes on
// follow-up `--rerank` smoke comparisons.
func indexCorpus(ctx context.Context, store *storage.Storage, embedder llm.Embedder, corpus map[string]Doc, verbose bool) error {
	if existing, err := store.GetEmbeddingCount(); err == nil && existing >= len(corpus) {
		if verbose {
			fmt.Fprintf(os.Stderr, "[mteb] %d embeddings already in storage; skipping index\n", existing)
		}
		return nil
	}

	// Deterministic order for log progress and reproducibility.
	ids := make([]string, 0, len(corpus))
	for id := range corpus {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	progressEvery := 250
	for i, id := range ids {
		doc := corpus[id]
		vec, err := embedder.Embed(ctx, doc.Body())
		if err != nil {
			return fmt.Errorf("embed %s: %w", id, err)
		}
		if len(vec) == 0 {
			return fmt.Errorf("embed %s returned empty vector", id)
		}
		if err := store.StoreEmbedding(id, "corpus", vec); err != nil {
			return fmt.Errorf("store embed %s: %w", id, err)
		}
		if verbose && progressEvery > 0 && (i+1)%progressEvery == 0 {
			fmt.Fprintf(os.Stderr, "[mteb] indexed %d/%d\n", i+1, len(ids))
		}
	}
	return nil
}

// retrieveForQuery is the lowest-level retrieval call: embed the query,
// vector-search storage, return the ranked content IDs. Bypasses
// Reflex/Resolve so per-MTEB-query scoring measures the embedding +
// index substrate without agent-loop reordering.
func retrieveForQuery(ctx context.Context, store *storage.Storage, embedder llm.Embedder, text string, k int) ([]string, error) {
	qvec, err := embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	results, err := store.SearchByVector(qvec, k, 0.0)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(results))
	for _, r := range results {
		if r.ContentType != "corpus" {
			// Defensive: a fresh storage shouldn't have other content
			// types, but it's cheap to filter and protects metrics if a
			// future Cortex change introduces shared embeddings.
			continue
		}
		ids = append(ids, r.ContentID)
	}
	return ids, nil
}

// rerankTopK calls Reflect with corpus-doc bodies as Result.Content.
// Reflect returns the same Result set, reordered; we map back to ids
// for metric scoring. Errors fall through to the un-reranked order so
// a flaky LLM doesn't tank the whole run.
func rerankTopK(ctx context.Context, rr reranker, q Query, ranked []string, corpus map[string]Doc) []string {
	if len(ranked) == 0 {
		return ranked
	}
	candidates := make([]cognition.Result, 0, len(ranked))
	for _, id := range ranked {
		body := corpus[id].Body()
		candidates = append(candidates, cognition.Result{
			ID:      id,
			Content: body,
		})
	}
	reordered, err := rr.Reflect(ctx, cognition.Query{Text: q.Text, Limit: len(ranked)}, candidates)
	if err != nil || len(reordered) == 0 {
		log.Printf("[mteb] rerank failed for %s; using unreranked order: %v", q.ID, err)
		return ranked
	}
	out := make([]string, 0, len(reordered))
	for _, r := range reordered {
		out = append(out, r.ID)
	}
	return out
}

// openIsolatedStorage wires a Cortex Storage rooted at workdir/.cortex/
// so per-instance MTEB runs don't pollute the operator's repo store.
func openIsolatedStorage(workdir string) (*storage.Storage, error) {
	ctxDir := filepath.Join(workdir, ".cortex")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir context: %w", err)
	}
	cfg := &config.Config{ContextDir: ctxDir}
	return storage.New(cfg)
}

// extractRunOpts pulls RunOpts off env if the CLI layer stashed them
// there via a sentinel key (see commands/eval_benchmark.go). The
// benchmarks.Env struct has no extension point yet; using a sentinel
// is the smallest change consistent with the shared Env type.
func extractRunOpts(env benchmarks.Env) RunOpts {
	if v := os.Getenv(envRunOpts); v != "" {
		switch v {
		case "rerank":
			return RunOpts{Rerank: true}
		}
	}
	return RunOpts{}
}

// envRunOpts is the environment variable the CLI uses to pass
// per-Run knobs through to this package without modifying the shared
// benchmarks.Env struct. Phase A only needs a single flag; Phase B
// (when we have multiple tasks + per-task knobs) earns its own
// extension point on Env.
const envRunOpts = "CORTEX_MTEB_RUNOPTS"

func safeDiv(a float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return a / float64(n)
}
