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
	"regexp"
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
//
// Requires declares the capability preference chain the executor's
// Router uses to pick the per-node provider: a tool-calling specialist
// (xLAM / phi-3-mini-tools / hermes-tool / functionary) when one is
// available, falling back to any tool-calling-capable model. Per
// docs/per-node-routing-plan.md — this is the canonical specialist
// use case and the reliability fix at the heart of the plan.
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
		Requires:  []string{llm.CapToolCallingSpecialist, llm.CapToolCalling},
		Handler:   NewToolCallHandler(cfg),
	}
}

var toolCallCostHint = dag.Cost{LatencyMS: 2000, Tokens: 200}

const toolCallPrompt = `You are a tool-call specialist. The user has a natural-language intent — convert it into a single, structured tool invocation.

Intent:
"""
{{INTENT}}
"""

Available tools — each entry shows the qualified name, a description, and the JSON Schema for its arguments. Field names in your output's "args" object MUST come from the listed schema "properties"; do not invent field names.

{{TOOLS}}
Output exactly one JSON object, no markdown fences, no prose:

{"tool_name":"<qualified.op>","args":{<field>:<value>, ...},"reasoning":"<short sentence>"}

Verb-to-tool map (match the FIRST verb in the intent):
  list   → act.list_dir
  read   → act.read_file
  shell  → act.run_shell    (intent starting with "shell:" wraps a literal command)
  run    → act.run_shell    (intent describing a command to run)
  write  → act.write_file
  search/grep/find/look → act.run_shell (use grep/find as the command)

Concrete shape (substitute the intent's actual file/dir; never copy the placeholder verbatim, never invent OR/IF in the args):

  Intent "list ."                                   → {"tool_name":"act.list_dir","args":{"path":"."},"reasoning":"list root"}
  Intent "list pkg/cognition"                       → {"tool_name":"act.list_dir","args":{"path":"pkg/cognition"},"reasoning":"list subdir"}
  Intent "read README.md"                           → {"tool_name":"act.read_file","args":{"path":"README.md"},"reasoning":"read it"}
  Intent "read pkg/cognition/dag/executor.go lines 1-200"
                                                    → {"tool_name":"act.read_file","args":{"path":"pkg/cognition/dag/executor.go","start_line":1,"end_line":200},"reasoning":"slice"}
  Intent "shell: grep -rn 'package main' cmd"       → {"tool_name":"act.run_shell","args":{"command":"grep -rn 'package main' cmd"},"reasoning":"locate"}
  Intent "shell: find . -name '*.go' -path '*/dag/*'"
                                                    → {"tool_name":"act.run_shell","args":{"command":"find . -name '*.go' -path '*/dag/*'"},"reasoning":"enumerate"}
  Intent "write notes.md with summary content"      → {"tool_name":"act.write_file","args":{"path":"notes.md","content":"<the literal content>"},"reasoning":"create"}

Rules:
- tool_name must be one of the listed qualified names exactly.
- "args" keys must be schema field names from the chosen tool. No "input_param_name", no "entrypoint", no "input".
- Required schema fields must all be present and non-empty. Use "." for the workdir root.
- Paths are workdir-relative; no absolute paths, no ".." segments.
- Pick the single best tool; the intent says what to do — match the verb to the tool ("read"→read_file, "list/scan dir"→list_dir, "write/create file"→write_file, "run/execute shell"→run_shell).
- Pull concrete values straight from the intent text — if the intent names a filename, that's the path.`

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

		// Mechanical fast path: when the planner emits an intent in one
		// of the canonical short forms (decide.next teaches it to do
		// this), parse it directly and skip the specialist round-trip.
		// 1-2B specialist models are unreliable at verb-to-tool matching
		// for prose like "shell: grep ..." even with examples; matching
		// the prefix here turns those reliable into 100% reliable AND
		// shaves the call latency to ~0. The catalog and the specialist
		// stay as the fallback for genuinely ambiguous prose.
		if spec, ok := mechanicalToolCallFromIntent(intent, cfg.Registry); ok {
			spec = attachSalience(spec, cfg, intent, budget)
			return dag.NodeResult{
				Out: map[string]any{
					"tool_name":          spec.QualifiedName(),
					"tool_args":          decodeArgsAttr(spec),
					"reasoning":          "mechanical: matched canonical intent prefix",
					"specialist_skipped": true,
				},
				Spawn:        []dag.NodeSpec{spec},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		provider, providerSrc := resolveToolCallProvider(cfg, in, budget)
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

		spec = attachSalience(spec, cfg, intent, budget)

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

// resolveToolCallProvider picks the provider for this call, in order:
//
//  1. Budget.Provider — set by the Executor's Router when the new
//     per-node routing is wired (docs/per-node-routing-plan.md slice 3).
//     The Router already applied Attrs["model"] override + Requires
//     chain, so the handler doesn't re-check those when Budget.Provider
//     is present.
//  2. Legacy attrs.model + factory — the pre-routing per-call override
//     path. Preserved so handlers without a wired Router behave exactly
//     as before.
//  3. cfg.Provider — the handler's configured default.
func resolveToolCallProvider(cfg ToolCallConfig, in map[string]any, budget dag.Budget) (llm.Provider, string) {
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

// formatActToolsCatalog renders the act.* tools the specialist may
// choose from. When in["tools"] is a non-empty []string subset, only
// those are listed — lets the caller scope the choice. Empty/missing
// → every registered exposable act.* op.
//
// Each entry shows the qualified name, a short description, and the
// underlying tool's JSON Schema (when registered via AdaptToolAsAct).
// The schema is what lets specialist models (xLAM, hermes-pro) pick
// correct field names and types — without it they invent plausible-
// looking but wrong field names.
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
		fmt.Fprintf(&b, "- %s\n  description: %s\n", qname, truncateOneLine(s.Description, 240))
		if len(s.ToolSchemaJSON) > 0 {
			fmt.Fprintf(&b, "  args_schema: %s\n", compactSchemaJSON(s.ToolSchemaJSON))
		} else if len(s.Inputs) > 0 {
			names := make([]string, 0, len(s.Inputs))
			for _, p := range s.Inputs {
				names = append(names, p.Name)
			}
			fmt.Fprintf(&b, "  args: %s\n", strings.Join(names, ", "))
		}
	}
	if b.Len() == 0 {
		return "  (no exposable act.* tools registered)"
	}
	return b.String()
}

// compactSchemaJSON canonicalizes a JSON Schema into single-line form
// so the catalog stays scannable. On parse failure, returns the input
// verbatim — the LLM still sees something legible even if it isn't
// minimally compact.
func compactSchemaJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
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

// attachSalience attaches a SalienceContract to the act spawn so the
// executor's post-handler hook handles oversized act.* outputs before
// deposit into turn state. Strategy:
//
//   - act.read_file / act.run_shell → deterministic line-based chunking
//     (ChunkOnOversize=true). The calling model sees raw bytes with
//     "[chunk i/N, lines a-b]" headers; no LLM is in the read path.
//   - Other act.* tools → LLM-mediated attend.compress, sized by Intent.
//
// When cfg.ToolOutputSalienceCap is zero the contract is omitted (pre-
// salience-budgets behavior).
func attachSalience(spec dag.NodeSpec, cfg ToolCallConfig, intent string, budget dag.Budget) dag.NodeSpec {
	if cfg.ToolOutputSalienceCap <= 0 {
		return spec
	}
	spec.Salience = &dag.SalienceContract{
		MaxOutputTokens:  cfg.ToolOutputSalienceCap,
		Intent:           intent,
		ChunkOnOversize:  shouldChunkOnOversize(spec.QualifiedName()),
		MaxEmittedTokens: budget.EmittedTokensCap(),
	}
	return spec
}

// decodeArgsAttr returns the parsed JSON args from a materialized act
// spawn. Used to populate the "tool_args" Out field for tracing /
// observability when the mechanical fast path skipped the specialist.
// On any parse failure returns nil — the trace still records the spawn
// itself.
func decodeArgsAttr(spec dag.NodeSpec) map[string]any {
	raw, _ := spec.Attrs["args"].(string)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// Canonical-intent prefix matchers. decide.next is instructed to emit
// intents in these short forms so the fast path catches them. The
// regexps are anchored to the start of the (lowercased, trimmed) intent
// so prose elsewhere in the string doesn't matter.
var (
	intentShell     = regexp.MustCompile(`^(?:shell|sh|bash):\s+(.+)$`)
	intentRunCmd    = regexp.MustCompile(`^run\s+(.+)$`)
	intentListDir   = regexp.MustCompile(`^list\s+(\S+)\s*$`)
	intentReadLines = regexp.MustCompile(`^read\s+(\S+)\s+lines?\s+(\d+)\s*[-–]\s*(\d+)\s*$`)
	intentReadFile  = regexp.MustCompile(`^read\s+(\S+)\s*$`)
	intentWriteFile = regexp.MustCompile(`^write\s+(\S+)\s*:\s*(.+)$`)
)

// mechanicalToolCallFromIntent parses the intent into a materialized
// act spawn when it matches one of the canonical forms decide.next
// teaches. Returns (zero, false) on any non-match so the caller falls
// through to the specialist LLM path. The registry is consulted to
// guarantee the chosen tool exists; if it doesn't (custom op set, test
// fixture), the fast path silently declines.
func mechanicalToolCallFromIntent(intent string, reg *dag.Registry) (dag.NodeSpec, bool) {
	trimmed := strings.TrimSpace(intent)
	if trimmed == "" {
		return dag.NodeSpec{}, false
	}
	// Match in priority order. read-with-lines must come before plain
	// read so the longer pattern wins.
	if m := intentShell.FindStringSubmatch(trimmed); m != nil {
		return materializeActSpawn("act.run_shell", map[string]any{"command": strings.TrimSpace(m[1])}, reg)
	}
	if m := intentRunCmd.FindStringSubmatch(strings.ToLower(trimmed)); m != nil {
		// Use the ORIGINAL case for the command body — the lowercased
		// match is just for prefix detection. Re-extract from the trimmed
		// original.
		cmd := strings.TrimSpace(trimmed[len("run "):])
		return materializeActSpawn("act.run_shell", map[string]any{"command": cmd}, reg)
	}
	if m := intentReadLines.FindStringSubmatch(trimmed); m != nil {
		var s, e int
		fmt.Sscanf(m[2], "%d", &s)
		fmt.Sscanf(m[3], "%d", &e)
		return materializeActSpawn("act.read_file", map[string]any{
			"path":       m[1],
			"start_line": s,
			"end_line":   e,
		}, reg)
	}
	if m := intentReadFile.FindStringSubmatch(trimmed); m != nil {
		return materializeActSpawn("act.read_file", map[string]any{"path": m[1]}, reg)
	}
	if m := intentListDir.FindStringSubmatch(trimmed); m != nil {
		return materializeActSpawn("act.list_dir", map[string]any{"path": m[1]}, reg)
	}
	if m := intentWriteFile.FindStringSubmatch(trimmed); m != nil {
		return materializeActSpawn("act.write_file", map[string]any{
			"path":    m[1],
			"content": strings.TrimSpace(m[2]),
		}, reg)
	}
	return dag.NodeSpec{}, false
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
