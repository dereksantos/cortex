// Package ops — decide.next.
//
// The dynamic-DAG steering op. Each REPL turn seeds [sense.prompt →
// decide.next]; decide.next inspects the prompt + per-turn state and
// emits a sequence of nodes to spawn next. Different prompts produce
// different trees; budget decay is handled entirely by the executor.
//
// Stage 7 (DAG-generation slice): the LLM no longer picks one of four
// fixed arms — it composes a sequence of {op, attrs, model?} specs
// from the live op catalogue + live model catalogue injected into its
// system prompt. Patterns that emerge from observed model behavior
// can be compressed to symbols later; for now expressivity wins.
//
// Output schema:
//
//	{
//	  "nodes": [
//	    {"op": "<qualified>", "attrs": {...}, "model": "<optional>"},
//	    ...
//	  ],
//	  "reasoning": "<one short sentence>"
//	}
//
// On parse error or empty nodes, falls back to a single
// decide.coding_turn spawn with the user prompt — the chain never
// stalls. Per-node attrs.model is threaded through to the spawned
// NodeSpec.Attrs so handlers that respect it (decide.coding_turn's
// per-node override at internal/harness/dagnode) pick it up.
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// NextConfig wires NewNextHandler.
type NextConfig struct {
	// Provider is the default classifier Provider. Used when the
	// per-call attrs.model is empty OR ProviderFactory is nil. Nil →
	// mechanical fallback (spawn one decide.coding_turn with the
	// prompt).
	Provider llm.Provider

	// ProviderFactory, when set, resolves per-call decide.next model
	// overrides. The LLM can emit a decide.next spawn with
	// `model: "<id>"` and that recursive classification routes
	// through factory.Get(id). Nil → attrs.model on decide.next is
	// ignored and cfg.Provider is used for every call. This is what
	// lets the steering layer compose multiple specialized small
	// classifiers (e.g., a fast 3B JSON-disciplined router for most
	// turns, a 14B model for hard re-decisions).
	ProviderFactory llm.ProviderFactory

	// Registry is consulted to validate emitted op qualified names AND
	// to build the {{OPS}} catalog at call time. Nil → skip validation
	// (every emitted op is trusted) and inject an empty catalog block.
	// Captured by reference: registrations after NextSpec is built are
	// visible to the handler at call time, which sidesteps the
	// chicken-and-egg of "decide.next needs the catalog but the
	// catalog must list decide.next."
	Registry *dag.Registry

	// ModelCatalog, when non-empty, is substituted into the prompt's
	// {{MODELS}} block — the LLM sees which models it can route
	// individual nodes to via attrs.model. Caller builds this from
	// listOllamaModels + OpenRouterClient.ListModels.
	ModelCatalog string

	// MaxFanout caps how many nodes one decide.next call may emit.
	// Defaults to 4 — a confused LLM can't blow the turn budget on a
	// single decision.
	MaxFanout int

	// MaxRecursionDepth caps decide.next → decide.next recursion. The
	// LLM can compose multi-step plans, but it tends to spin (e.g.,
	// "search, then decide" loops) if it can't see whether the prior
	// step changed anything. When the inherited depth attribute hits
	// this cap, the handler drops any further decide.next from the
	// emitted spawn list (other ops still emit normally). Defaults
	// to 3 — enough for legitimate search-then-act, search-again-
	// with-results-then-act patterns, but not enough to runaway.
	MaxRecursionDepth int

	// MaxLatencyMS caps the classifier call. Defaults to 30000 — 7B+
	// local models digesting the op + model catalogues and emitting
	// JSON can easily take 10-15s. Tighten when picking smaller
	// dedicated classifier models (3B+ Phi/Llama) or running on
	// faster hardware.
	MaxLatencyMS int

	// SystemBoundaries, when non-empty, is rendered into the prompt's
	// {{BOUNDARIES}} block. The caller supplies a short fact-sheet
	// describing the running model's capability class + the
	// per-tool-call salience cap, so decide.next plans within its
	// physical-system bounds (Phase 3 Slice 2): tight cap + small
	// model → favor fan-out; loose cap + large model → fewer larger
	// nodes are OK. Empty → no boundary block; pre-Phase-3 behavior.
	SystemBoundaries string
}

// recursionDepthAttr is the Attr key used to track decide.next →
// decide.next recursion. Set by the handler on emitted decide.next
// spawns (parent_depth + 1); read on entry to decide whether further
// decide.next emission is allowed.
const recursionDepthAttr = "_dnext_depth"

// NextSpec returns the dag.NodeSpec for decide.next.
//
// Requires declares the capability preference chain the executor's
// Router uses to pick the per-node provider. decide.next is a
// *planning* node — it reads the user's prompt + the op catalog and
// composes a multi-step DAG. That's a reasoning task, not a
// function-call task. Routing it to a tool-call specialist (xLAM,
// phi-3-mini-tools, etc.) fails: those models are trained on the
// narrow "given a tool catalog, emit one function call" shape and
// respond in English when asked to compose arbitrary JSON node lists.
//
// Chain: prefer a reasoning model when one is tagged; fall back to
// any tool-callable chat model (coder, generalist). On stacks where
// no reasoning specialist is auto-detected, this falls through to
// the operator's session default.
func NextSpec(cfg NextConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "next",
		Description: "compose the next set of nodes to spawn based on the prompt + available ops + available models",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
			{Name: "history_summary", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "nodes", Type: "[]NodeSpec"},
			{Name: "reasoning", Type: "string"},
		},
		Cost:      nextCostHint,
		Exposable: true, // the LLM can recurse into decide.next
		Requires:  []string{llm.CapReasoning, llm.CapToolCalling},
		Handler:   NewNextHandler(cfg),
	}
}

// nextCostHint — sized for a small composition call. The prompt is
// modest (catalogs + user prompt), output is short (a JSON array of
// 1-4 specs). Real wall-time settles to model + hardware.
var nextCostHint = dag.Cost{LatencyMS: 5000, Tokens: 500}

const nextPrompt = `You compose a tiny DAG to handle the user's request. You don't execute the work yourself — you emit a list of nodes to spawn, and the executor runs them in order.

Available ops (function.op(params) - what it does):
{{OPS}}
Available models (route a node by setting attrs.model; omit to use the session default):
{{MODELS}}
{{BOUNDARIES}}The user said:
"""
{{PROMPT}}
"""

{{CONTEXT}}

Match the plan to the shape of the request:

- Trivial / conversational ("hi", "what is 17 × 23?", "explain X conceptually"): 1 node. Just a decide.coding_turn with the prompt.
- Code change in a known file ("add foo to bar.py"): 2-3 nodes. Read, write, optionally verify.
- Exploration ("what does this project do?", "how does X work in this codebase?"): 3-5 nodes. Use decide.tool_call to perform specific tool actions (list dir, read README, read manifest), then a final decide.coding_turn that synthesizes the answer. The synthesis node automatically sees prior step outputs as context — don't ask it to re-fetch what's already been read.

decide.tool_call is a SPECIALIST node that turns a natural-language intent into a structured tool call (list_dir, read_file, run_shell, write_file). Use it when you need a specific, focused tool action — it's more reliable than a full coding_turn for one-shot reads.

You can also re-enter decide.next when a step's result will fundamentally change what should happen next (e.g., after a search). But do NOT loop "search → decide → search" — if a search returns nothing, don't search again.

Each emitted node should have a CONCRETE prompt or intent naming the specific file/dir/action, not a vague goal. Vague prompts produce vague work.

Common shapes:

  Direct (conversational or simple Q&A):
    [{"op":"decide.coding_turn","attrs":{"prompt":"<user prompt verbatim>"}}]

  Exploration (tool_call for each read, coding_turn synthesizes at the end):
    [{"op":"decide.tool_call","attrs":{"intent":"list everything in the workdir root"}},
     {"op":"decide.tool_call","attrs":{"intent":"read README.md"}},
     {"op":"decide.tool_call","attrs":{"intent":"read package.json or the equivalent project manifest"}},
     {"op":"decide.coding_turn","attrs":{"prompt":"Using the prior step outputs, answer in 2-3 sentences: '<user prompt>'"}}]

  Code change:
    [{"op":"decide.tool_call","attrs":{"intent":"read the file <path>"}},
     {"op":"decide.coding_turn","attrs":{"prompt":"Apply the change: <user prompt>. Then run the project's build/test command."}}]

  Search-then-act (specific reason to believe prior captures help):
    [{"op":"remember.vector_search","attrs":{"query":"<topic>"}},
     {"op":"decide.next","attrs":{"prompt":"<user prompt>"}}]

Respond with ONLY JSON, no markdown fences, no prose before or after:

{
  "nodes": [
    {"op": "<qualified.op>", "attrs": {...}, "model": "<optional>"}
  ],
  "reasoning": "<one short sentence>"
}`

// emittedNode is the JSON shape the LLM produces per spawn. Matches
// the example in the system prompt. Unknown fields are tolerated by
// Go's json package (already permissive by default).
type emittedNode struct {
	Op    string         `json:"op"`
	Attrs map[string]any `json:"attrs"`
	Model string         `json:"model,omitempty"`
}

// nextResponse parses the classifier's full response.
type nextResponse struct {
	Nodes     []emittedNode `json:"nodes"`
	Reasoning string        `json:"reasoning"`
}

// NewNextHandler returns the dag.Handler for decide.next.
//
// Behavior:
//   - Reads `prompt` (required) and `history_summary` (optional).
//   - If Provider == nil OR budget can't afford the cost hint → thin
//     fallback: spawn one decide.coding_turn with the prompt.
//   - Otherwise: render the prompt with op + model catalogs, ask the
//     provider, parse the JSON response, validate each emitted op
//     against Registry (when set), drop unknowns with a logged
//     reasoning suffix, cap to MaxFanout, thread attrs.model through.
//   - On parse error or empty nodes list → same thin fallback.
//
// The handler intentionally has no rule-based language heuristics —
// the floor is 7B+. Sub-7B steering should use a different op.
func NewNextHandler(cfg NextConfig) dag.Handler {
	maxFanout := cfg.MaxFanout
	if maxFanout <= 0 {
		maxFanout = 4
	}
	maxLatency := cfg.MaxLatencyMS
	if maxLatency <= 0 {
		maxLatency = 30000
	}
	maxRecursion := cfg.MaxRecursionDepth
	if maxRecursion <= 0 {
		maxRecursion = 3
	}

	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt, _ := in["prompt"].(string)
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.next: 'prompt' (string) is required")
		}
		historySummary, _ := in["history_summary"].(string)
		// When the caller didn't pass an explicit history_summary,
		// fall back to the latest attend.accumulate snapshot from
		// turn state — the bounded working memory built by earlier
		// nodes in this turn. Caller-passed history_summary wins so
		// callers can still inject cross-turn context (compressed
		// session summaries) at the chain root.
		if historySummary == "" {
			historySummary = dag.LatestAccumulatorSnapshot(ctx)
		}
		recDepth := readDecideNextDepth(in)

		// Per-call provider resolution. Budget.Provider (set by the
		// executor's Router under the new per-node routing — see
		// docs/per-node-routing-plan.md slice 3) wins; the legacy
		// attrs.model + factory path is preserved as fallback so the
		// LLM-emitted per-call model override keeps working for callers
		// that haven't adopted the Router yet.
		provider, providerSrc := resolveNextProvider(cfg, in, budget)

		// Mechanical fallback: no provider available or budget exhausted.
		if provider == nil || !budget.CanAfford(nextCostHint) {
			return fallbackSpawn(prompt, "fallback: no provider or budget exhausted", started), nil
		}

		// Ask the classifier.
		classifyCtx, cancel := context.WithTimeout(ctx, time.Duration(maxLatency)*time.Millisecond)
		defer cancel()
		opCatalog := ""
		if cfg.Registry != nil {
			opCatalog = FormatOpCatalog(cfg.Registry)
		}
		systemPrompt := renderNextPrompt(prompt, opCatalog, cfg.ModelCatalog, historySummary, cfg.SystemBoundaries, budget)
		respText, err := provider.GenerateWithSystem(classifyCtx, "Compose the next nodes.", systemPrompt)
		if err != nil {
			return fallbackSpawn(prompt, "fallback: classifier error ("+providerSrc+"): "+err.Error(), started), nil
		}

		var parsed nextResponse
		if perr := parseNextResponse(respText, &parsed); perr != nil {
			return fallbackSpawn(prompt, fmt.Sprintf("fallback: parse error: %v (raw=%.120s)", perr, respText), started), nil
		}
		if len(parsed.Nodes) == 0 {
			return fallbackSpawn(prompt, "fallback: empty nodes list from classifier", started), nil
		}

		// Validate + materialize emitted nodes.
		var (
			spawn    []dag.NodeSpec
			skipped  []string
			accepted int
		)
		for _, n := range parsed.Nodes {
			if accepted >= maxFanout {
				skipped = append(skipped, n.Op+"(fanout-cap)")
				continue
			}
			spec, ok := materializeEmittedNode(n, cfg.Registry)
			if !ok {
				skipped = append(skipped, n.Op+"(unknown-op)")
				continue
			}
			// Recursion cap: when the inherited depth is at or above
			// the configured max, drop any further decide.next emission.
			// Other ops still spawn — this stops the "search, then
			// decide, then search again" runaway pattern without
			// blocking legitimate multi-step plans.
			if spec.QualifiedName() == "decide.next" {
				if recDepth >= maxRecursion {
					skipped = append(skipped, "decide.next(recursion-cap)")
					continue
				}
				if spec.Attrs == nil {
					spec.Attrs = make(map[string]any)
				}
				spec.Attrs[recursionDepthAttr] = recDepth + 1
			}
			spawn = append(spawn, spec)
			accepted++
		}
		if len(spawn) == 0 {
			return fallbackSpawn(prompt, "fallback: all emitted nodes invalid: "+strings.Join(skipped, ","), started), nil
		}

		latency := int(time.Since(started).Milliseconds())
		reasoning := parsed.Reasoning
		if len(skipped) > 0 {
			reasoning = reasoning + " [skipped: " + strings.Join(skipped, ",") + "]"
		}
		return dag.NodeResult{
			Out: map[string]any{
				"reasoning": reasoning,
				"nodes":     parsed.Nodes,
			},
			Spawn:        spawn,
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(respText)},
		}, nil
	}
}

// resolveNextProvider chooses the Provider to use for THIS decide.next
// call. Resolution order:
//
//  1. Budget.Provider — set by the executor's Router when the new
//     per-node routing is wired (docs/per-node-routing-plan.md slice 3).
//     The Router already applied Attrs["model"] override + Requires
//     chain, so the handler doesn't re-check those when Budget.Provider
//     is present.
//  2. Legacy attrs.model + factory — the LLM-emitted per-call override
//     path. On factory error, falls through to step 3 so a typo'd
//     model id doesn't sink the chain.
//  3. cfg.Provider (the session default).
//
// Returns the provider plus a short tag describing where it came from
// — surfaced in fallback reasoning when classification fails so it's
// debuggable which model errored out.
func resolveNextProvider(cfg NextConfig, in map[string]any, budget dag.Budget) (llm.Provider, string) {
	if budget.Provider != nil {
		return budget.Provider, "budget:" + budget.Provider.Name()
	}
	if cfg.ProviderFactory != nil {
		if m, _ := in["model"].(string); m != "" {
			if p, err := cfg.ProviderFactory.Get(m); err == nil && p != nil {
				return p, "factory:" + m
			}
		}
	}
	return cfg.Provider, "default"
}

// readDecideNextDepth pulls the recursion-depth Attr out of the
// handler's input map, tolerating both int and float64 (JSON
// unmarshalling can yield either). Missing or zero → 0.
func readDecideNextDepth(in map[string]any) int {
	v, ok := in[recursionDepthAttr]
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

// renderNextPrompt substitutes the runtime catalogs + user prompt
// into the static template. Empty catalogs simply leave the section
// header in place — the LLM still sees "Available ops:" with no
// content, which is intentional (signals "operate from priors").
//
// boundaries is the caller-supplied capability fact-sheet (Phase 3
// Slice 2). budget is the live remaining turn budget — the renderer
// summarizes both into a {{BOUNDARIES}} block so the LLM plans
// within its physical-system limits. Empty boundaries + zero budget
// → no block; pre-Phase-3 behavior preserved.
func renderNextPrompt(prompt, opCatalog, modelCatalog, historySummary, boundaries string, budget dag.Budget) string {
	out := nextPrompt
	out = strings.ReplaceAll(out, "{{PROMPT}}", prompt)
	out = strings.ReplaceAll(out, "{{OPS}}", opCatalog)
	out = strings.ReplaceAll(out, "{{MODELS}}", modelCatalog)

	contextBlock := ""
	if historySummary != "" {
		// Labelled as "working memory" so the LLM treats the
		// content as authoritative (already-compressed bounded
		// state from prior nodes in this turn), not just "recent
		// chatter". This is the bridge to attend.accumulate.
		contextBlock = "Working memory so far (already-compressed; use directly, do NOT re-fetch what's here):\n\"\"\"\n" + historySummary + "\n\"\"\"\n"
	}
	out = strings.ReplaceAll(out, "{{CONTEXT}}", contextBlock)
	out = strings.ReplaceAll(out, "{{BOUNDARIES}}", formatBoundariesBlock(boundaries, budget))
	return out
}

// formatBoundariesBlock renders the self-awareness section of
// decide.next's prompt — the caller-supplied capability fact-sheet
// plus the live remaining-budget summary. Returns "" when neither
// signal is present so the slot collapses cleanly.
//
// Phase 3 Slice 2: the LLM sees its own physical-system limits at
// plan time and chooses fanout shape accordingly — small model + tight
// budget → many narrow nodes; large model + loose budget → fewer
// larger nodes. The fact-sheet is opaque to this op so the REPL can
// evolve what it surfaces (capability class today; pre-ingest summary
// pointer in a later phase) without touching the prompt template.
func formatBoundariesBlock(boundaries string, budget dag.Budget) string {
	hasBoundaries := strings.TrimSpace(boundaries) != ""
	hasBudget := budget.LatencyMS > 0 || budget.Tokens > 0 || budget.OutputTokens > 0
	if !hasBoundaries && !hasBudget {
		return ""
	}
	var b strings.Builder
	b.WriteString("Physical-system limits for THIS turn (plan within them):\n")
	if hasBoundaries {
		b.WriteString(boundaries)
		if !strings.HasSuffix(boundaries, "\n") {
			b.WriteByte('\n')
		}
	}
	if hasBudget {
		fmt.Fprintf(&b, "- Remaining budget: latency=%dms, tokens=%d, depth=%d, output_tokens=%d\n",
			budget.LatencyMS, budget.Tokens, budget.Depth, budget.OutputTokens)
	}
	b.WriteString("\nPlanning guidance:\n")
	b.WriteString("- Tight output budget or small model → favor fan-out: many narrow decide.tool_call nodes, each producing a compressed deposit. Avoid one big decide.coding_turn that tries to hold everything in its working set.\n")
	b.WriteString("- Loose output budget or large model → fewer larger nodes are fine; a single decide.coding_turn that reads several files in its agent loop is OK.\n")
	b.WriteByte('\n')
	return b.String()
}

// materializeEmittedNode converts one emittedNode into a dag.NodeSpec
// ready for NodeResult.Spawn. Returns (spec, false) when the op
// can't be resolved against the registry.
func materializeEmittedNode(n emittedNode, reg *dag.Registry) (dag.NodeSpec, bool) {
	qname := strings.TrimSpace(n.Op)
	if qname == "" {
		return dag.NodeSpec{}, false
	}
	parts := strings.SplitN(qname, ".", 2)
	if len(parts) != 2 {
		return dag.NodeSpec{}, false
	}

	// Validate against registry when available. A spec the registry
	// hasn't seen is dropped — the executor would fail with
	// unknown_node anyway; better to surface the skip in this op's
	// reasoning than to take a trace-row hit downstream.
	if reg != nil {
		if _, err := reg.Get(qname); err != nil {
			return dag.NodeSpec{}, false
		}
	}

	attrs := n.Attrs
	if attrs == nil {
		attrs = make(map[string]any)
	}
	// Stage 7 contract: attrs.model is consumed by handlers that
	// support per-node model override (currently decide.coding_turn
	// at internal/harness/dagnode). Empty string means "use the
	// session default" — leave the key unset rather than spreading
	// empty strings through downstream code.
	if strings.TrimSpace(n.Model) != "" {
		attrs["model"] = n.Model
	}

	return dag.NodeSpec{
		Function: dag.CortexFunction(parts[0]),
		Op:       parts[1],
		Attrs:    attrs,
	}, true
}

// fallbackSpawn returns the chain-keep-walking result when the LLM
// can't be consulted or its output is unusable. Spawns one
// decide.coding_turn with the original user prompt so the chain
// produces SOMETHING even at degraded quality.
func fallbackSpawn(prompt, reasoning string, started time.Time) dag.NodeResult {
	return dag.NodeResult{
		Out: map[string]any{
			"reasoning": reasoning,
			"fallback":  true,
		},
		Spawn: []dag.NodeSpec{{
			Function: dag.FuncDecide, Op: "coding_turn",
			Attrs: map[string]any{"prompt": prompt},
		}},
		CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
	}
}

// parseNextResponse extracts the JSON object from the classifier's
// raw response. Tolerates leading/trailing whitespace and optional
// markdown fences ("```json ... ```") since some models can't help
// themselves.
func parseNextResponse(raw string, out *nextResponse) error {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		nl := strings.IndexByte(s, '\n')
		if nl > 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	return json.Unmarshal([]byte(s), out)
}
