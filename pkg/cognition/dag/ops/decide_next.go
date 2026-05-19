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
}

// recursionDepthAttr is the Attr key used to track decide.next →
// decide.next recursion. Set by the handler on emitted decide.next
// spawns (parent_depth + 1); read on entry to decide whether further
// decide.next emission is allowed.
const recursionDepthAttr = "_dnext_depth"

// NextSpec returns the dag.NodeSpec for decide.next.
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
The user said:
"""
{{PROMPT}}
"""

{{CONTEXT}}

DEFAULT: emit a single decide.coding_turn that handles the whole request. Most prompts — code changes, questions, prose — fit this shape. The coding_turn's own agent loop will use its tools (read_file, write_file, run_shell, list_dir, cortex_search) as needed.

Only use multi-node plans when a step's result fundamentally changes what should happen next. Do NOT loop "search, then decide" — if search returns nothing useful, don't search again.

Common shapes:

  Direct (most prompts — code, questions, prose):
    [{"op":"decide.coding_turn","attrs":{"prompt":"<user prompt verbatim>"}}]

  Explore-then-answer (user asks about an unfamiliar codebase; bias the coding_turn toward tool use via a steered prompt):
    [{"op":"decide.coding_turn","attrs":{"prompt":"First call list_dir(\".\") and read_file(\"README.md\"), then answer: <user prompt>"}}]

  Search-then-act (you have specific reason to believe prior captures contain the answer):
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
		recDepth := readDecideNextDepth(in)

		// Per-call provider resolution. attrs.model on this decide.next
		// spawn (set by an emitted-node materializer one level up) lets
		// the LLM route this classification through a different model
		// than the session default — the small-model-amplifier path
		// where, say, a 3B JSON-disciplined classifier handles the
		// router role while a 14B coder handles the work.
		provider, providerSrc := resolveNextProvider(cfg, in)

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
		systemPrompt := renderNextPrompt(prompt, opCatalog, cfg.ModelCatalog, historySummary)
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
//  1. If attrs.model is set AND cfg.ProviderFactory is set, route this
//     call through factory.Get(model). On factory error, fall through
//     to step 2 so a typo'd model id doesn't sink the chain.
//  2. cfg.Provider (the session default).
//
// Returns the provider plus a short tag describing where it came from
// — surfaced in fallback reasoning when classification fails so it's
// debuggable which model errored out.
func resolveNextProvider(cfg NextConfig, in map[string]any) (llm.Provider, string) {
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
func renderNextPrompt(prompt, opCatalog, modelCatalog, historySummary string) string {
	out := nextPrompt
	out = strings.ReplaceAll(out, "{{PROMPT}}", prompt)
	out = strings.ReplaceAll(out, "{{OPS}}", opCatalog)
	out = strings.ReplaceAll(out, "{{MODELS}}", modelCatalog)

	contextBlock := ""
	if historySummary != "" {
		contextBlock = "Recent context: " + historySummary + "\n"
	}
	out = strings.ReplaceAll(out, "{{CONTEXT}}", contextBlock)
	return out
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
