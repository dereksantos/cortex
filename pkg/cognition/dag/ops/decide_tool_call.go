// Package ops — decide.tool_call.
//
// The specialist tool-caller node. The generalist reasoning model
// (decide.coding_turn) is good at choosing WHAT to do but unreliable
// at emitting structured `tool_calls` — especially small open-weight
// models on Ollama's OpenAI-compat shim, which routinely emit
// pseudo-tool-calls as fenced JSON inside the response body.
//
// decide.tool_call sidesteps that by routing the natural-language
// intent through a specialist function-calling model (xlam-1.5b,
// hermes-pro, functionary, etc. — anything trained specifically to
// emit JSON args). The specialist's response is the structured call,
// which decide.tool_call materializes as an act.<tool> spawn.
//
// This is the small-model-amplifier pattern made concrete: a 1.5B
// purpose-built model does what a 7B+ generalist can't reliably do
// itself, at 200-500ms per call.
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

// ToolCallConfig wires NewToolCallHandler.
type ToolCallConfig struct {
	// Provider is the default specialist (e.g., xlam-1.5b). Nil →
	// the chain falls back to a no-op spawn with the reasoning
	// "tool_call: no provider configured".
	Provider llm.Provider

	// ProviderFactory enables per-call routing via attrs.model on
	// emitted decide.tool_call spawns. Same shape as decide.next:
	// attrs.model + factory → factory.Get(model); otherwise default.
	ProviderFactory llm.ProviderFactory

	// Registry is read at call time to format the available-tools
	// section of the specialist's prompt (one line per registered
	// act.* op). Captured by reference so additions after
	// construction are visible.
	Registry *dag.Registry

	// MaxLatencyMS caps the specialist call. Defaults to 10000 — a
	// purpose-built 1.5B function-caller typically runs sub-second,
	// but the local OpenAI-compat shim can cold-start slowly. 10s is
	// generous enough for the first call and irrelevant once the
	// model is warm.
	MaxLatencyMS int

	// ToolOutputSalienceCap is the per-tool-call output-token cap
	// attached as a SalienceContract to every act.* node spawned by
	// this op. Zero means "no contract" — pre-salience-budgets
	// behavior. When > 0, the executor compresses any oversized act.*
	// output before depositing into turn state, so the synthesizer
	// downstream sees compact context rather than the raw file
	// payload. See docs/salience-budgets.md.
	ToolOutputSalienceCap int
}

// ToolCallSpec returns the NodeSpec for decide.tool_call.
func ToolCallSpec(cfg ToolCallConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "tool_call",
		Description: "convert a natural-language intent into a structured tool call; spawns the resolved act.<tool>",
		Inputs: []dag.ParamSpec{
			{Name: "intent", Type: "string", Required: true},
			{Name: "tools", Type: "[]string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "tool_name", Type: "string"},
			{Name: "tool_args", Type: "map"},
			{Name: "reasoning", Type: "string"},
		},
		Cost:      toolCallCostHint,
		Exposable: true,
		Handler:   NewToolCallHandler(cfg),
	}
}

var toolCallCostHint = dag.Cost{LatencyMS: 2000, Tokens: 200}

const toolCallPrompt = `You are a tool-call specialist. The user has a natural-language intent — convert it into a single, structured tool invocation.

Intent:
"""
{{INTENT}}
"""

Available act-typed tools (qualified name(input_param_name) - description):
{{TOOLS}}

Respond with ONLY JSON, no markdown fences, no prose before or after:

{
  "tool_name": "<qualified.op, e.g. act.read_file>",
  "args": {"<param>": "<value>", ...},
  "reasoning": "<one short sentence>"
}

Rules:
- tool_name must be one of the listed qualified names exactly.
- args fields must match the tool's input schema.
- Paths in args are workdir-relative; no absolute paths.
- Pick the single best tool; do not emit multiple calls.`

type toolCallResponse struct {
	ToolName  string         `json:"tool_name"`
	Args      map[string]any `json:"args"`
	Reasoning string         `json:"reasoning"`
}

// NewToolCallHandler returns the dag.Handler for decide.tool_call.
//
// Flow:
//  1. Resolve provider (attrs.model + factory, else cfg.Provider).
//  2. Render prompt with the registry-derived tool catalog.
//  3. Ask provider for the structured call.
//  4. Validate tool_name against the registry (must be a registered
//     act.* op marked Exposable).
//  5. Marshal args into JSON-string form (the act-op handler expects
//     `args: string` per AdaptToolAsAct's input schema).
//  6. Spawn act.<tool> with attrs.args (JSON string) + attrs.confirm
//     (auto-true; the user opted in by running cortex).
//
// Errors: missing intent → handler error. Provider nil, budget
// exhausted, parse error, validation miss → empty spawn + reasoning
// note. The chain keeps walking.
func NewToolCallHandler(cfg ToolCallConfig) dag.Handler {
	maxLatency := cfg.MaxLatencyMS
	if maxLatency <= 0 {
		maxLatency = 10000
	}

	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		intent, _ := in["intent"].(string)
		if strings.TrimSpace(intent) == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.tool_call: 'intent' (string) is required")
		}

		provider, providerSrc := resolveToolCallProvider(cfg, in)
		if provider == nil || !budget.CanAfford(toolCallCostHint) {
			return dag.NodeResult{
				Out: map[string]any{
					"reasoning": "skipped: no provider or budget exhausted",
					"fallback":  true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		toolsBlock := formatActToolsCatalog(cfg.Registry, in)
		sysPrompt := strings.ReplaceAll(toolCallPrompt, "{{INTENT}}", intent)
		sysPrompt = strings.ReplaceAll(sysPrompt, "{{TOOLS}}", toolsBlock)

		callCtx, cancel := context.WithTimeout(ctx, time.Duration(maxLatency)*time.Millisecond)
		defer cancel()
		respText, err := provider.GenerateWithSystem(callCtx, "Emit the tool call as JSON.", sysPrompt)
		latency := int(time.Since(started).Milliseconds())
		if err != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"reasoning": "specialist error (" + providerSrc + "): " + err.Error(),
					"fallback":  true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency},
			}, nil
		}

		var parsed toolCallResponse
		if perr := parseToolCallResponse(respText, &parsed); perr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"reasoning":   fmt.Sprintf("parse error: %v (raw=%.120s)", perr, respText),
					"fallback":    true,
					"raw_respone": respText,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(respText)},
			}, nil
		}

		spec, ok := materializeActSpawn(parsed.ToolName, parsed.Args, cfg.Registry)
		if !ok {
			return dag.NodeResult{
				Out: map[string]any{
					"reasoning": fmt.Sprintf("rejected tool name %q (not a registered exposable act.* op)", parsed.ToolName),
					"fallback":  true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(respText)},
			}, nil
		}

		// Attach a SalienceContract so the executor's post-handler
		// hook handles oversized act.* outputs before deposit. The
		// strategy depends on the tool:
		//
		//   - act.read_file / act.run_shell → deterministic line-based
		//     chunking (ChunkOnOversize=true). The calling model sees
		//     the actual bytes with "[chunk i/N, lines a-b]" headers;
		//     no LLM is in the read path. Preserves ground truth and
		//     lets the model re-fetch specific ranges if needed.
		//
		//   - Other act.* tools → LLM-mediated attend.compress, sized
		//     by Intent. Appropriate where genuine salience extraction
		//     is wanted (e.g. cortex_search results).
		if cfg.ToolOutputSalienceCap > 0 {
			spec.Salience = &dag.SalienceContract{
				MaxOutputTokens: cfg.ToolOutputSalienceCap,
				Intent:          intent,
				ChunkOnOversize: shouldChunkOnOversize(parsed.ToolName),
			}
		}

		return dag.NodeResult{
			Out: map[string]any{
				"tool_name": parsed.ToolName,
				"tool_args": parsed.Args,
				"reasoning": parsed.Reasoning,
			},
			Spawn:        []dag.NodeSpec{spec},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(respText)},
		}, nil
	}
}

// resolveToolCallProvider mirrors decide.next's resolveNextProvider —
// attrs.model + factory takes precedence, else cfg.Provider.
func resolveToolCallProvider(cfg ToolCallConfig, in map[string]any) (llm.Provider, string) {
	if cfg.ProviderFactory != nil {
		if m, _ := in["model"].(string); m != "" {
			if p, err := cfg.ProviderFactory.Get(m); err == nil && p != nil {
				return p, "factory:" + m
			}
		}
	}
	return cfg.Provider, "default"
}

// formatActToolsCatalog renders the act.* tools the specialist may
// choose from. When in["tools"] is a non-empty []string subset, only
// those are listed — lets the caller scope the choice. Empty/missing
// → every registered exposable act.* op.
func formatActToolsCatalog(reg *dag.Registry, in map[string]any) string {
	if reg == nil {
		return "  (no tools registered)"
	}
	allowed := map[string]bool{}
	switch v := in["tools"].(type) {
	case []string:
		for _, s := range v {
			allowed[s] = true
		}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				allowed[s] = true
			}
		}
	}

	var b strings.Builder
	for _, s := range reg.Exposable() {
		if s.Function != dag.FuncAct {
			continue
		}
		qname := s.QualifiedName()
		if len(allowed) > 0 && !allowed[qname] {
			continue
		}
		params := "args"
		if len(s.Inputs) > 0 {
			names := make([]string, 0, len(s.Inputs))
			for _, p := range s.Inputs {
				names = append(names, p.Name)
			}
			params = strings.Join(names, ", ")
		}
		fmt.Fprintf(&b, "  %s(%s) - %s\n", qname, params, truncateOneLine(s.Description, 80))
	}
	if b.Len() == 0 {
		return "  (no exposable act.* tools registered)"
	}
	return b.String()
}

// shouldChunkOnOversize reports whether oversized output from this
// tool should be split into deterministic line-based chunks rather
// than passed through attend.compress. True for tools whose value is
// the raw bytes (read_file, run_shell — calling model can act on the
// content directly with location headers); false for tools where the
// caller wants LLM-mediated salience (e.g. cortex_search returning
// many results worth ranking).
func shouldChunkOnOversize(toolName string) bool {
	switch toolName {
	case "act.read_file", "act.run_shell":
		return true
	default:
		return false
	}
}

// materializeActSpawn validates the specialist-emitted tool_name +
// args against the registry and converts them into a dag.NodeSpec
// ready for NodeResult.Spawn. Returns (zero, false) when the tool
// name isn't a registered exposable act.* op.
//
// Args are JSON-marshalled into the `args` Attr (string) because
// AdaptToolAsAct's input schema expects raw JSON the underlying
// harness.ToolHandler parses itself. confirm=true is auto-supplied
// (axis-5 opt-in — the user running cortex consented to agent-driven
// shell calls).
func materializeActSpawn(toolName string, args map[string]any, reg *dag.Registry) (dag.NodeSpec, bool) {
	qname := strings.TrimSpace(toolName)
	parts := strings.SplitN(qname, ".", 2)
	if len(parts) != 2 || parts[0] != "act" {
		return dag.NodeSpec{}, false
	}
	if reg != nil {
		spec, err := reg.Get(qname)
		if err != nil || !spec.Exposable {
			return dag.NodeSpec{}, false
		}
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return dag.NodeSpec{}, false
	}

	return dag.NodeSpec{
		Function: dag.FuncAct,
		Op:       parts[1],
		Attrs: map[string]any{
			"args":    string(argsJSON),
			"confirm": true,
		},
	}, true
}

// parseToolCallResponse extracts JSON from the specialist's raw
// response. Tolerates leading/trailing whitespace and optional
// markdown fences (some models can't help themselves).
func parseToolCallResponse(raw string, out *toolCallResponse) error {
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
