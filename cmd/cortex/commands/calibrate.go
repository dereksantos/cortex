// Package commands — cortex calibrate entry point.
//
// Reads the project's .cortex/db/dag_traces.jsonl rolling window,
// computes per-op p50 latency + tokens from successful rows, and
// persists the result to .cortex/db/op_cost_hints.json. The next
// `cortex run` (or REPL session) warms its registry from the
// snapshot, so pre-spawn budget checks reflect observed reality
// instead of authored guesses.
//
// Stage 4-C deliverable per docs/dag-build-plan.md.
package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
)

func init() {
	Register(&CalibrateCommand{})
}

// CalibrateCommand exposes cost-hint self-calibration as a CLI op.
type CalibrateCommand struct{}

func (c *CalibrateCommand) Name() string { return "calibrate" }
func (c *CalibrateCommand) Description() string {
	return "Recompute per-op p50 cost hints from .cortex/db/dag_traces.jsonl"
}

func (c *CalibrateCommand) Execute(ctx *Context) error {
	tracePath := ""
	snapshotPath := ""
	window := 0

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch {
		case arg == "--trace" && i+1 < len(ctx.Args):
			tracePath = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--trace="):
			tracePath = strings.TrimPrefix(arg, "--trace=")
		case arg == "--snapshot" && i+1 < len(ctx.Args):
			snapshotPath = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--snapshot="):
			snapshotPath = strings.TrimPrefix(arg, "--snapshot=")
		case arg == "--window" && i+1 < len(ctx.Args):
			n, err := strconv.Atoi(ctx.Args[i+1])
			if err != nil {
				return fmt.Errorf("--window: %w", err)
			}
			window = n
			i++
		case strings.HasPrefix(arg, "--window="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--window="))
			if err != nil {
				return fmt.Errorf("--window: %w", err)
			}
			window = n
		case arg == "-h", arg == "--help":
			printCalibrateHelp()
			return nil
		}
	}

	// Build a registry with the default Stage 2 op set so applyHints
	// has somewhere to land. The on-disk snapshot is the durable
	// artifact callers consume; the in-memory registry mutation here
	// is just used to validate the calibration would apply cleanly.
	reg := dag.NewRegistry()
	if _, err := ops.RegisterDefaults(reg, ops.DefaultsConfig{}); err != nil {
		return fmt.Errorf("register default ops: %w", err)
	}

	snap, err := dag.Calibrate(reg, dag.CalibrateOptions{
		TracePath:    tracePath,
		SnapshotPath: snapshotPath,
		WindowSize:   window,
	})
	if err != nil {
		return fmt.Errorf("calibrate: %w", err)
	}

	fmt.Printf("=== calibration snapshot ===\n")
	fmt.Printf("Source: %s  Window: %d  Hints: %d\n\n", snap.SourcePath, snap.WindowSize, len(snap.Hints))
	if len(snap.Hints) == 0 {
		fmt.Println("(no trace rows — nothing to calibrate)")
		return nil
	}
	for qname, h := range snap.Hints {
		fmt.Printf("  %-32s p50_latency=%6dms  p50_tokens=%5d  samples=%d\n",
			qname, h.LatencyMS, h.Tokens, h.Samples)
	}
	return nil
}

func printCalibrateHelp() {
	fmt.Println("Usage: cortex calibrate [--trace PATH] [--snapshot PATH] [--window N]")
	fmt.Println()
	fmt.Println("Recompute per-op p50 cost hints from a dag_traces.jsonl rolling window.")
	fmt.Println("Persists the result to .cortex/db/op_cost_hints.json (the next")
	fmt.Println("cortex run / REPL turn loads it at executor construction).")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --trace PATH      Source dag_traces.jsonl (default .cortex/db/dag_traces.jsonl)")
	fmt.Println("  --snapshot PATH   Output op_cost_hints.json (default .cortex/db/op_cost_hints.json)")
	fmt.Println("  --window N        Rolling-window row count (default 100)")
}
