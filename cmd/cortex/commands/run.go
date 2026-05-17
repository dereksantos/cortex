// Package commands — cortex run --type=<dag-type> entry point.
//
// CLI surface for the DAG protocol per docs/dag-build-plan.md Stage
// 1 v0. Routes to the seed-and-grow executor with a per-type seed
// and initial budget. Telemetry from each node lands in
// .cortex/db/cell_results.jsonl via the Phase 1 unified sink.
//
// V0 scope:
// - --type=turn only (other types route to "not implemented" stubs)
// - 4 registered ops: sense.prompt, attend.reflex, decide.inject,
//   maintain.capture — all stub handlers that demonstrate the
//   executor walks, decays budget, and writes trace correctly
// - decide.coding_turn handler (wraps the LLM agent loop) lands in
//   Stage 3 of the build plan, not v0
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/dagtrace"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func init() {
	Register(&RunCommand{})
}

// RunCommand executes a DAG of a given type with a user-supplied
// prompt (or other trigger).
type RunCommand struct{}

func (c *RunCommand) Name() string        { return "run" }
func (c *RunCommand) Description() string { return "Run a DAG by type (turn|think|dream|capture|eval)" }

func (c *RunCommand) Execute(ctx *Context) error {
	dagType := ""
	prompt := ""
	outputFormat := "human"
	verbose := false

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch arg {
		case "--type":
			if i+1 < len(ctx.Args) {
				dagType = ctx.Args[i+1]
				i++
			}
		case "--prompt":
			if i+1 < len(ctx.Args) {
				prompt = ctx.Args[i+1]
				i++
			}
		case "-o", "--output":
			if i+1 < len(ctx.Args) {
				outputFormat = ctx.Args[i+1]
				i++
			}
		case "-v", "--verbose":
			verbose = true
		case "-h", "--help":
			printRunHelp()
			return nil
		default:
			if strings.HasPrefix(arg, "--type=") {
				dagType = strings.TrimPrefix(arg, "--type=")
			} else if strings.HasPrefix(arg, "--prompt=") {
				prompt = strings.TrimPrefix(arg, "--prompt=")
			}
		}
	}

	if dagType == "" {
		return fmt.Errorf("--type required (turn|think|dream|capture|eval)")
	}

	switch dagType {
	case "turn":
		return runTurnDAG(prompt, outputFormat, verbose)
	case "think", "dream", "capture", "eval":
		return fmt.Errorf("--type=%s not yet implemented (Stage 5 of dag-build-plan.md)", dagType)
	default:
		return fmt.Errorf("unknown --type=%s (known: turn, think, dream, capture, eval)", dagType)
	}
}

// runTurnDAG seeds a turn-type DAG with a minimal 4-op stub chain
// and runs it through the executor. V0 surface: demonstrates the
// executor walks end-to-end with telemetry; does NOT yet wire to
// real LLM-backed ops (that's Stage 2 of the build plan).
func runTurnDAG(prompt, outputFormat string, verbose bool) error {
	reg := buildV0Registry()
	turnID := fmt.Sprintf("turn-%d", time.Now().UnixNano())

	// Wire structured trace writes to .cortex/db/dag_traces.jsonl.
	// Tolerant: if the writer can't be constructed (e.g., no writable
	// cwd), continue without telemetry rather than abort.
	tw, twErr := dagtrace.NewWriter("")
	var traceCB dag.TraceCallback
	if twErr == nil {
		traceCB = tw.Callback(turnID)
	} else if verbose {
		fmt.Fprintf(os.Stderr, "[run] trace writer init failed: %v (continuing without dag_traces.jsonl)\n", twErr)
	}
	ex := dag.NewExecutor(reg, traceCB)

	seed := []dag.NodeSpec{
		{
			Function: dag.FuncSense,
			Op:       "prompt",
			ID:       "n1",
			Attrs:    map[string]any{"prompt": prompt},
		},
	}
	budget := dag.DefaultTurnBudget()
	trace, err := ex.Run(context.Background(), turnID, seed, budget)
	if err != nil {
		return fmt.Errorf("run turn DAG: %w", err)
	}

	if outputFormat == "json" {
		// Render trace as JSON envelope.
		out := map[string]any{
			"turn_id":          trace.TurnID,
			"initial_budget":   trace.InitialBudget,
			"final_budget":     trace.FinalBudget,
			"total_executed":   trace.TotalExecuted,
			"exhausted":        trace.Exhausted,
			"exhausted_axis":   trace.ExhaustedAxis,
			"spawn_refusals":   trace.SpawnRefusals,
			"trace_entry_ops":  qualifiedOps(trace.Entries),
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("=== turn DAG (%s) ===\n", turnID)
		fmt.Printf("Initial budget: %s\n", trace.InitialBudget)
		fmt.Printf("Executed %d nodes; exhausted=%v\n\n", trace.TotalExecuted, trace.Exhausted)
		for _, e := range trace.Entries {
			parent := e.ParentID
			if parent == "" {
				parent = "(seed)"
			}
			fmt.Printf("  %s [%s] parent=%s ok=%v cost={lat=%d tok=%d}\n",
				e.NodeID, e.QualifiedName, parent, e.OK,
				e.CostConsumed.LatencyMS, e.CostConsumed.Tokens)
			if verbose && e.ErrorMessage != "" {
				fmt.Printf("    err: %s\n", e.ErrorMessage)
			}
		}
		fmt.Printf("\nFinal budget: %s\n", trace.FinalBudget)
		if trace.Exhausted {
			fmt.Printf("Stopped on: %s exhaustion\n", trace.ExhaustedAxis)
		}
	}
	return nil
}

// buildV0Registry registers the 4 v0 ops with stub handlers. Each
// op consumes a small fixed cost and spawns the next op in the chain
// — enough to exercise the executor end-to-end. Real handlers land
// in Stage 2 (registry expansion to per-cognitive-mode ops) and
// Stage 3 (decide.coding_turn wraps the LLM loop).
func buildV0Registry() *dag.Registry {
	reg := dag.NewRegistry()

	stubHandler := func(cost dag.Cost, next []dag.NodeSpec) dag.Handler {
		return func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{
				Out:          map[string]any{"v0_stub": true},
				Spawn:        next,
				CostConsumed: cost,
			}, nil
		}
	}

	_ = reg.Register(dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "capture",
		Description: "stub: persist turn outcome to journal (real impl in Stage 3)",
		Cost:        dag.Cost{LatencyMS: 20, Tokens: 10},
		Handler:     stubHandler(dag.Cost{LatencyMS: 20, Tokens: 10}, nil),
	})
	_ = reg.Register(dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "inject",
		Description: "stub: format candidates as injected context (real impl in Stage 2)",
		Cost:        dag.Cost{LatencyMS: 30, Tokens: 20},
		Handler: stubHandler(dag.Cost{LatencyMS: 30, Tokens: 20}, []dag.NodeSpec{
			{Function: dag.FuncMaintain, Op: "capture", ID: "n4"},
		}),
	})
	_ = reg.Register(dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "reflex",
		Description: "stub: mechanical salience scoring (real impl in Stage 2)",
		Cost:        dag.Cost{LatencyMS: 40, Tokens: 5},
		Handler: stubHandler(dag.Cost{LatencyMS: 40, Tokens: 5}, []dag.NodeSpec{
			{Function: dag.FuncDecide, Op: "inject", ID: "n3"},
		}),
	})
	_ = reg.Register(dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "prompt",
		Description: "ingress: user prompt arrives",
		Cost:        dag.Cost{LatencyMS: 5, Tokens: 0},
		Handler: stubHandler(dag.Cost{LatencyMS: 5, Tokens: 0}, []dag.NodeSpec{
			{Function: dag.FuncAttend, Op: "reflex", ID: "n2"},
		}),
	})

	return reg
}

func qualifiedOps(entries []dag.TraceEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.QualifiedName
	}
	return out
}

func printRunHelp() {
	fmt.Fprintln(os.Stderr, `Usage: cortex run --type=<dag-type> [options]

Run a DAG of the given type through the seed-and-grow executor.

Options:
  --type TYPE         DAG type: turn | think | dream | capture | eval
                      (v0: only turn implemented)
  --prompt TEXT       User prompt (for --type=turn)
  -o, --output FMT    Output format: human | json (default: human)
  -v, --verbose       Verbose trace output
  -h, --help          Show this help

V0 scope (per docs/dag-build-plan.md Stage 1):
  - --type=turn only; other types route to "not implemented"
  - 4 stub ops (sense.prompt, attend.reflex, decide.inject, maintain.capture)
  - Demonstrates executor walks the seed, decays budget, emits trace
  - Real LLM handlers + decide.coding_turn integration land in Stage 2/3

See docs/dag-protocol.md for the protocol semantics.`)
}
