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

	"github.com/dereksantos/cortex/internal/harness"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
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

// NewActDispatcher returns a harness.ToolDispatcher that routes each
// tool call through the configured act.* registry, emits a synthetic
// trace entry, and appends the child's ID to spawnedChildren.
//
// Used by the executor-driven coding_turn handler (via the dispatcher
// installed on CortexHarness inside NewCodingTurnHandler), and by
// cortex code's --dag opt-in (which has no executor walking but
// wants the same per-tool trace shape with a synthetic parent ID).
//
// On registry miss (no act.<name> registered), emits a miss-trace
// entry and returns a structured error string the LLM can read.
// Practical impact is zero when RegisterActOps was called with all
// 5 known tools.
func NewActDispatcher(cfg CodingTurnConfig, parentNodeID string, spawnedChildren *[]string) harness.ToolDispatcher {
	return func(ctx context.Context, call llm.ToolCall) (string, error) {
		qname := "act." + normalizeToolName(call.Function.Name)
		spec, lookupErr := cfg.ActRegistry.Get(qname)
		if lookupErr != nil {
			// Miss: emit a trace entry recording the miss + return an
			// error message the loop forwards to the LLM.
			msg := fmt.Sprintf(`{"error":"no act op registered for %s; coding_turn cannot dispatch via DAG"}`, qname)
			emitTraceMiss(cfg, parentNodeID, spawnedChildren, qname, lookupErr.Error())
			return msg, nil
		}

		// Invoke the act handler. Force confirm=true — the user has
		// opted into destructive ops by running cortex code in the
		// first place; the axis-5 gate exists to catch programmatic
		// invocations that shouldn't happen, not user-driven coding.
		started := time.Now()
		childID := fmt.Sprintf("act-%d", atomic.AddInt64(&actChildIDCounter, 1))
		childCtx := context.WithValue(ctx, struct{ k string }{"dag.parent"}, parentNodeID)
		res, herr := spec.Handler(childCtx, map[string]any{
			"args":    call.Function.Arguments,
			"confirm": true,
		}, dag.DefaultTurnBudget())
		entry := dag.TraceEntry{
			NodeID:        childID,
			ParentID:      parentNodeID,
			QualifiedName: qname,
			OK:            herr == nil,
			CostConsumed:  res.CostConsumed,
			Out:           res.Out,
			WallStart:     started,
			WallEnd:       time.Now(),
		}
		if herr != nil {
			entry.ErrorCode = "handler_error"
			entry.ErrorMessage = herr.Error()
		}
		if cfg.TraceCB != nil {
			cfg.TraceCB(entry)
		}
		*spawnedChildren = append(*spawnedChildren, childID)

		out, _ := res.Out["output"].(string)
		if out == "" && herr != nil {
			out = fmt.Sprintf(`{"error":%q}`, herr.Error())
		}
		return out, nil
	}
}

// emitTraceMiss records a synthetic trace entry for a tool call that
// had no corresponding act op registered. Useful for the operator to
// see in dag_traces.jsonl: "the agent asked for tool X, no act op
// existed, here's the cost the lookup took."
func emitTraceMiss(cfg CodingTurnConfig, parentNodeID string, spawnedChildren *[]string, qname, errMsg string) {
	childID := fmt.Sprintf("act-%d", atomic.AddInt64(&actChildIDCounter, 1))
	entry := dag.TraceEntry{
		NodeID:        childID,
		ParentID:      parentNodeID,
		QualifiedName: qname,
		OK:            false,
		ErrorCode:     "unknown_node",
		ErrorMessage:  errMsg,
		WallStart:     time.Now(),
		WallEnd:       time.Now(),
	}
	if cfg.TraceCB != nil {
		cfg.TraceCB(entry)
	}
	*spawnedChildren = append(*spawnedChildren, childID)
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
