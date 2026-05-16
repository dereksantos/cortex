package mteb

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
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

// corpusContentType is the bucket every MTEB corpus doc lands in.
// Centralized so the indexing and retrieval sides can't drift.
const corpusContentType = "corpus"

// Run executes one MTEB task instance end-to-end via the cortex CLI:
//
//  1. Resolve the cortex binary (per eval-principles #1, no in-process
//     calls into internal/storage or internal/cognition).
//  2. Bulk-embed the corpus into <env.Workdir>/.cortex via
//     `cortex embed --bulk --workdir`.
//  3. For each query: `cortex search-vector --text --top-k 10
//     --content-type corpus --workdir` returns ranked content IDs.
//  4. Aggregate NDCG@10, MRR@10, Recall@10 over scored queries.
//  5. Emit ONE CellResult per task.
//
// Retrieval latency (NOT indexing) lands in LatencyMs; indexing is
// one-time amortization and would dwarf real retrieval cost.
//
// --rerank is currently a no-op when running through the CLI: a
// `cortex rerank` command does not yet exist. See task list for the
// follow-up. The flag still parses cleanly so operators don't see a
// regression in --help.
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

	binary, err := benchmarks.ResolveCortexBinary()
	if err != nil {
		return nil, fmt.Errorf("mteb: %w", err)
	}

	if env.Verbose {
		fmt.Fprintf(os.Stderr, "[mteb/%s] indexing %d docs via cortex embed --bulk…\n",
			task.Name, len(task.Corpus))
	}

	indexStart := time.Now()
	summary, err := indexCorpusViaCLI(ctx, binary, env.Workdir, task.Corpus)
	if err != nil {
		return nil, fmt.Errorf("index corpus: %w", err)
	}
	indexDur := time.Since(indexStart)

	runOpts := extractRunOpts(env)
	if runOpts.Rerank && env.Verbose {
		fmt.Fprintln(os.Stderr, "[mteb] --rerank requested but cortex rerank CLI is not yet implemented; running without reranking")
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
		ranked, err := retrieveForQuery(ctx, binary, env.Workdir, q.Text, scoreK)
		if err != nil {
			return nil, fmt.Errorf("retrieve %s: %w", q.ID, err)
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
	cell.Provider = summary.Provider
	cell.Model = summary.Model
	cell.CortexVersion = "mteb-phase-a"
	cell.LatencyMs = retrieveDur.Milliseconds()
	cell.TaskSuccess = meanNDCG >= defaultPassingNDCG
	if cell.TaskSuccess {
		cell.TestsPassed = 1
	} else {
		cell.TestsFailed = 1
	}
	cell.Notes = fmt.Sprintf(
		"NDCG@%d=%.4f MRR@%d=%.4f Recall@%d=%.4f queries=%d embedder=%s/%s index=%s retrieve=%s rerank=false",
		scoreK, meanNDCG, scoreK, meanMRR, scoreK, meanRecall,
		scored, summary.Provider, summary.Model,
		indexDur.Round(time.Millisecond),
		retrieveDur.Round(time.Millisecond),
	)

	if env.Verbose {
		fmt.Fprintf(os.Stderr, "[mteb/%s] %s\n", task.Name, cell.Notes)
	}
	return cell, nil
}

// indexCorpusViaCLI builds the bulk-embed request list from the corpus
// (in deterministic ID order for reproducibility) and pipes it to
// `cortex embed --bulk`. Returns the summary so the caller can stamp
// embedder model + provider on the CellResult.
//
// Empty corpus returns a zero summary — the caller will still produce
// a CellResult, with zero queries scored, which the rollup layer
// interprets as a no-signal run.
func indexCorpusViaCLI(ctx context.Context, binary, workdir string, corpus map[string]Doc) (*benchmarks.EmbedBulkSummary, error) {
	if len(corpus) == 0 {
		return &benchmarks.EmbedBulkSummary{}, nil
	}
	ids := make([]string, 0, len(corpus))
	for id := range corpus {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	reqs := make([]benchmarks.EmbedBulkRequest, 0, len(ids))
	for _, id := range ids {
		reqs = append(reqs, benchmarks.EmbedBulkRequest{
			DocID:       id,
			ContentType: corpusContentType,
			Text:        corpus[id].Body(),
		})
	}
	summary, err := benchmarks.RunEmbedBulk(ctx, binary, workdir, corpusContentType, reqs)
	if err != nil {
		return nil, err
	}
	if summary.Stored != len(reqs) {
		return summary, fmt.Errorf("indexed %d/%d docs (CLI dropped %d)", summary.Stored, len(reqs), len(reqs)-summary.Stored)
	}
	return summary, nil
}

// retrieveForQuery runs `cortex search-vector --text --content-type
// corpus --top-k k` and returns the ranked content IDs. ContentType is
// hard-pinned to "corpus" so a future shared store with mixed embeddings
// can't leak unrelated content into MTEB scoring.
func retrieveForQuery(ctx context.Context, binary, workdir, text string, k int) ([]string, error) {
	out, err := benchmarks.RunSearchVector(ctx, binary, benchmarks.SearchVectorOpts{
		Workdir:     workdir,
		Text:        text,
		TopK:        k,
		ContentType: corpusContentType,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Results))
	for _, r := range out.Results {
		ids = append(ids, r.ContentID)
	}
	return ids, nil
}

// RunOpts carries the runner-tunable knobs the CLI plumbs through.
// Stashed via env var so the shared benchmarks.Env doesn't need an
// MTEB-specific extension point.
type RunOpts struct {
	Rerank   bool
	Embedder string // reserved for future per-task embedder override
}

// extractRunOpts pulls RunOpts off env if the CLI layer stashed them
// via the CORTEX_MTEB_RUNOPTS sidechannel.
func extractRunOpts(_ benchmarks.Env) RunOpts {
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
// benchmarks.Env struct.
const envRunOpts = "CORTEX_MTEB_RUNOPTS"

func safeDiv(a float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return a / float64(n)
}
