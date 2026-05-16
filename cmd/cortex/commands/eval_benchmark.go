package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	// Side-effect imports register per-benchmark constructors via init().
	// Without these, benchmarks.Get(name) returns ErrUnknownBenchmark.
	_ "github.com/dereksantos/cortex/internal/eval/benchmarks/longmemeval"
	_ "github.com/dereksantos/cortex/internal/eval/benchmarks/niah"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/secret"
)

// runBenchmark is the dispatch path for `cortex eval --benchmark <name>`.
// It looks up the benchmark in the registry, parses benchmark-shared
// flags from args, calls Load → Run per instance, and persists each
// CellResult through the standard Persister fan-out (journal → SQLite
// + JSONL).
//
// Shared flags handled here: --subset, --limit, --strategy,
// --question-type, --model, --judge, --judge-model. Benchmark-specific
// flags (--length, --depth for NIAH) are parsed via each Benchmark's
// optional benchmarks.ArgsApplier implementation — no name-switch in
// the CLI.
func runBenchmark(name string, args []string, verbose bool) error {
	bench, err := benchmarks.Get(name)
	if err != nil {
		if errors.Is(err, benchmarks.ErrUnknownBenchmark) {
			return fmt.Errorf("unknown benchmark %q (registered: %v)", name, benchmarks.Registered())
		}
		return err
	}

	opts, err := parseBenchmarkArgs(args)
	if err != nil {
		return fmt.Errorf("parse benchmark args: %w", err)
	}

	// Benchmark-specific flag parsing layered on top of the shared
	// --subset / --limit handling. Each benchmark owns its flags by
	// implementing benchmarks.ArgsApplier (no name-switch in the
	// CLI). Unknown flags must be tolerated by Applier impls since
	// the same args slice carries the shared flags too.
	if applier, ok := bench.(benchmarks.ArgsApplier); ok {
		if err := applier.ApplyArgs(args, &opts); err != nil {
			return fmt.Errorf("parse %s flags: %w", name, err)
		}
	}

	ctx := context.Background()
	instances, err := bench.Load(ctx, opts)
	if err != nil {
		return fmt.Errorf("load %s: %w", name, err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[benchmark %s] loaded %d instance(s)\n", name, len(instances))
	}

	persister, err := evalv2.NewPersister()
	if err != nil {
		return fmt.Errorf("open persister: %w", err)
	}
	defer persister.Close()

	// Construct a judge provider once when --judge is set so all
	// instances reuse the same client (cheaper, simpler accounting).
	// Failure to build the judge is a soft error: log + skip judging.
	var judgeProvider llm.Provider
	if strings.EqualFold(opts.Filter["judge"], "true") {
		jm := opts.Filter["judge-model"]
		if jm == "" {
			jm = "anthropic/claude-haiku-4.5"
		}
		jp, jerr := newOpenRouterJudgeForBenchmark(jm)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "[benchmark %s] judge disabled: %v\n", name, jerr)
		} else {
			judgeProvider = jp
		}
	}

	env := benchmarks.Env{
		Persister:     persister,
		JudgeProvider: judgeProvider,
		Verbose:       verbose,
	}

	// Pre-call spend check: estimate the run's total cost against the
	// run/daily/lifetime ceilings. Hard-fail before any LLM calls so
	// the operator never accidentally drains the budget on a
	// no-filter full-split invocation.
	spend := evalv2.NewSpendTracker(persister, evalv2.CeilingsFromEnv())
	modelForEstimate := strings.TrimSpace(opts.Filter["model"])
	if modelForEstimate == "" {
		modelForEstimate = "anthropic/claude-haiku-4.5"
	}
	perCell := spend.EstimateCost(evalv2.ProviderOpenRouter, modelForEstimate)
	projected := perCell * float64(len(instances))
	if tripped, daily, lifetime, ckErr := spend.CheckBeforeCall(projected); ckErr != nil {
		return fmt.Errorf("spend check: %w", ckErr)
	} else if tripped != "" {
		return fmt.Errorf("benchmark %s projected cost $%.2f trips %s ceiling (per-cell est $%.4f × %d instances; daily=$%.2f lifetime=$%.2f) — set %s / %s / %s to raise, or use --limit N to bound",
			name, projected, tripped, perCell, len(instances), daily, lifetime,
			evalv2.EnvRunUSDCeiling, evalv2.EnvDailyUSDCeiling, evalv2.EnvLifetimeUSDCeiling)
	}

	var (
		ran, passed int
		firstErr    error
	)
	for _, inst := range instances {
		ran++
		workdir, err := os.MkdirTemp("", "cortex-bench-"+name+"-*")
		if err != nil {
			return fmt.Errorf("mkdir workdir: %w", err)
		}
		env.Workdir = workdir

		result, err := bench.Run(ctx, inst, env)
		if err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "[benchmark %s] %s: %v\n", name, inst.ID, err)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if result == nil {
			continue
		}
		if result.Benchmark == "" {
			result.Benchmark = name
		}
		if err := persister.PersistCell(ctx, result); err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "[benchmark %s] persist %s: %v\n", name, inst.ID, err)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Roll observed cost into the spend tracker so subsequent
		// estimates within this same process reflect reality.
		_ = spend.RecordCell(evalv2.ProviderOpenRouter, modelForEstimate, result.CostUSD)
		if result.TaskSuccess {
			passed++
		}
	}

	fmt.Fprintf(os.Stdout, "[benchmark %s] %d/%d passed across %d instance(s)\n",
		name, passed, ran, len(instances))

	return firstErr
}

// parseBenchmarkArgs extracts shared flags from a raw arg slice into
// LoadOpts. Unknown flags are tolerated so per-benchmark flag parsers
// in downstream loops can re-walk the same slice without colliding.
//
// Supported:
//
//	--subset NAME            → opts.Subset
//	--limit N                → opts.Limit
//	--strategy a,b           → opts.Filter["strategy"]
//	--question-type a,b      → opts.Filter["question-type"] (repeatable;
//	                           repeats are joined with commas)
//	--model SLUG             → opts.Filter["model"]
//	--judge                  → opts.Filter["judge"] = "true"
//	--judge-model SLUG       → opts.Filter["judge-model"] (implies judge)
func parseBenchmarkArgs(args []string) (benchmarks.LoadOpts, error) {
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--subset":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--subset requires a value")
			}
			opts.Subset = args[i+1]
			i++
		case "--limit":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--limit requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, fmt.Errorf("--limit %q: %w", args[i+1], err)
			}
			opts.Limit = n
			i++
		case "--strategy":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--strategy requires a value")
			}
			opts.Filter["strategy"] = args[i+1]
			i++
		case "--question-type":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--question-type requires a value")
			}
			if existing := opts.Filter["question-type"]; existing != "" {
				opts.Filter["question-type"] = existing + "," + args[i+1]
			} else {
				opts.Filter["question-type"] = args[i+1]
			}
			i++
		case "--model":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--model requires a value")
			}
			opts.Filter["model"] = args[i+1]
			i++
		case "--judge":
			opts.Filter["judge"] = "true"
		case "--judge-model":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--judge-model requires a value")
			}
			opts.Filter["judge-model"] = args[i+1]
			opts.Filter["judge"] = "true"
			i++
		}
	}
	return opts, nil
}

// newOpenRouterJudgeForBenchmark builds a judge provider rooted at
// OpenRouter (keychain key, model passed in). Returns an error if the
// key is missing — the caller treats that as "judge disabled" rather
// than failing the whole run.
func newOpenRouterJudgeForBenchmark(model string) (llm.Provider, error) {
	key, _, err := secret.MustOpenRouterKey()
	if err != nil {
		return nil, fmt.Errorf("openrouter key: %w", err)
	}
	cl := llm.NewOpenRouterClientWithKey(&config.Config{}, key)
	cl.SetModel(model)
	return cl, nil
}
