package mteb

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

const (
	// Name is the registry key.
	Name = "mteb"

	// TaskNFCorpus is the only task name accepted in this PR. Other BEIR
	// retrieval tasks (SciFact, FiQA, …) follow in Phase B.
	TaskNFCorpus = "NFCorpus"
)

func init() {
	benchmarks.Register(Name, func() benchmarks.Benchmark { return &Benchmark{} })
}

// Benchmark is the MTEB retrieval benchmark wired against NFCorpus.
// Stateless; the runner constructs an isolated Cortex storage per Run.
type Benchmark struct{}

// Name returns the registry key.
func (b *Benchmark) Name() string { return Name }

// Load fetches the requested MTEB task and returns a single Instance.
// Per the Phase A brief, only "NFCorpus" (or an empty Subset, which
// defaults to NFCorpus) is accepted. Unknown tasks return a clear
// "Phase B" error so operators know the gap rather than getting a
// cryptic upstream 404.
//
// Limit caps the number of *queries* scored, not the corpus size. The
// corpus stays intact so retrieval is not artificially easy.
func (b *Benchmark) Load(ctx context.Context, opts benchmarks.LoadOpts) ([]benchmarks.Instance, error) {
	task := opts.Subset
	if task == "" {
		task = TaskNFCorpus
	}
	if task != TaskNFCorpus {
		return nil, fmt.Errorf("mteb task %q not supported in this PR (Phase B)", task)
	}

	payload, err := loadNFCorpus(ctx)
	if err != nil {
		return nil, err
	}

	// NFCorpus's queries.jsonl mixes train/dev/test; only test-split
	// queries have entries in qrels/test.tsv. Filter to the judged set
	// before --limit so a small --limit doesn't accidentally score
	// nothing because the first N happened to be train-only.
	payload.Queries = filterJudgedQueries(payload.Queries, payload.Qrels)

	if opts.Limit > 0 && opts.Limit < len(payload.Queries) {
		payload.Queries = payload.Queries[:opts.Limit]
	}

	return []benchmarks.Instance{{
		ID:      "mteb/" + payload.Name,
		Payload: payload,
	}}, nil
}

// Run is implemented in runner.go.

// ApplyArgs implements benchmarks.ArgsApplier so the CLI dispatcher
// doesn't need a switch-on-name to wire MTEB's flags.
//
// Phase A flags:
//   - --tasks NAME       → opts.Subset (default NFCorpus; others rejected at Load)
//   - --rerank           → CORTEX_MTEB_RUNOPTS=rerank (sidechannel read by runner;
//     env-var lives only for the duration of this process, which is the
//     same as the CLI invocation)
//   - --embedder ID      → opts.Filter["embedder"] (reserved, not yet plumbed)
//   - -m / --model       → rejected with mteb-specific guidance: MTEB measures
//     the embedder substrate, not the LLM
func (b *Benchmark) ApplyArgs(args []string, opts *benchmarks.LoadOpts) error {
	if opts.Filter == nil {
		opts.Filter = map[string]string{}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tasks":
			if i+1 >= len(args) {
				return errors.New("--tasks requires a value")
			}
			opts.Subset = args[i+1]
			i++
		case "--rerank":
			if err := os.Setenv("CORTEX_MTEB_RUNOPTS", "rerank"); err != nil {
				return err
			}
		case "--embedder":
			if i+1 >= len(args) {
				return errors.New("--embedder requires a value")
			}
			opts.Filter["embedder"] = args[i+1]
			i++
		case "-m", "--model":
			return errors.New("--model is not valid with --benchmark mteb " +
				"(MTEB measures the embedder, not the LLM — pass --embedder instead)")
		}
	}
	return nil
}

// filterJudgedQueries keeps only the queries that appear in qrels.
// Preserves the original order so smoke runs with --limit are
// deterministic across invocations (qrels iteration is map-ordered;
// queries is slice-ordered).
func filterJudgedQueries(queries []Query, qrels map[string]map[string]int) []Query {
	out := queries[:0]
	for _, q := range queries {
		if len(qrels[q.ID]) > 0 {
			out = append(out, q)
		}
	}
	return out
}

// helper used by runner.go to surface the package-level CellResult
// constants without leaking them into the public API.
func cellResultBase() *evalv2.CellResult {
	return &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		Harness:              evalv2.HarnessCortex,
		ContextStrategy:      evalv2.StrategyCortex,
		TaskSuccessCriterion: evalv2.CriterionTestsPassAll,
	}
}
