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
	"strings"
	"sync/atomic"
	"time"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/llm"
)

// formatPriorOutputs renders this turn's prior NodeResult.Outs as a
// compact context block to prepend to a coding_turn's user prompt.
// This is what makes multi-node plans (read → read → synthesize)
// compose: the synthesis node sees what earlier act.* / coding_turn
// nodes produced.
//
// Empty turn state (single-node plan, first node, or no executor) →
// empty string; the caller leaves the prompt untouched.
//
// Format: one section per prior node with its qname + relevant Out
// fields. Heterogeneous Out shapes get a generic "fields" dump — the
// model is good at parsing JSON-shaped context.
func formatPriorOutputs(ctx context.Context) string {
	records := dag.AllPriorOutputs(ctx)
	if len(records) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("PRIOR STEPS IN THIS TURN (use these as ground truth; do not call tools to re-fetch what's already here):\n")
	for i, r := range records {
		fmt.Fprintf(&b, "\n[step %d: %s]\n", i+1, r.QualifiedName)
		// Heterogeneous Out — pull the common useful fields by name,
		// fall back to a small JSON dump of unknown keys.
		emitted := false
		if v, ok := r.Out["output"].(string); ok && v != "" {
			fmt.Fprintf(&b, "output:\n%s\n", v)
			emitted = true
		}
		if v, ok := r.Out["response"].(string); ok && v != "" {
			fmt.Fprintf(&b, "response:\n%s\n", v)
			emitted = true
		}
		if v, ok := r.Out["reasoning"].(string); ok && v != "" {
			fmt.Fprintf(&b, "reasoning: %s\n", v)
			emitted = true
		}
		if !emitted {
			// Last-resort dump — drop the bulky/uninteresting bits.
			for k, v := range r.Out {
				switch k {
				case "spawned_children", "stub", "fallback", "tokens_in", "tokens_out", "cost_usd", "turns", "files_changed":
					continue
				}
				fmt.Fprintf(&b, "%s: %v\n", k, v)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// localOllamaAPIURL is the chat-completions endpoint for a default
// Ollama install. Duplicated from cmd/cortex/commands/repl.go's
// `defaultOllamaAPIURL` to keep dagnode independent of the CLI layer.
const localOllamaAPIURL = "http://localhost:11434/v1/chat/completions"

// apiURLForModel returns the chat-completions endpoint appropriate
// for a given model id. Mirrors the same slash-vs-bare-name routing
// the REPL uses elsewhere: slash → OpenRouter (empty = client default),
// bare name → local Ollama. Used by per-node model override in
// decide.coding_turn so an LLM-emitted `attrs.model` lands at the
// right endpoint without the caller having to specify both.
func apiURLForModel(modelID string) string {
	if strings.Contains(modelID, "/") {
		return ""
	}
	return localOllamaAPIURL
}

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

	// HarnessFactory, when non-nil, returns a CortexHarness instance
	// the handler should drive instead of constructing a fresh one.
	// Used by the REPL chain unification (Stage 5/6) to pass through
	// its preconfigured harness — model, shared Cortex, system
	// prompt, API URL, notifier, budget, full-tools, dispatcher
	// already wired. When nil the handler calls
	// evalv2.NewCortexHarness(model) and applies no overrides
	// (preserves V0 behavior for cortex run --type=turn callers).
	HarnessFactory func() (*evalv2.CortexHarness, error)

	// ResultCallback, when non-nil, is invoked synchronously after
	// the underlying harness's RunSessionWithResult returns, with the
	// full HarnessResult + LoopResult + the post-run harness instance
	// (so callers can call LastLoopResult, etc.). Lets a chain caller
	// like the REPL recover the full result objects rather than
	// reconstructing them from coding_turn's Out map (which only
	// carries a subset).
	ResultCallback func(*evalv2.CortexHarness, evalv2.HarnessResult, harness.LoopResult, error)

	// ToolOutputSalienceCap is the per-tool-call output-token cap the
	// dispatcher applies to act.* outputs before they re-enter the
	// inner agent loop's transcript. Zero means "no cap" — the legacy
	// uncompressed path. When > 0, the dispatcher invokes
	// attend.compress (looked up in ActRegistry) on any tool output
	// whose approx-token count exceeds the cap, and emits a synthetic
	// compression trace row with parent_node_id = the act.* call.
	//
	// Set by the REPL (and other callers) to keep cumulative input
	// tokens flat across many tool-calling agent turns — see
	// docs/salience-budgets.md "Inner agent loop (per-turn)".
	ToolOutputSalienceCap int

	// AccumulatorProvider, when non-nil, makes the dispatcher fold
	// each act.* output (post-compression) through attend.accumulate
	// and deposit the resulting snapshot into the executor's turn
	// state. Subsequent decide.next / decide.coding_turn calls in
	// the same turn see the working memory via
	// dag.LatestAccumulatorSnapshot(ctx) — bounded-context wiring
	// without the LLM having to emit attend.accumulate explicitly.
	//
	// Nil → no accumulator wiring; pre-bounded-context behavior.
	AccumulatorProvider llm.Provider

	// AccumulatorMaxTokens is the per-snapshot budget the
	// accumulator runs against. 0 disables the dispatcher path even
	// when AccumulatorProvider is set (defense-in-depth: an
	// unconfigured budget would mean "unbounded snapshot" which
	// defeats the point).
	AccumulatorMaxTokens int

	// AccumulatorIntent threads into the accumulator's prompt so
	// the compressor knows what facts to keep. Typically the user's
	// classified intent for the current turn ("code" / "review" /
	// etc.) — same one passed via NewActDispatcher's intent arg, but
	// kept distinct so callers can override.
	AccumulatorIntent string
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
		var (
			h    *evalv2.CortexHarness
			herr error
		)
		if cfg.HarnessFactory != nil {
			h, herr = cfg.HarnessFactory()
		} else {
			h, herr = evalv2.NewCortexHarness(model)
		}
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

		// Per-node model override (Stage 7 dynamic-DAG slice). When
		// decide.next emits attrs.model="..." on a coding_turn spawn,
		// retarget the harness at that model + the corresponding
		// API endpoint BEFORE running the session. The harness
		// constructs its OpenRouterClient fresh per session reading
		// h.model + h.apiURL, so SetModel/SetAPIURL takes effect on
		// the next RunSessionWithResult call.
		//
		// The harness is short-lived (one chain execution); no need
		// to restore the original model after — the next turn builds
		// a fresh one via HarnessFactory.
		if model != "" && model != cfg.Model {
			h.SetModel(model)
			h.SetAPIURL(apiURLForModel(model))
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
			h.SetDispatcher(NewActDispatcher(cfg, parentNodeID, prompt, &spawnedChildren))
		}

		// Inner-loop accumulator wiring. When the dispatcher is folding
		// tool outputs through attend.accumulate (AccumulatorProvider +
		// AccumulatorMaxTokens both set), give the agent loop a hook
		// that reads the latest deposited snapshot via
		// dag.LatestAccumulatorSnapshot(ctx). The loop then rewrites
		// each turn's msgs as (system + user + snapshot + last K
		// pairs), bounding per-turn input by the snapshot size rather
		// than letting tool-output history grow linearly. The same
		// snapshots already power decide.next / synthesis coding_turn
		// composition; this wiring just lets the *current* coding_turn's
		// own inner loop drink from them too — the missing piece
		// between the accumulator eval and the live REPL.
		if cfg.AccumulatorProvider != nil && cfg.AccumulatorMaxTokens > 0 {
			h.SetAccumulatorSnapshot(func(c context.Context) string {
				return dag.LatestAccumulatorSnapshot(c)
			}, 1)
		}

		// Inject prior turn-state outputs as a context block prepended
		// to the user prompt. This is what makes the "decide.next emits
		// [list_dir, read_file, synthesize-coding_turn]" pattern
		// actually compose: the synthesis node sees what the prior act.*
		// calls produced, instead of running blind. Empty turn state
		// (single-node plan, or first node in a turn) → no injection;
		// behavior unchanged.
		if priorCtx := formatPriorOutputs(ctx); priorCtx != "" {
			prompt = priorCtx + "\n\n---\n\nYour task: " + prompt
		}

		hr, runErr := h.RunSessionWithResult(ctx, prompt, workdir)
		latency := int(time.Since(started).Milliseconds())
		lr := h.LastLoopResult()
		// loop.Run swallows provider errors into LoopResult.Err and
		// returns (res, nil). Without lifting that back into runErr the
		// DAG executor records this node as OK even when the agent loop
		// failed mid-turn — the trace then lies (`decide.coding_turn ·
		// ok`) while the REPL has just printed `⚠ provider error: ...`.
		// Surface the loop's stored error so the node is marked failed.
		if runErr == nil && lr.Err != nil {
			runErr = lr.Err
		}
		// Cap-hit guard: loop.Run can return (Reason=turn_limit |
		// budget | no_progress, Err=nil, Final="") — the agent was
		// still in tool-calling mode when the loop bailed at a cap.
		// Without this guard the node reports ok=true with response=""
		// and the REPL prints nothing, indistinguishable from a hang.
		// Lift the structured Reason into runErr so the trace is
		// honest and the REPL surfaces a visible failure.
		//
		// ReasonModelDone with empty Final is left alone: a model that
		// legitimately emits an empty assistant turn with no tool calls
		// is a different (UX) problem, not a loop failure.
		if runErr == nil && strings.TrimSpace(lr.Final) == "" {
			switch lr.Reason {
			case harness.ReasonTurnLimit, harness.ReasonBudget, harness.ReasonNoProgress:
				runErr = fmt.Errorf("agent loop hit %s with no final response (turns=%d, tokens=%d in / %d out)",
					lr.Reason, hr.AgentTurnsTotal, hr.TokensIn, hr.TokensOut)
			}
		}
		if cfg.ResultCallback != nil {
			cfg.ResultCallback(h, hr, lr, runErr)
		}
		if runErr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"error":            runErr.Error(),
					"spawned_children": spawnedChildren,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: hr.TokensIn + hr.TokensOut},
			}, fmt.Errorf("decide.coding_turn run: %w", runErr)
		}
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
func NewActDispatcher(cfg CodingTurnConfig, parentNodeID string, intent string, spawnedChildren *[]string) harness.ToolDispatcher {
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

		// Salience-budget enforcement on the way back to the agent
		// loop's transcript. When the caller set ToolOutputSalienceCap,
		// we compress oversized tool outputs in place and emit a
		// synthetic attend.compress trace row with parent = this act.*
		// call. The compressed `out` is what flows into the LLM's
		// next-turn prompt; the original lives in the journal.
		var compressTrace *dag.TraceEntry
		if dispErr == nil && cfg.ToolOutputSalienceCap > 0 {
			compressed, ct := compressToolOutput(ctx, cfg.ActRegistry, out, cfg.ToolOutputSalienceCap, intent, childID, qname)
			if ct != nil {
				out = compressed
				compressTrace = ct
			}
		}

		// Accumulator folding. When the caller wired
		// AccumulatorProvider + AccumulatorMaxTokens, run each
		// (possibly-compressed) tool output through attend.accumulate
		// and deposit the new snapshot into turn state so later nodes
		// (decide.next, the synthesis decide.coding_turn) see it via
		// dag.LatestAccumulatorSnapshot. The deposit ID is recorded on
		// this act.* call's trace so the lineage is recoverable.
		var accumulatorTrace *dag.TraceEntry
		var accumulatorDepositID string
		if dispErr == nil && cfg.AccumulatorProvider != nil && cfg.AccumulatorMaxTokens > 0 && out != "" {
			accumulatorTrace, accumulatorDepositID = foldIntoAccumulator(ctx, cfg, out, childID)
		}

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
		if cfg.ToolOutputSalienceCap > 0 {
			entry.Salience = &dag.SalienceContract{
				MaxOutputTokens: cfg.ToolOutputSalienceCap,
				Intent:          intent,
			}
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
		if compressTrace != nil {
			entry.SpawnedChildren = append(entry.SpawnedChildren, compressTrace.NodeID)
		}
		if accumulatorDepositID != "" {
			entry.SpawnedChildren = append(entry.SpawnedChildren, accumulatorDepositID)
		}

		if cfg.TraceCB != nil {
			cfg.TraceCB(entry)
			if compressTrace != nil {
				cfg.TraceCB(*compressTrace)
			}
			if accumulatorTrace != nil {
				cfg.TraceCB(*accumulatorTrace)
			}
		}
		*spawnedChildren = append(*spawnedChildren, childID)

		return out, dispErr
	}
}

// foldIntoAccumulator runs the dispatcher's accumulator hook: invokes
// attend.accumulate with (latest snapshot, new tool output), deposits
// the resulting snapshot into dag turn state, and returns a synthetic
// trace entry capturing the step (parent_node_id = the act.* call).
//
// Returns (nil, "") when the accumulator op isn't registered or the
// snapshot is empty — fail-safe: the dispatcher keeps working
// without the accumulator wiring even if the registry is incomplete.
func foldIntoAccumulator(ctx context.Context, cfg CodingTurnConfig, observation, parentNodeID string) (*dag.TraceEntry, string) {
	spec, err := cfg.ActRegistry.Get("attend.accumulate")
	if err != nil {
		return nil, ""
	}
	prev := dag.LatestAccumulatorSnapshot(ctx)
	started := time.Now()
	res, herr := spec.Handler(ctx, map[string]any{
		"prev_snapshot": prev,
		"observation":   observation,
		"max_tokens":    cfg.AccumulatorMaxTokens,
		"intent":        cfg.AccumulatorIntent,
	}, dag.DefaultTurnBudget())
	if herr != nil {
		return nil, ""
	}
	snap, _ := res.Out["snapshot"].(string)
	tok, _ := res.Out["snapshot_tokens"].(int)
	fallback, _ := res.Out["fallback"].(bool)
	if snap == "" {
		return nil, ""
	}
	depositID := dag.DepositAccumulatorSnapshot(ctx, snap, tok, fallback)
	entry := &dag.TraceEntry{
		NodeID:        depositID,
		ParentID:      parentNodeID,
		QualifiedName: "attend.accumulate",
		OK:            true,
		CostConsumed:  res.CostConsumed,
		WallStart:     started,
		WallEnd:       time.Now(),
		Out: map[string]any{
			"snapshot_tokens": tok,
			"fallback":        fallback,
		},
	}
	return entry, depositID
}

// compressToolOutput runs the dispatcher's salience hook against a
// single tool output. When the output's approx-token count exceeds
// maxTokens, it either:
//
//   - Chunks deterministically (qname in {act.read_file, act.run_shell}):
//     splits by line boundary, joins with "[chunk i/N, lines a-b]"
//     headers, emits an attend.chunk trace entry. No LLM call. The
//     calling model sees raw bytes with location headers.
//
//   - Compresses via LLM (other qnames): invokes attend.compress through
//     reg with the intent string for salience extraction. Emits an
//     attend.compress trace entry.
//
// Returns (raw, nil) when no oversize handling was needed.
func compressToolOutput(ctx context.Context, reg *dag.Registry, raw string, maxTokens int, intent, parentNodeID, qname string) (string, *dag.TraceEntry) {
	if raw == "" || maxTokens <= 0 {
		return raw, nil
	}
	if approxTokens(raw) <= maxTokens {
		return raw, nil
	}
	// Deterministic chunking path — no LLM. Used for read_file and
	// run_shell where the calling model can act on raw bytes directly
	// and an LLM-summarized version would lose information.
	if qname == "act.read_file" || qname == "act.run_shell" {
		started := time.Now()
		joined, totalChunks, emitted := dag.ChunkOversize(raw, maxTokens)
		ended := time.Now()
		childID := fmt.Sprintf("chunk-%d", atomic.AddInt64(&actChildIDCounter, 1))
		entry := &dag.TraceEntry{
			NodeID:        childID,
			ParentID:      parentNodeID,
			QualifiedName: "attend.chunk",
			WallStart:     started,
			WallEnd:       ended,
			OK:            true,
			CostConsumed:  dag.Cost{LatencyMS: int(ended.Sub(started).Milliseconds())},
			Out: map[string]any{
				"chunks":     totalChunks,
				"emitted":    emitted,
				"max_tokens": maxTokens,
				"intent":     intent,
			},
			Salience: &dag.SalienceContract{
				MaxOutputTokens: maxTokens,
				Intent:          intent,
				ChunkOnOversize: true,
			},
		}
		return joined, entry
	}
	spec, err := reg.Get("attend.compress")
	if err != nil {
		// Compressor not registered in the actReg — caller forgot to
		// register it. Fall through; pre-salience-budgets behavior
		// preserved. Telemetry will catch this when downstream context
		// exhausts.
		return raw, nil
	}
	started := time.Now()
	compRes, compErr := spec.Handler(ctx, map[string]any{
		"raw":        raw,
		"max_tokens": maxTokens,
		"intent":     intent,
	}, dag.Budget{LatencyMS: 60000, Tokens: 4000, Depth: 1})
	ended := time.Now()

	childID := fmt.Sprintf("compress-%d", atomic.AddInt64(&actChildIDCounter, 1))
	entry := &dag.TraceEntry{
		NodeID:        childID,
		ParentID:      parentNodeID,
		QualifiedName: "attend.compress",
		WallStart:     started,
		WallEnd:       ended,
		CostConsumed:  compRes.CostConsumed,
		Out:           compRes.Out,
		Salience: &dag.SalienceContract{
			MaxOutputTokens: maxTokens,
			Intent:          intent,
		},
	}
	if compErr != nil {
		entry.OK = false
		entry.ErrorCode = "handler_error"
		entry.ErrorMessage = compErr.Error()
		return raw, entry
	}
	compressed, ok := compRes.Out["compressed"].(string)
	if !ok {
		entry.OK = false
		entry.ErrorCode = "handler_error"
		entry.ErrorMessage = "attend.compress returned no 'compressed' string"
		return raw, entry
	}
	entry.OK = true
	return compressed, entry
}

// approxTokens — local 4-char-per-token heuristic. Keeps the dagnode
// package free of an ops dependency; both packages agree on the same
// rule of thumb until Phase 2's real compressor wires a tokenizer.
func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n == 0 {
		return 1
	}
	return n
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
// DefaultActOpCosts, plus the attend.compress op the dispatcher uses
// to enforce per-tool-call salience budgets (docs/salience-budgets.md).
//
// The compress op is registered without a Provider — it falls back to
// the deterministic truncate-stub. Callers that want the LLM-driven
// Reflect-style compressor should use
// RegisterDefaultActOpMetadataWithCompressor instead.
//
// Returns the count registered.
func RegisterDefaultActOpMetadata(reg *dag.Registry) (int, error) {
	return RegisterDefaultActOpMetadataWithCompressor(reg, nil)
}

// RegisterDefaultActOpMetadataWithCompressor is the
// RegisterDefaultActOpMetadata variant that wires a Provider into the
// attend.compress op, so per-tool-call compression goes through a
// real small-model salience pass rather than the truncate-stub
// fallback.
//
// The provider is shared with the dispatcher's read of attend.compress
// from this registry — every act.* call that exceeds its salience
// budget triggers a Provider.GenerateWithStats call under the
// attend_compress.tmpl prompt. See docs/salience-budgets.md.
func RegisterDefaultActOpMetadataWithCompressor(reg *dag.Registry, provider llm.Provider) (int, error) {
	contracts := DefaultActOpContracts()
	costs := DefaultActOpCosts()
	names := []string{"read_file", "list_dir", "write_file", "run_shell", "cortex_search"}
	for _, n := range names {
		if err := RegisterActOpMetadata(reg, n, contracts[n], costs[n]); err != nil {
			return 0, fmt.Errorf("register act.%s: %w", n, err)
		}
	}
	if err := reg.Register(ops.CompressSpec(ops.CompressConfig{Provider: provider})); err != nil {
		return 0, fmt.Errorf("register attend.compress: %w", err)
	}
	// attend.accumulate + attend.compact ride the same provider —
	// they're the working-memory engine the dispatcher folds into
	// when CodingTurnConfig.AccumulatorProvider is set. Registering
	// them here means callers don't have to know about the
	// bounded-context wiring to benefit from it; setting a single
	// AccumulatorProvider on CodingTurnConfig flips the path on.
	if err := reg.Register(ops.AccumulateSpec(ops.AccumulateConfig{Provider: provider})); err != nil {
		return 0, fmt.Errorf("register attend.accumulate: %w", err)
	}
	if err := reg.Register(ops.CompactSpec(ops.CompactConfig{Provider: provider})); err != nil {
		return 0, fmt.Errorf("register attend.compact: %w", err)
	}
	return len(names) + 3, nil
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
