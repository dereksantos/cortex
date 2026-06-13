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
	"os"
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

	// ToolOutputEmittedTokens caps the TOTAL tokens emitted across all
	// deterministic chunks produced from a single act.read_file /
	// act.run_shell output (i.e. the chunker's truncation budget,
	// distinct from ToolOutputSalienceCap which is the per-chunk
	// size). Zero falls back to the legacy MaxEmittedChunks=8 chunk-
	// count cap.
	//
	// Set by NewCodingTurnHandler from budget.EmittedTokensCap() at
	// per-invocation time so the value reflects the classified session
	// intent (code → 4K, review/recall/meta → 16K) and the active
	// model's context window. See docs/handoff-2026-05-25.md.
	ToolOutputEmittedTokens int

	// SynthDefaultModel, when non-nil, supplies a deterministic
	// fallback model for synthesize=true coding_turn invocations
	// where the spawn didn't carry an explicit attrs.model override.
	// Returns the model id to retarget the harness to (e.g.
	// "chatterbox/reasoner"), or "" to fall through to the harness's
	// configured model.
	//
	// Used by the REPL to land review/recall-class synth turns on a
	// reasoning specialist when the LLM that emitted the spawn
	// (decide.next) chose not to. Without this, synth-mode routing
	// is LLM-discretion — gpt-5.4 reliably routes tool-call turns but
	// often leaves the synthesizer un-routed because the prompt
	// guidance is one sentence in a long template. SynthDefaultModel
	// makes the decision deterministic for intents we know benefit.
	//
	// Same precedence as in["model"]: a non-empty in["model"] still
	// wins (explicit per-call override is the strongest signal). Only
	// fires when in["model"] is empty AND synthMode=true.
	//
	// The closure is consulted ONCE per coding_turn invocation, so
	// callers can capture mutable state (e.g. a pointer to the
	// session's classified intent) and have it read at handler-run
	// time rather than cfg-construction time.
	SynthDefaultModel func() string

	// ModelRouteResolver, when non-nil, is consulted for per-call
	// model overrides (in["model"]) before the harness gets
	// retargeted. Lets the handler honor endpoint-prefixed forms
	// emitted by decide.next ("chatterbox/coder"): the resolver
	// returns the (endpoint, bareModel) pair, and the handler calls
	// h.SetEndpoint(endpoint) + h.SetModel(bareModel) so the request
	// body carries just "coder" — same path used for configured
	// session endpoints via SetEndpoint elsewhere.
	//
	// When nil OR when the resolver returns ok=false, the handler
	// falls back to the legacy SetModel/SetAPIURL shape: slash → no
	// apiURL override, bare → local Ollama. Suitable for callers
	// (cortex run, cortex code) that don't have a config to consult.
	ModelRouteResolver func(modelID string) (*llm.EndpointConfig, string, bool)
}

// synthesizerDirective is appended to the user prompt when a
// coding_turn is invoked in synthesize mode (attrs.synthesize=true,
// set by decide.next on the trailing synthesizer node). It tells the
// model the contract: answer from prior context, or emit a single
// "NEED_MORE: <next action>" line that the handler parses and turns
// into a follow-up decide.next spawn. That's how multi-hop emerges
// as additional DAG nodes instead of getting hidden inside one
// coding_turn's agent loop — see memory: project-multi-hop-via-spawn.
const synthesizerDirective = `

---

SYNTHESIZER MODE. Use the PRIOR STEPS context above as your sole source of truth. No tools are available to you this turn — the harness has stripped them.

Your job is to answer the user's question with EVIDENCE, never with hedging. Two responses are valid:

  ANSWER — every claim in your answer is backed by a concrete citation from the prior context (file path, line number, or quoted snippet). For multi-item questions (audits, comparisons, enumerations), produce one line per item paired with its evidence; do NOT summarize.

  NEED_MORE — you cannot answer fully because one specific read or shell command is missing. Respond with EXACTLY one line, nothing else:
    NEED_MORE: <one concrete next action in the intent grammar — e.g. "read pkg/cognition/dag/ops/decide_next.go" or "read internal/harness/dagnode/coding_turn.go lines 380-450" or "shell: grep -rn 'NewNextHandler' --include=*.go ." or "list internal/harness">
  The harness will run that action and route the result back to you as a fresh synthesis turn.

Hard rules:
- NEVER write "not directly confirmed", "not seen in the manifest", "the README mentions X but I cannot verify", or any other variant of "I don't know but here's a guess." Those are hedges. Replace each hedge with either (a) the actual citation that confirms or refutes the claim, or (b) a NEED_MORE for the read that would.
- NEVER invent a name, line number, function signature, or directory you have not seen verbatim in the prior context. Pattern-matching "looks plausible from the project shape" is hallucination.
- For multi-item questions: each item is independent. If you can verify 9 of 10 claims and one needs more reading, emit NEED_MORE for the 10th. Don't ship a partial audit that omits the unverified one — the harness will route hop-2 back to you with the missing read.

Wrong patterns to avoid (these are the failure mode this directive exists to prevent):
  Prior context: a list_dir of root and reads of README, go.mod, manifests.
  WRONG: "The DAG engine lives in pkg/cognition/dag/, but this is not directly confirmed in the codebase structure."
  WHY WRONG: the list_dir of root showed (or didn't show) "pkg" as an entry — cite which it was. If you didn't list pkg/cognition, NEED_MORE: list pkg/cognition. Don't say "not directly confirmed."

  Prior context: "coding_turn.go:436: h.SetNoTools(true)" from grep, no surrounding file content.
  WRONG: "The call is in function NewCodingTurnNode" (guessed name).
  RIGHT: NEED_MORE: read internal/harness/dagnode/coding_turn.go lines 380-450 (or simpler: rely on the read_file tool's enclosing_symbol field once you read any slice).

Termination: NEED_MORE on the SAME concrete read twice in a row means the read isn't producing the answer — at that point, answer with the evidence you have and explicitly note which sub-claim remains unverified ("unverified: <one sentence on what would resolve it>"). That is the ONLY shape in which a hedge is acceptable; everywhere else, cite or NEED_MORE.`

// needMoreMarker is the structured suffix the synthesizer emits when
// it needs another hop. Parsed by the handler to materialize a
// follow-up decide.next spawn. Keep this stable — the prompt above
// instructs the model to emit it verbatim.
const needMoreMarker = "NEED_MORE:"

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
// isSynthMode reports whether the spawn carried attrs.synthesize=true.
// Tolerant of the JSON-emitted-numeric form decide.next's upstream LLM
// sometimes uses (round-tripping booleans through JSON as 1.0 /
// float64). The synthMode flag drives both the prompt directive and
// the SynthDefaultModel routing fallback, so a single recognizer keeps
// the two branches consistent.
func isSynthMode(in map[string]any) bool {
	if v, ok := in["synthesize"].(bool); ok {
		return v
	}
	if f, ok := in["synthesize"].(float64); ok {
		return f != 0
	}
	return false
}

func NewCodingTurnHandler(cfg CodingTurnConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		prompt, _ := in["prompt"].(string)
		if prompt == "" {
			return dag.NodeResult{}, fmt.Errorf("decide.coding_turn: prompt input is required")
		}

		// Model selection. In synth-mode, the deterministic
		// SynthDefaultModel WINS over the upstream LLM's attrs.model
		// pick when both are present — the REPL knows the session's
		// classified intent and the LLM doesn't. Without this
		// inversion, gpt-5.4 reliably picks chatterbox/coder for audit
		// synth turns (saw real hallucinations in Q3 baseline runs)
		// because it can't see the intent — Path B's whole point is
		// that the harness composes a stronger answer than any one
		// model alone, which requires the harness to control routing
		// on review-class synth.
		//
		// Precedence (synth-mode):
		//   1. cfg.SynthDefaultModel() if non-empty  — deterministic intent-aware pick
		//   2. in["model"]                            — LLM's explicit pick
		//   3. cfg.Model                              — registration default
		//
		// Precedence (non-synth, agent loop):
		//   1. in["model"]              — LLM's explicit pick wins (per-node routing)
		//   2. cfg.Model                — registration default
		model := cfg.Model
		synth := isSynthMode(in)
		var synthPick string
		if synth && cfg.SynthDefaultModel != nil {
			synthPick = cfg.SynthDefaultModel()
		}
		switch {
		case synth && synthPick != "":
			model = synthPick
		default:
			if m, ok := in["model"].(string); ok && m != "" {
				model = m
			}
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
		// constructs its client fresh per session reading h.model +
		// h.apiURL / h.endpoint, so SetModel/SetAPIURL/SetEndpoint
		// takes effect on the next RunSessionWithResult call.
		//
		// The harness is short-lived (one chain execution); no need
		// to restore the original model after — the next turn builds
		// a fresh one via HarnessFactory.
		//
		// Endpoint-prefixed overrides ("chatterbox/coder") route via
		// ModelRouteResolver when wired: the resolver strips the
		// prefix and returns the endpoint config so the API request
		// body carries just "coder" (LiteLLM/Lemonade/etc reject the
		// prefixed form). Without a resolver, falls back to the
		// slash-vs-bare-name apiURLForModel heuristic.
		if model != "" && model != cfg.Model {
			if cfg.ModelRouteResolver != nil {
				if ep, bareModel, ok := cfg.ModelRouteResolver(model); ok {
					h.SetEndpoint(ep)
					h.SetModel(bareModel)
				} else {
					h.SetModel(model)
					h.SetAPIURL(apiURLForModel(model))
				}
			} else {
				h.SetModel(model)
				h.SetAPIURL(apiURLForModel(model))
			}
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
		// Per-invocation cfg override: derive the chunker's emission
		// budget from the current turn's Budget (intent + n_ctx). The
		// dispatcher captures cfg by value, so we mutate a local copy
		// rather than the long-lived registration-time cfg — keeps
		// callers that haven't wired Budget.Intent (e.g. eval suites)
		// on the legacy MaxEmittedChunks=8 path.
		dispatchCfg := cfg
		if emittedCap := budget.EmittedTokensCap(); emittedCap > 0 && dispatchCfg.ToolOutputEmittedTokens == 0 {
			dispatchCfg.ToolOutputEmittedTokens = emittedCap
		}
		if cfg.ActRegistry != nil {
			h.SetDispatcher(NewActDispatcher(dispatchCfg, parentNodeID, prompt, &spawnedChildren))
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

		// Synthesizer mode: append the structured-response directive so
		// the model knows to emit NEED_MORE: <next-action> when prior
		// context is insufficient (instead of guessing or refusing).
		// Parsed back into a follow-up decide.next spawn below. The
		// directive only goes in when the caller (typically decide.next)
		// explicitly sets attrs.synthesize=true; freestanding
		// coding_turn invocations are unchanged.
		synthMode := isSynthMode(in)
		if synthMode {
			prompt = prompt + synthesizerDirective
			// Enforce the directive at the protocol level: strip the
			// tool surface entirely so the model literally cannot
			// agent-loop its way out of answer-or-NEED_MORE. Without
			// this the prior turn observed the synthesizer ignoring
			// the prompt directive and running 4+ intra-loop tool
			// calls instead of emitting a clean NEED_MORE that becomes
			// an emergent hop. See memory: project-multi-hop-via-spawn.
			h.SetNoTools(true)
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
		// In synth mode, strip any trailing NEED_MORE: line from lr.Final
		// BEFORE the callback fires — the marker is the harness's internal
		// multi-hop signal, not user-facing terminal text. finalizeSynthFinal
		// also substitutes an honest fallback when stripping leaves nothing
		// (so a dead-ended hop never surfaces empty). See its doc for the
		// three outcomes. rawFinalBeforeStrip preserves the literal emission
		// for the spawn branch's re-parse below.
		var rawFinalBeforeStrip string
		if synthMode {
			lr.Final, rawFinalBeforeStrip = finalizeSynthFinal(lr.Final)
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
		// Multi-hop spawn: when the synthesizer emits the NEED_MORE:
		// marker, parse the next-action question and spawn a fresh
		// decide.next carrying it. The originating user prompt rides
		// along on the spawned node's attrs so downstream synthesizers
		// know what question the whole chain is ultimately answering.
		// Caps hops at 3 to bound runaway — three follow-up rounds is
		// far past the point where a 4th would help; the model should
		// have answered by then with whatever it has.
		//
		// Per project-multi-hop-via-spawn: multi-hop emerges as DAG
		// nodes, not as intra-loop tool calls. The "response" field is
		// elided when a spawn fires — the final answer comes from the
		// follow-up chain's terminal synthesizer.
		if synthMode {
			// Parse from the pre-strip Final when we stripped, otherwise
			// from lr.Final directly. parseNeedMore needs to see the
			// marker we removed for the callback.
			needMoreSource := lr.Final
			if rawFinalBeforeStrip != "" {
				needMoreSource = rawFinalBeforeStrip
			}
			if nextQ, ok := parseNeedMore(needMoreSource); ok {
				hopDepth := readHopDepth(in)
				if hopDepth < maxHopDepth {
					rootPrompt := rootPromptFromIn(in, prompt)
					// Build a composite prompt for the follow-up
					// planner so it produces a "act + synthesize"
					// plan, not just the standalone action. Without
					// this framing the planner sees "read foo.go" as
					// a single-node task, emits just decide.tool_call,
					// and the chain ends with no synthesizer — the
					// user gets no final answer. Naming the root
					// question + asking for a trailing synthesize=true
					// coding_turn nudges the planner toward the right
					// shape every time.
					childPrompt := fmt.Sprintf(
						"Continue resolving the user's original question:\n  %q\n\nNext concrete action this turn:\n  %s\n\nPlan: perform the action above, then add a trailing decide.coding_turn with synthesize=true whose prompt asks for the final answer to the original question (it may emit NEED_MORE: again if the action's output is still insufficient).",
						rootPrompt, nextQ,
					)
					childAttrs := map[string]any{
						"prompt":     childPrompt,
						hopDepthAttr: hopDepth + 1,
						// Inherit the root question so the final
						// synthesizer (which may be several hops deep)
						// can frame its answer toward what the user
						// actually asked.
						"root_user_prompt": rootPrompt,
					}
					return dag.NodeResult{
						Out: map[string]any{
							"response":   "",
							"need_more":  nextQ,
							"hop_depth":  hopDepth,
							"turns":      hr.AgentTurnsTotal,
							"tokens_in":  hr.TokensIn,
							"tokens_out": hr.TokensOut,
							"cost_usd":   hr.CostUSD,
						},
						Spawn: []dag.NodeSpec{{
							Function: dag.FuncDecide,
							Op:       "next",
							Attrs:    childAttrs,
						}},
						CostConsumed: dag.Cost{
							LatencyMS: latency,
							Tokens:    hr.TokensIn + hr.TokensOut,
						},
					}, nil
				}
				// Cap hit: fall through to the normal Out shape. lr.Final
				// here is finalizeSynthFinal's output — the synthesizer's
				// partial content, or an honest "couldn't finish, next
				// step was X" fallback when its only output was the marker.
				// Either way the response is non-empty, so the operator
				// sees the cap was the failure, not a silent stall.
			}
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

// hopDepthAttr is the Attr key used to track decide.coding_turn →
// decide.next → decide.coding_turn synthesis-hop recursion. Set by
// this handler on emitted follow-up decide.next spawns
// (parent_depth + 1); read on entry by readHopDepth to decide whether
// to permit another hop. Cap is maxHopDepth.
const hopDepthAttr = "_hop_depth"

// maxHopDepth bounds synthesizer follow-up rounds. Three hops covers
// realistic patterns (grep → identify file → read file → answer); a
// fourth almost always means the model is looping. The cap is the
// stop signal — the chain returns whatever the synthesizer had at
// that depth rather than spawning again.
const maxHopDepth = 3

// readHopDepth pulls the synthesis-hop depth out of the input map,
// tolerating both int and float64 (JSON unmarshalling can yield
// either). Missing or zero → 0.
func readHopDepth(in map[string]any) int {
	v, ok := in[hopDepthAttr]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

// rootPromptFromIn returns the originating user question for a
// follow-up hop. When upstream nodes set in["root_user_prompt"], that
// wins; otherwise the current node's prompt is taken as the root
// (first hop). Threading this through every follow-up means the
// terminal synthesizer can always frame its answer toward what the
// user actually asked, even three hops deep.
func rootPromptFromIn(in map[string]any, fallback string) string {
	if s, ok := in["root_user_prompt"].(string); ok && s != "" {
		return s
	}
	return fallback
}

// stripNeedMoreLine removes the NEED_MORE: marker line and everything
// after it from s. Returns (stripped, true) when the marker was found,
// (s, false) otherwise.
//
// The marker is the harness's internal multi-hop signal. The synth
// directive contract says NEED_MORE: must be on its own line with
// nothing else; in practice models sometimes append explanatory text
// after the marker, which is non-conformant content we don't want
// surfaced. Stripping through EOF gives a clean cut regardless.
//
// Empty preceding content yields "" — caller decides whether to
// substitute a "follow-up needed" annotation or accept silence (the
// next synthesis hop's text will overwrite the captured Final if it
// runs successfully).
func stripNeedMoreLine(s string) (string, bool) {
	idx := strings.Index(s, needMoreMarker)
	if idx < 0 {
		return s, false
	}
	// Scan backward from idx to find the start of the marker's line —
	// either index 0 or one past the most recent newline. Everything
	// from there to end-of-string gets dropped.
	lineStart := strings.LastIndexByte(s[:idx], '\n') + 1
	// Trim trailing whitespace + newlines on the preceding content so
	// the result doesn't end with a blank line that the marker was
	// preceded by.
	return strings.TrimRight(s[:lineStart], " \t\r\n"), true
}

// parseNeedMore extracts the "<next action>" payload from a
// synthesizer response that emitted NEED_MORE:. Returns ok=false
// when the marker isn't present or the payload is empty after trim.
//
// Matching is tolerant: the marker may appear anywhere in the
// response (some models prefix it with whitespace or a colon-line),
// but the payload is taken from the first non-empty line containing
// the marker through end-of-line. Anything past a trailing newline
// is dropped — the synthesizer's contract is one action per hop.
func parseNeedMore(s string) (string, bool) {
	idx := strings.Index(s, needMoreMarker)
	if idx < 0 {
		return "", false
	}
	rest := s[idx+len(needMoreMarker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// finalizeSynthFinal applies synth-mode NEED_MORE handling to a
// synthesizer's raw Final and returns (userFacingFinal, rawBeforeStrip).
// rawBeforeStrip is "" when there was no marker (so callers can re-parse
// the returned final), otherwise it carries the literal emission for the
// spawn branch's parseNeedMore.
//
// Three outcomes, all yielding a NON-EMPTY final so a dead-ended chain
// never surfaces empty terminal text:
//
//  1. No marker → the final is returned untouched.
//  2. Marker with content before it (a partial answer + a follow-up
//     request) → the marker line is stripped, the partial stays. A
//     successful hop-2's callback overwrites it; a refused hop-2 leaves
//     the partial, which is more useful than the fallback.
//  3. Marker as the ONLY content → stripping leaves nothing. Substitute
//     an honest "couldn't finish, here's the next step" fallback. This
//     is the case that previously produced an empty Final — an INVALID
//     cell that compromised the whole eval run — when the budget was
//     exhausted before hop-2 could schedule (the q2-cross-file-cortex
//     failure; see docs/eval-journal.md 2026-06-12).
func finalizeSynthFinal(raw string) (final, rawBeforeStrip string) {
	stripped, hadMarker := stripNeedMoreLine(raw)
	if !hadMarker {
		return raw, ""
	}
	if strings.TrimSpace(stripped) == "" {
		action, _ := parseNeedMore(raw)
		return needMoreFallback(action), raw
	}
	return stripped, raw
}

// needMoreFallback builds a non-empty terminal answer for the case where
// the synthesizer's only output was a NEED_MORE: line and the follow-up
// hop cannot run (budget refused or hop cap). An honest partial is a
// valid (if failing) answer; a silent empty is an INVALID cell.
func needMoreFallback(action string) string {
	msg := "I could not complete the answer within this turn's budget."
	if strings.TrimSpace(action) != "" {
		msg += " The next step needed was: " + action
	}
	return msg
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
			compressed, ct := compressToolOutput(ctx, cfg.ActRegistry, out, cfg.ToolOutputSalienceCap, cfg.ToolOutputEmittedTokens, intent, childID, qname)
			if ct != nil {
				out = compressed
				compressTrace = ct
			}
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
				MaxOutputTokens:  cfg.ToolOutputSalienceCap,
				MaxEmittedTokens: cfg.ToolOutputEmittedTokens,
				Intent:           intent,
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

		if cfg.TraceCB != nil {
			cfg.TraceCB(entry)
			if compressTrace != nil {
				cfg.TraceCB(*compressTrace)
			}
		}
		*spawnedChildren = append(*spawnedChildren, childID)

		return out, dispErr
	}
}

// compressToolOutput runs the dispatcher's salience hook against a
// single tool output. When the output's approx-token count exceeds
// maxTokens, it either:
//
//   - Chunks deterministically (qname in {act.read_file, act.run_shell}):
//     splits by line boundary, joins with "[chunk i/N, lines a-b]"
//     headers, emits an attend.chunk trace entry. No LLM call. The
//     calling model sees raw bytes with location headers.
//     maxEmittedTokens caps the TOTAL tokens across emitted chunks —
//     0 falls back to the legacy MaxEmittedChunks=8 chunk-count cap.
//
//   - Compresses via LLM (other qnames): invokes attend.compress through
//     reg with the intent string for salience extraction. Emits an
//     attend.compress trace entry.
//
// Returns (raw, nil) when no oversize handling was needed.
func compressToolOutput(ctx context.Context, reg *dag.Registry, raw string, maxTokens, maxEmittedTokens int, intent, parentNodeID, qname string) (string, *dag.TraceEntry) {
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
		joined, totalChunks, emitted := dag.ChunkOversize(raw, maxTokens, maxEmittedTokens)
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
				"chunks":             totalChunks,
				"emitted":            emitted,
				"max_tokens":         maxTokens,
				"max_emitted_tokens": maxEmittedTokens,
				"intent":             intent,
			},
			Salience: &dag.SalienceContract{
				MaxOutputTokens:  maxTokens,
				Intent:           intent,
				ChunkOnOversize:  true,
				MaxEmittedTokens: maxEmittedTokens,
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
	// Keep the advertised op surface in lockstep with the tool registry:
	// when the study gate swaps read_file -> study_file (see
	// library_service_cortex_harness.go), the planner must advertise the
	// tool that actually exists. A split surface had the model calling an
	// unregistered read_file while study_file sat invisible (eval-journal
	// 2026-06-10, the aborted pass B).
	if os.Getenv("CORTEX_STUDY_FILE") == "1" {
		names[0] = "study_file"
	}
	for _, n := range names {
		if err := RegisterActOpMetadata(reg, n, contracts[n], costs[n]); err != nil {
			return 0, fmt.Errorf("register act.%s: %w", n, err)
		}
	}
	if err := reg.Register(ops.CompressSpec(ops.CompressConfig{Provider: provider})); err != nil {
		return 0, fmt.Errorf("register attend.compress: %w", err)
	}
	// attend.compact rides the same provider — rolling re-summarization
	// for working memory. (attend.accumulate was removed: study_file's
	// coverage subsumes the per-tool-call accumulator; see docs/study-file.md.)
	if err := reg.Register(ops.CompactSpec(ops.CompactConfig{Provider: provider})); err != nil {
		return 0, fmt.Errorf("register attend.compact: %w", err)
	}
	return len(names) + 2, nil
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
