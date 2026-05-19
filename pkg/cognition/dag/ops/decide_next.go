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
	// Provider classifies the next step. Nil → mechanical fallback
	// (spawn one decide.coding_turn with the user prompt).
	Provider llm.Provider

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

	// MaxLatencyMS caps the classifier call. Defaults to 5000 — small
	// classifier prompt, but on a 14B local model end-to-end can hit
	// ~3-4s. Tighten when local hardware improves.
	MaxLatencyMS int
}

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

const nextPrompt = `You compose a DAG that will handle the user's request. You don't execute the work yourself — you emit a sequence of nodes to spawn, and the executor schedules them in order.

Available ops (function.op(params) - what it does):
{{OPS}}
Available models (route nodes by setting attrs.model; omit to use the session default):
{{MODELS}}
The user said:
"""
{{PROMPT}}
"""

{{CONTEXT}}

Emit a JSON object describing the nodes to spawn. Keep it short — 1 to 4 nodes per call. If a step's result will change what should run next (e.g., search results that change the answer), end with another decide.next so you can re-decide.

Common shapes (concrete examples, not a fixed menu — compose as you see fit):

  Explore-then-answer (user asks about an unfamiliar codebase):
    [{"op":"decide.coding_turn","attrs":{"prompt":"List the workdir, read README and the most prominent source files, then answer: <user prompt>"}}]

  Direct prose (user asks a general question with no workdir grounding):
    [{"op":"decide.coding_turn","attrs":{"prompt":"<user prompt>"}}]

  Search-then-act (relevant prior captures may exist):
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
		maxLatency = 5000
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

		// Mechanical fallback: no provider or budget-exhausted.
		if cfg.Provider == nil || !budget.CanAfford(nextCostHint) {
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
		respText, err := cfg.Provider.GenerateWithSystem(classifyCtx, "Compose the next nodes.", systemPrompt)
		if err != nil {
			return fallbackSpawn(prompt, "fallback: classifier error: "+err.Error(), started), nil
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
