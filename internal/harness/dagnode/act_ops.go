package dagnode

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// ActOpConfig wires a single tool handler + its axis contract +
// calibrated cost hint into a NodeSpec.
//
// Per docs/tool-surface.md the contract carries axis 2 (mutator) and
// axis 5 (requires-confirmation). Axes 1/3/4/6 (typed, transparent,
// budget-respecting, telemetered) are protocol-level guarantees the
// executor enforces; nothing to declare here.
type ActOpConfig struct {
	Handler  harness.ToolHandler
	Contract dag.AxisContract
	Cost     dag.Cost // calibrated p50 + headroom
}

// AdaptToolAsAct wraps a harness.ToolHandler as an act-typed DAG
// NodeSpec. The wrapper:
//
//   - Reads `args` (string) from the input map and forwards to the
//     underlying tool's Call(rawArgs).
//   - Enforces axis-5 — when Contract.RequiresConfirmation is true, the
//     input must also carry `confirm: true`, otherwise the call is
//     refused with a clear error before reaching the tool. This is the
//     destructive-op gate.
//   - Measures wall time per call and reports it as CostConsumed.
//   - Returns the tool's output string verbatim in Out["output"], plus
//     any tool error as the handler error (executor logs it as
//     handler_error, but the chain continues).
//
// V0 inline-dispatch path (no act ops registered) still works
// unchanged in internal/harness/loop.go — these adapters are
// additive, not a replacement for the existing dispatcher.
func AdaptToolAsAct(cfg ActOpConfig) dag.NodeSpec {
	t := cfg.Handler
	contract := cfg.Contract
	return dag.NodeSpec{
		Function:    dag.FuncAct,
		Op:          t.Name(),
		Description: t.Spec().Function.Description,
		Inputs: []dag.ParamSpec{
			{Name: "args", Type: "string", Required: true},
			{Name: "confirm", Type: "bool", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "output", Type: "string"},
			{Name: "tool_error", Type: "string"},
		},
		AxisContract: &contract,
		Cost:         cfg.Cost,
		Handler: func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
			started := time.Now()

			// Axis-5 gate: destructive ops require an explicit confirm.
			if contract.RequiresConfirmation {
				confirm, _ := in["confirm"].(bool)
				if !confirm {
					return dag.NodeResult{
						Out: map[string]any{
							"output":     "",
							"tool_error": fmt.Sprintf("act.%s: axis-5 violation — destructive op requires attrs.confirm=true", t.Name()),
						},
						CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
					}, fmt.Errorf("act.%s: destructive op requires confirm=true (axis-5)", t.Name())
				}
			}

			args, _ := in["args"].(string)
			if args == "" {
				args = "{}"
			}
			out, callErr := t.Call(ctx, args)
			latency := int(time.Since(started).Milliseconds())
			resOut := map[string]any{"output": out}
			if callErr != nil {
				resOut["tool_error"] = callErr.Error()
			}
			return dag.NodeResult{
				Out:          resOut,
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: 0},
			}, callErr
		},
	}
}

// RegisterActOps registers a batch of act ops on reg. Returns the
// number registered. Uses last-write-wins semantics — re-registering
// a tool with the same name replaces the prior spec (matches the
// rest of the registry).
func RegisterActOps(reg *dag.Registry, configs []ActOpConfig) (int, error) {
	for _, cfg := range configs {
		if err := reg.Register(AdaptToolAsAct(cfg)); err != nil {
			return 0, fmt.Errorf("register act.%s: %w", cfg.Handler.Name(), err)
		}
	}
	return len(configs), nil
}

// DefaultActOpContracts returns the canonical AxisContract for each
// of the 5 existing tools per docs/tool-surface.md:
//
//	read_file       — read (Mutator=false)
//	list_dir        — read (Mutator=false)
//	cortex_search   — read (Mutator=false)
//	write_file      — mutator (Mutator=true, no confirm — write is the
//	                  expected primary purpose, axis-5 would block
//	                  every write call)
//	run_shell       — mutator + requires confirm. The existing
//	                  sandbox already gates by allowlist
//	                  (resolveShellCommand), but axis-5 adds a
//	                  protocol-level "this is mutating, prove you
//	                  meant it" check the agent loop must opt in to.
//
// Returns a map keyed by tool name — callers (typically
// internal/harness or the cmd/cortex/commands wiring) pair these with
// their ToolHandler instances to build []ActOpConfig.
func DefaultActOpContracts() map[string]dag.AxisContract {
	return map[string]dag.AxisContract{
		"read_file":     {Mutator: false, RequiresConfirmation: false},
		"list_dir":      {Mutator: false, RequiresConfirmation: false},
		"cortex_search": {Mutator: false, RequiresConfirmation: false},
		"write_file":    {Mutator: true, RequiresConfirmation: false},
		"run_shell":     {Mutator: true, RequiresConfirmation: true},
	}
}

// DefaultActOpCosts returns cost hints for each of the 5 existing
// tools. Read ops are cheap (filesystem-bound, ~5-20ms); write_file
// is similar; run_shell varies wildly by command (gofmt ~50ms,
// go test minutes) so we hint conservatively at 30s and let the
// op self-report actual via CostConsumed.
//
// These are starter values; recalibrate against dag_traces.jsonl
// once the act-op path is exercised end-to-end.
func DefaultActOpCosts() map[string]dag.Cost {
	return map[string]dag.Cost{
		"read_file":     {LatencyMS: 50, Tokens: 0},
		"list_dir":      {LatencyMS: 50, Tokens: 0},
		"cortex_search": {LatencyMS: 100, Tokens: 0}, // includes embedder + storage scan
		"write_file":    {LatencyMS: 50, Tokens: 0},
		"run_shell":     {LatencyMS: 30000, Tokens: 0}, // generous; tools.go's run_shell has 30s timeout
	}
}
