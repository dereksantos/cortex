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
	// Without these, benchmarks.Get("niah") returns ErrUnknownBenchmark.
	_ "github.com/dereksantos/cortex/internal/eval/benchmarks/niah"
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

	// Benchmark-specific flag parsing layered on top of the shared
	// --subset / --limit handling. Each benchmark owns its own flags
	// (--length / --depth for niah, --tasks for mteb, ...) and decodes
	// them into opts.Filter; benchmark.Load reads from there.
	switch name {
	case "niah":
		if err := applyNIAHFlags(args, &opts); err != nil {
			return fmt.Errorf("parse niah flags: %w", err)
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

// applyNIAHFlags walks the raw arg slice a second time, extracting
// niah-specific flags into opts.Filter. Repeated flags accumulate
// (--length 8k --length 16k → "8k,16k"); singletons overwrite.
// Refuses --model in this layer — NIAH measures the retrieval substrate,
// not the LLM, so a --model flag is meaningless and almost always an
// operator error worth surfacing loudly.
func applyNIAHFlags(args []string, opts *benchmarks.LoadOpts) error {
	if opts.Filter == nil {
		opts.Filter = map[string]string{}
	}
	var lengths, depths []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--length":
			if i+1 >= len(args) {
				return fmt.Errorf("--length requires a value")
			}
			lengths = append(lengths, args[i+1])
			i++
		case "--depth":
			if i+1 >= len(args) {
				return fmt.Errorf("--depth requires a value")
			}
			depths = append(depths, args[i+1])
			i++
		case "--needle":
			if i+1 >= len(args) {
				return fmt.Errorf("--needle requires a value")
			}
			opts.Filter["needle"] = args[i+1]
			i++
		case "--seed":
			if i+1 >= len(args) {
				return fmt.Errorf("--seed requires a value")
			}
			opts.Filter["seed"] = args[i+1]
			i++
		case "-m", "--model":
			return fmt.Errorf("--model is not valid with --benchmark niah (NIAH measures retrieval, not LLMs)")
		}
	}
	if len(lengths) > 0 {
		// Honor comma-separated values within a single flag too, so
		// `--length 8k,16k` and `--length 8k --length 16k` both work.
		opts.Filter["lengths"] = joinExpandingCSV(lengths)
	}
	if len(depths) > 0 {
		opts.Filter["depths"] = joinExpandingCSV(depths)
	}
	return nil
}

// joinExpandingCSV joins values with commas, flattening any
// already-comma-separated values inside. Empty/whitespace fragments
// are dropped so "8k, ,16k" round-trips to "8k,16k".
func joinExpandingCSV(vals []string) string {
	var out []string
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return strings.Join(out, ",")
}

// parseBenchmarkArgs extracts the shared --subset and --limit flags
// from a raw arg slice. Unknown flags are tolerated and ignored so
// per-benchmark flag parsers in downstream loops can re-walk the same
// slice without colliding with this layer.
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
		}
	}
	return opts, nil
}
