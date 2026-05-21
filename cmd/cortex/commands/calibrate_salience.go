// Package commands — `cortex calibrate salience` subcommand
// (Phase 3 Slice 5 per docs/salience-budgets.md).
//
// Reads .cortex/db/dag_traces.jsonl, isolates attend.compress rows,
// fits per-intent suggested caps from observed kept_tokens and
// fallback rate, and writes .cortex/calibration/salience.json. The
// REPL loads the snapshot at session start and overrides
// SalienceCapForClass with the calibrated global cap.
package commands

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// executeCalibrateSalience parses the salience-specific flag set
// and runs the calibration.
func executeCalibrateSalience(args []string) error {
	// Help arg is handled before flag parsing because flag.Parse
	// returns ErrHelp on --help with flag.ContinueOnError, which
	// would otherwise surface as a confusing "flag: help requested"
	// error to the user.
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printCalibrateSalienceHelp()
			return nil
		}
	}

	fs := flag.NewFlagSet("calibrate salience", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // suppress flag's own error spam; we print our own usage

	trace := fs.String("trace", "", "Source dag_traces.jsonl (default .cortex/db/dag_traces.jsonl)")
	out := fs.String("out", "", "Output path (default "+dag.DefaultSalienceCalibrationPath+")")
	window := fs.Int("window", 0, "Rolling-window row count (default 500)")
	headroom := fs.Float64("headroom", 0, "Multiplier on p90(kept_tokens) when sizing suggested cap (default 1.20)")
	minCap := fs.Int("min-cap", 0, "Floor under any suggested cap (default 60)")

	if err := fs.Parse(args); err != nil {
		printCalibrateSalienceHelp()
		return err
	}

	snap, err := dag.CalibrateSalience(dag.SalienceCalibrateOptions{
		TracePath:      *trace,
		SnapshotPath:   *out,
		WindowSize:     *window,
		HeadroomFactor: *headroom,
		MinCap:         *minCap,
	})
	if err != nil {
		return fmt.Errorf("calibrate salience: %w", err)
	}

	fmt.Printf("=== salience calibration ===\n")
	fmt.Printf("Source: %s  Window: %d  Samples: %d\n", snap.SourcePath, snap.WindowSize, snap.Samples)
	if snap.GlobalCap > 0 {
		fmt.Printf("Global cap (class-agnostic override): %d\n", snap.GlobalCap)
	} else {
		fmt.Printf("Global cap: (none — no compression rows yet; falling back to static SalienceCapForClass)\n")
	}
	fmt.Println()

	if len(snap.PerIntent) == 0 {
		fmt.Println("No per-intent samples — nothing to calibrate. Continue running the REPL to accumulate attend.compress rows.")
		return nil
	}

	// Stable order so the audit output is reproducible across runs.
	intents := make([]string, 0, len(snap.PerIntent))
	for k := range snap.PerIntent {
		intents = append(intents, k)
	}
	sort.Strings(intents)
	for _, intent := range intents {
		fit := snap.PerIntent[intent]
		fmt.Printf("  %-36s  cap=%4d  p50=%4d  p90=%4d  fallback=%5s  n=%d\n",
			truncStr(intent, 36),
			fit.SuggestedCap,
			fit.P50KeptTokens,
			fit.P90KeptTokens,
			strconv.FormatFloat(fit.FallbackRate, 'f', 2, 64),
			fit.Samples,
		)
	}
	return nil
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func printCalibrateSalienceHelp() {
	fmt.Println("Usage: cortex calibrate salience [flags]")
	fmt.Println()
	fmt.Println("Read .cortex/db/dag_traces.jsonl, isolate attend.compress rows, fit a")
	fmt.Println("per-intent suggested cap from observed kept_tokens + fallback rate, and")
	fmt.Println("write .cortex/calibration/salience.json. The REPL loads the snapshot at")
	fmt.Println("session start and overrides SalienceCapForClass with the calibrated cap.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --trace PATH      Source dag_traces.jsonl (default .cortex/db/dag_traces.jsonl)")
	fmt.Println("  --out PATH        Output snapshot (default " + dag.DefaultSalienceCalibrationPath + ")")
	fmt.Println("  --window N        Rolling-window row count (default 500)")
	fmt.Println("  --headroom F      Multiplier on p90(kept_tokens) (default 1.20)")
	fmt.Println("  --min-cap N       Floor under any suggested cap (default 60)")
}
