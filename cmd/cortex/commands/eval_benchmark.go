package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	// Blank imports register per-benchmark Benchmark constructors with
	// the registry via their init() functions. Add new benchmarks here
	// as they land.
	_ "github.com/dereksantos/cortex/internal/eval/benchmarks/swebench"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

// runBenchmark is the dispatch path for `cortex eval --benchmark <name>`.
// It looks up the benchmark in the registry, parses benchmark-shared
// flags from args, calls Load → Run per instance, and persists each
// CellResult through the standard Persister fan-out (journal → SQLite
// + JSONL).
//
// Benchmark-specific flags (--tasks, --length, --depth, --strategy,
// etc.) are parsed by the individual benchmark packages off of opts.Filter
// in a follow-up loop; this skeleton only wires --subset and --limit.
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

	env := benchmarks.Env{
		Persister: persister,
		Verbose:   verbose,
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
		if result.TaskSuccess {
			passed++
		}
	}

	fmt.Fprintf(os.Stdout, "[benchmark %s] %d/%d passed across %d instance(s)\n",
		name, passed, ran, len(instances))

	return firstErr
}

// parseBenchmarkArgs extracts shared and per-benchmark flags from a
// raw arg slice. --subset and --limit land on the LoadOpts struct
// directly; the rest land in opts.Filter as string→string so each
// benchmark can interpret them as it needs. Repeatable flags (--repo)
// are comma-joined.
//
// Unknown flags are tolerated and ignored so legacy callers that pass
// e.g. --provider don't break this layer.
func parseBenchmarkArgs(args []string) (benchmarks.LoadOpts, error) {
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	// repeatable: collect values and join with commas.
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
		case "--repo":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--repo requires a value")
			}
			prev := opts.Filter["repo"]
			if prev == "" {
				opts.Filter["repo"] = args[i+1]
			} else {
				opts.Filter["repo"] = prev + "," + args[i+1]
			}
			i++
		case "--strategy":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--strategy requires a value")
			}
			opts.Filter["strategy"] = args[i+1]
			i++
		case "--model":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--model requires a value")
			}
			opts.Filter["model"] = args[i+1]
			i++
		case "--docker-image-prefix":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--docker-image-prefix requires a value")
			}
			opts.Filter["docker-image-prefix"] = args[i+1]
			i++
		case "--git-cache-dir":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--git-cache-dir requires a value")
			}
			opts.Filter["git-cache-dir"] = args[i+1]
			i++
		}
	}
	return opts, nil
}
