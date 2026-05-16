// Package benchmarks defines the substrate for dataset-driven evals
// (LongMemEval, MTEB, SWE-bench, NIAH/RULER) that complement Cortex's
// hand-authored YAML scenarios.
//
// A Benchmark loads a list of Instances from a dataset (cached locally
// under ~/.cortex/benchmarks/<name>/) and runs each one against Cortex,
// producing a CellResult that lands in the same SQLite + JSONL + journal
// fan-out as scenario-driven evals. See docs/benchmarks/overview.md.
//
// Per-benchmark packages (longmemeval, mteb, swebench, niah) register
// themselves via init() against the registry in this package.
package benchmarks

import (
	"context"

	"github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Benchmark is the contract every dataset-driven eval implements.
//
// Lifecycle per `cortex eval --benchmark <name>` invocation:
//  1. CLI handler looks up the benchmark by Name() via Get().
//  2. CLI handler calls Load() with the parsed LoadOpts.
//  3. For each returned Instance, CLI handler calls Run(env), then
//     env.Persister.PersistCell(result).
//
// The benchmark itself does NOT call PersistCell; keeping the call out
// of Run lets each Benchmark be unit-tested without a real Persister.
type Benchmark interface {
	// Name returns the registry key (e.g. "longmemeval", "mteb",
	// "swebench", "niah").
	Name() string

	// Load returns the instances selected by opts. Implementations should
	// honor Subset and Limit; the meaning of Filter is benchmark-specific
	// (e.g. LongMemEval uses it for --question-type). Implementations may
	// return an error if Subset is unsupported (e.g. "Phase B" splits not
	// yet wired).
	Load(ctx context.Context, opts LoadOpts) ([]Instance, error)

	// Run executes one instance and returns a fully-validated CellResult.
	// Implementations must set Benchmark on the returned result so
	// downstream rollups can group by benchmark family without parsing
	// ScenarioID prefixes.
	Run(ctx context.Context, inst Instance, env Env) (*eval.CellResult, error)
}

// Instance is a single benchmark item — one LongMemEval question, one
// MTEB task, one SWE-bench issue, one NIAH (length, depth) combination.
// ID is the human-readable identifier (e.g. "qa_017", "NFCorpus",
// "django__django-12345"); Payload carries benchmark-specific data the
// runner needs at Run time.
type Instance struct {
	ID      string
	Payload any
}

// LoadOpts narrows which instances Load returns. All fields are optional;
// a zero-value LoadOpts loads everything.
//
// Subset selects a dataset variant (LongMemEval: "oracle"|"s"|"m";
// MTEB: a task name; SWE-bench: "verified"|"lite"|"full"). Limit caps
// the number of returned instances. Filter is benchmark-specific.
type LoadOpts struct {
	Subset string
	Limit  int
	Filter map[string]string
}

// Env is the runtime context Run receives. Provider is the LLM used by
// the system under test; JudgeProvider is optional and used when the
// benchmark scores via LLM-as-judge. Persister is supplied for
// benchmarks that need cost accounting probes; the CLI handler owns the
// actual PersistCell call. Workdir is a per-instance scratch directory
// owned by the handler.
type Env struct {
	Provider      llm.Provider
	JudgeProvider llm.Provider
	Persister     *eval.Persister
	Workdir       string
	Verbose       bool
}

// ArgsApplier is an optional interface a Benchmark can implement to
// parse its own CLI flags off the raw arg slice and stash the
// results in LoadOpts.Filter. The dispatcher (cmd/cortex/commands)
// calls ApplyArgs after parseBenchmarkArgs and before Load, so
// benchmark-specific flags do not need a switch on benchmark name
// in the CLI layer.
//
// Conventions for implementers:
//   - Tolerate unknown flags silently — the dispatcher's shared flags
//     (--subset, --limit, --benchmark) will appear in args too.
//   - Reject flags that are meaningless for this benchmark (e.g. NIAH
//     rejects --model) with a clean error.
//   - Encode repeated values as comma-separated strings in
//     opts.Filter (Filter is map[string]string, not []string).
type ArgsApplier interface {
	ApplyArgs(args []string, opts *LoadOpts) error
}
