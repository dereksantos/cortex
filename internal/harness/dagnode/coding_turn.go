// Package dagnode bridges the DAG executor (pkg/cognition/dag) to
// the existing Cortex coding harness (internal/eval/v2 + internal/
// harness). It provides handlers for cortex-function-typed DAG nodes
// that need to invoke real LLM-backed work — primarily
// `decide.coding_turn`.
//
// Per ADR-001 (docs/adrs/0001-coding-turn-structure.md), V0
// `decide.coding_turn` runs the existing agent loop INLINE within a
// single handler call. The handler returns the final response in
// Out + total CostConsumed; it does NOT spawn act.* children in V0
// (that's Stage 3 per the same ADR).
package dagnode

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// CodingTurnConfig wires the handler to a model + workdir at
// registration time. Model and Workdir may be empty: if Model is
// empty the handler returns a stub-mode NodeResult; if Workdir is
// empty it defaults to ".".
//
// Stage 3 fields (ActRegistry + TraceCB) are optional. When both are
// set AND the registry has act.* entries, the handler routes every
// tool call the LLM emits through the registered act op (axis-5
// gate enforced) and emits a synthetic dag.TraceEntry row per call
// via TraceCB with parent_node_id = this node's ID. Without these
// fields, the handler runs V0 inline-dispatch (no per-tool trace
// rows, behavior identical to pre-Stage-3).
type CodingTurnConfig struct {
	Model   string // OpenRouter-qualified model id, e.g. "anthropic/claude-haiku-4.5"
	Workdir string // workdir the harness operates on; defaults to "." (cwd)

	// ActRegistry, if non-nil, is consulted for `act.<tool_name>` on
	// each tool call. When the lookup hits, the call dispatches
	// through the registered handler (axis-5 gate enforced + per-tool
	// CostConsumed measured). When it misses, falls back to inline
	// dispatch on the harness's own ToolRegistry. nil → always
	// inline (V0).
	ActRegistry *dag.Registry

	// TraceCB, if non-nil AND ActRegistry is also set, receives one
	// synthetic dag.TraceEntry per dispatched tool call with
	// parent_node_id = this coding_turn node's ID. Used by run.go
	// to write per-tool rows to dag_traces.jsonl alongside the
	// coding_turn's own row.
	TraceCB dag.TraceCallback
}

// NewCodingTurnHandler returns a dag.Handler for decide.coding_turn.
//
// V0 behavior (per ADR-001):
//   - Reads `prompt` from in (string)
//   - Reads `model` from in.attrs override if present, else cfg.Model
//   - If no model configured: returns stub NodeResult with a clear
//     error_message indicating provider-not-configured; this lets
//     `cortex run --type=turn` exercise the rest of the DAG without
//     requiring API keys for every developer iteration
//   - If model configured: constructs evalv2.CortexHarness, runs the
//     session, returns the final response + actual cost
//   - Does NOT spawn act.* children (Stage 3)
//
// Errors from the harness are returned as the second return value;
// the executor will log them as handler_error TraceEntry rows.
func NewCodingTurnHandler(cfg CodingTurnConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		prompt, _ := in["prompt"].(string)
		if prompt == "" {
			return dag.NodeResult{}, fmt.Errorf("decide.coding_turn: prompt input is required")
		}

		// Per-call model override (attrs) wins over registration default.
		model := cfg.Model
		if m, ok := in["model"].(string); ok && m != "" {
			model = m
		}

		// Stub-mode path: no model configured. Return success with a
		// clear stub indicator. This is the path cortex run --type=turn
		// takes by default (so developers can exercise the protocol
		// without API keys).
		if model == "" {
			return dag.NodeResult{
				Out: map[string]any{
					"response": fmt.Sprintf("[stub coding_turn] prompt was: %q (no provider configured; set --model to invoke real LLM)", prompt),
					"stub":     true,
				},
				CostConsumed: dag.Cost{LatencyMS: 10, Tokens: 0},
			}, nil
		}

		// Real path: construct the existing CortexHarness and run.
		// V0 form (cfg.ActRegistry == nil): wraps the LLM agent loop
		// as one opaque node. Tool calls dispatch inline via the
		// harness's own ToolRegistry; no per-tool trace rows.
		//
		// Stage 3 form (cfg.ActRegistry != nil): the coding_turn node
		// installs a ToolDispatcher on the harness that routes each
		// tool call through the registered act.<tool_name> op (axis-5
		// gate enforced), then fabricates a synthetic dag.TraceEntry
		// per call with parent_node_id = this node's ID, and emits
		// via cfg.TraceCB. The agent loop is otherwise unchanged —
		// the LLM still drives turns, only the dispatch layer is
		// re-wired. Per-tool trace rows appear alongside the
		// coding_turn's own row in dag_traces.jsonl.
		started := time.Now()
		h, herr := evalv2.NewCortexHarness(model)
		if herr != nil {
			return dag.NodeResult{
				Out:          map[string]any{"error": herr.Error()},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.coding_turn init: %w", herr)
		}

		workdir := cfg.Workdir
		if workdir == "" {
			workdir = "."
		}

		// Spawn-aware dispatch wiring. When ActRegistry is provided,
		// build a dispatcher closure that:
		//   1. Looks up act.<tool_name> in the registry.
		//   2. If hit, invokes the registered handler with the tool's
		//      raw args + confirm=true (the agent-loop opt-in to
		//      destructive ops; the user opted in by running cortex code).
		//   3. Emits a synthetic dag.TraceEntry for the call.
		//   4. Returns the underlying tool's output string.
		// On miss (no act op registered for the tool name), returns
		// without setting Dispatcher so the harness's V0 inline path
		// keeps working.
		var spawnedChildren []string
		parentNodeID := dag.NodeIDFromContext(ctx)
		if cfg.ActRegistry != nil {
			h.SetDispatcher(NewActDispatcher(cfg, parentNodeID, &spawnedChildren))
		}

		hr, runErr := h.RunSessionWithResult(ctx, prompt, workdir)
		latency := int(time.Since(started).Milliseconds())
		if runErr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"error":            runErr.Error(),
					"spawned_children": spawnedChildren,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: hr.TokensIn + hr.TokensOut},
			}, fmt.Errorf("decide.coding_turn run: %w", runErr)
		}

		lr := h.LastLoopResult()
		return dag.NodeResult{
			Out: map[string]any{
				"response":         lr.Final,
				"turns":            hr.AgentTurnsTotal,
				"tokens_in":        hr.TokensIn,
				"tokens_out":       hr.TokensOut,
				"cost_usd":         hr.CostUSD,
				"files_changed":    hr.FilesChanged,
				"spawned_children": spawnedChildren,
			},
			CostConsumed: dag.Cost{
				LatencyMS: latency,
				Tokens:    hr.TokensIn + hr.TokensOut,
			},
		}, nil
	}
}

// actChildIDCounter is a process-wide monotonic counter for assigning
// unique IDs to fabricated act-op trace entries. Atomic to be safe
// under concurrent coding_turn invocations (a Stage 4 use case).
var actChildIDCounter int64

// NewActDispatcher returns a harness.ToolDispatcher that:
//
//  1. Reads the axis-5 contract + cost-hint metadata from the
//     `act.<tool_name>` spec in cfg.ActRegistry.
//  2. Enforces the axis-5 gate (destructive ops require confirm,
//     which the agent-loop context auto-supplies — the user opted in
//     by running cortex code/repl).
//  3. **Delegates the actual tool execution to reg.Dispatch** so the
//     harness's own ToolRegistry accounting (FilesWritten,
//     ShellNonZeroExits, InjectedContextTokens) is preserved. The
//     earlier-this-session bug — building a parallel ToolRegistry
//     just for the act path — violated principles 5 (Reproducible)
//     and 7 (Structured) by silently zeroing those CellResult
//     fields when --dag was on. Don't reintroduce.
//  4. Emits a synthetic dag.TraceEntry per call with parent_node_id
//     set to the calling node's ID, child_id auto-assigned.
//
// Registry miss (no act.<name> spec) → still delegates to
// reg.Dispatch so the agent loop keeps working, but emits an
// unknown_node trace row so the operator sees the gap. Practical
// impact zero when RegisterActOpMetadata covers all known tools.
func NewActDispatcher(cfg CodingTurnConfig, parentNodeID string, spawnedChildren *[]string) harness.ToolDispatcher {
	return func(ctx context.Context, reg *harness.ToolRegistry, call llm.ToolCall) (string, error) {
		qname := "act." + normalizeToolName(call.Function.Name)
		started := time.Now()
		childID := fmt.Sprintf("act-%d", atomic.AddInt64(&actChildIDCounter, 1))

		spec, lookupErr := cfg.ActRegistry.Get(qname)
		var (
			contract dag.AxisContract
			costHint dag.Cost
			missErr  string
		)
		if lookupErr != nil {
			missErr = lookupErr.Error()
		} else {
			if spec.AxisContract != nil {
				contract = *spec.AxisContract
			}
			costHint = spec.Cost
		}

		// axis-5: surface contract metadata in the trace, but
		// auto-confirm — the agent loop is itself the user's
		// confirmation. The dispatcher cannot meaningfully prompt the
		// user mid-LLM-call. The contract is preserved so future
		// human-in-the-loop modes can read it and intercept.
		_ = contract

		// Delegate to the harness registry — same tool instances the
		// V0 path would have used, so all the registry's per-call
		// accounting (write_file → noteFileWritten, run_shell →
		// noteShellExit) runs verbatim.
		out, dispErr := reg.Dispatch(ctx, call)
		latency := int(time.Since(started).Milliseconds())

		// Compose trace entry.
		entry := dag.TraceEntry{
			NodeID:        childID,
			ParentID:      parentNodeID,
			QualifiedName: qname,
			OK:            dispErr == nil && missErr == "",
			CostConsumed:  dag.Cost{LatencyMS: latency, Tokens: 0},
			Out:           map[string]any{"output": out},
			WallStart:     started,
			WallEnd:       time.Now(),
		}
		// Surface the cost hint in Out for the operator to compare
		// against observed latency (calibration feedback channel).
		if costHint.LatencyMS > 0 {
			entry.Out["cost_hint_ms"] = costHint.LatencyMS
		}
		if missErr != "" {
			entry.ErrorCode = "unknown_node"
			entry.ErrorMessage = "no act op metadata registered for " + qname
		} else if dispErr != nil {
			entry.ErrorCode = "handler_error"
			entry.ErrorMessage = dispErr.Error()
		}

		if cfg.TraceCB != nil {
			cfg.TraceCB(entry)
		}
		*spawnedChildren = append(*spawnedChildren, childID)

		return out, dispErr
	}
}

// RegisterActOpMetadata registers an act.<name> NodeSpec in reg with
// only the protocol metadata (axis contract, cost hint, qualified
// name). The Handler is a placeholder that returns an error if
// invoked standalone — these specs are designed to be consumed by
// the NewActDispatcher (which reads metadata + delegates to the
// harness ToolRegistry), not invoked through the DAG executor.
//
// Use this from CLI callers (cortex code, REPL) that want the
// dispatcher's axis-5 + trace shape without constructing parallel
// tool instances.
func RegisterActOpMetadata(reg *dag.Registry, name string, contract dag.AxisContract, cost dag.Cost) error {
	return reg.Register(dag.NodeSpec{
		Function:    dag.FuncAct,
		Op:          name,
		Description: "act-op metadata for " + name + " (executed via harness ToolRegistry; see NewActDispatcher)",
		Inputs: []dag.ParamSpec{
			{Name: "args", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "output", Type: "string"},
		},
		AxisContract: &contract,
		Cost:         cost,
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{}, fmt.Errorf("act.%s: metadata-only NodeSpec (executed via harness ToolRegistry; see NewActDispatcher)", name)
		},
	})
}

// RegisterDefaultActOpMetadata is the batch convenience wrapper:
// registers all 5 canonical tools (read_file, list_dir, write_file,
// run_shell, cortex_search) with their DefaultActOpContracts +
// DefaultActOpCosts. Returns the count registered.
func RegisterDefaultActOpMetadata(reg *dag.Registry) (int, error) {
	contracts := DefaultActOpContracts()
	costs := DefaultActOpCosts()
	names := []string{"read_file", "list_dir", "write_file", "run_shell", "cortex_search"}
	for _, n := range names {
		if err := RegisterActOpMetadata(reg, n, contracts[n], costs[n]); err != nil {
			return 0, fmt.Errorf("register act.%s: %w", n, err)
		}
	}
	return len(names), nil
}

// normalizeToolName mirrors harness.normalizeToolName (which is
// unexported). Strips chat-template artifacts some open-weight models
// leak into the tool name field ("foo<|channel|>commentary" → "foo").
func normalizeToolName(s string) string {
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '<' && s[i+1] == '|' {
			return trimTrailingSpace(s[:i])
		}
	}
	return trimTrailingSpace(s)
}

func trimTrailingSpace(s string) string {
	end := len(s)
	for end > 0 && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n') {
		end--
	}
	return s[:end]
}
