package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeProvider returns canned responses for tests. The optional
// `respond` closure is invoked per-call with prompt + system so tests
// can drive different scenarios off a single mock instance.
type fakeProvider struct {
	respond func(prompt, system string) (string, error)
}

func (f *fakeProvider) Generate(ctx context.Context, prompt string) (string, error) {
	return f.respond(prompt, "")
}
func (f *fakeProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return f.respond(prompt, system)
}
func (f *fakeProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	out, err := f.respond(prompt, "")
	return out, llm.GenerationStats{}, err
}
func (f *fakeProvider) Name() string      { return "fake" }
func (f *fakeProvider) IsAvailable() bool { return true }

func mustBudget() dag.Budget {
	return dag.Budget{LatencyMS: 30000, Tokens: 5000, Depth: 10}
}

// registryWithCodingTurn returns a registry that has decide.coding_turn
// and remember.vector_search registered with no-op handlers — enough
// for materializeEmittedNode validation to succeed.
func registryWithCodingTurn(t *testing.T) *dag.Registry {
	t.Helper()
	reg := dag.NewRegistry()
	for _, qn := range []struct{ fn, op string }{
		{"decide", "coding_turn"},
		{"remember", "vector_search"},
		{"decide", "next"},
	} {
		err := reg.Register(dag.NodeSpec{
			Function: dag.CortexFunction(qn.fn),
			Op:       qn.op,
			Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
				return dag.NodeResult{}, nil
			},
		})
		if err != nil {
			t.Fatalf("register %s.%s: %v", qn.fn, qn.op, err)
		}
	}
	return reg
}

// TestNext_NoProvider_Fallback — nil Provider → single coding_turn
// spawn so the chain keeps walking.
func TestNext_NoProvider_Fallback(t *testing.T) {
	h := NewNextHandler(NextConfig{})
	res, err := h(context.Background(), map[string]any{"prompt": "anything"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("expected fallback=true; got %v", res.Out["fallback"])
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("fallback should spawn one coding_turn; got %+v", res.Spawn)
	}
}

// TestNext_MissingPrompt_Errors — handler validates the required input.
func TestNext_MissingPrompt_Errors(t *testing.T) {
	h := NewNextHandler(NextConfig{})
	_, err := h(context.Background(), map[string]any{}, mustBudget())
	if err == nil {
		t.Errorf("expected error for missing prompt")
	}
}

// TestNext_LLM_SingleSpawn — provider returns a valid one-node plan;
// handler materializes it.
func TestNext_LLM_SingleSpawn(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"nodes":[{"op":"decide.coding_turn","attrs":{"prompt":"go"}}],"reasoning":"direct"}`, nil
		},
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{Provider: p, Registry: reg})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("expected one coding_turn spawn; got %+v", res.Spawn)
	}
	if res.Spawn[0].Attrs["prompt"] != "go" {
		t.Errorf("attrs.prompt not threaded; got %v", res.Spawn[0].Attrs)
	}
}

// TestNext_LLM_MultiSpawn_WithModel — multi-node plan with attrs.model
// threaded through to the resulting NodeSpec.
func TestNext_LLM_MultiSpawn_WithModel(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"nodes":[
				{"op":"remember.vector_search","attrs":{"query":"auth"}},
				{"op":"decide.coding_turn","attrs":{"prompt":"answer"},"model":"qwen2.5:14b"}
			],"reasoning":"search-then-act"}`, nil
		},
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{Provider: p, Registry: reg})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 2 {
		t.Fatalf("expected 2 spawns; got %d", len(res.Spawn))
	}
	if res.Spawn[0].Op != "vector_search" {
		t.Errorf("first spawn should be vector_search; got %s", res.Spawn[0].Op)
	}
	if res.Spawn[1].Op != "coding_turn" {
		t.Errorf("second spawn should be coding_turn; got %s", res.Spawn[1].Op)
	}
	if m, _ := res.Spawn[1].Attrs["model"].(string); m != "qwen2.5:14b" {
		t.Errorf("attrs.model not threaded; got %v", res.Spawn[1].Attrs)
	}
}

// TestNext_UnknownOp_Dropped — emitted op not in registry is dropped
// with a logged reasoning suffix; other valid ops still spawn.
func TestNext_UnknownOp_Dropped(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"nodes":[
				{"op":"made.up","attrs":{}},
				{"op":"decide.coding_turn","attrs":{"prompt":"go"}}
			]}`, nil
		},
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{Provider: p, Registry: reg})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("expected only the valid op spawned; got %+v", res.Spawn)
	}
	reasoning, _ := res.Out["reasoning"].(string)
	if !strings.Contains(reasoning, "made.up") || !strings.Contains(reasoning, "unknown-op") {
		t.Errorf("reasoning should note the dropped op; got %q", reasoning)
	}
}

// TestNext_FanoutCap — emitter overshoots MaxFanout; only the cap
// number of nodes are spawned; the rest are dropped.
func TestNext_FanoutCap(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"nodes":[
				{"op":"decide.coding_turn","attrs":{"prompt":"a"}},
				{"op":"decide.coding_turn","attrs":{"prompt":"b"}},
				{"op":"decide.coding_turn","attrs":{"prompt":"c"}},
				{"op":"decide.coding_turn","attrs":{"prompt":"d"}},
				{"op":"decide.coding_turn","attrs":{"prompt":"e"}}
			]}`, nil
		},
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{Provider: p, Registry: reg, MaxFanout: 2})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 2 {
		t.Fatalf("expected 2 spawns (cap=2); got %d", len(res.Spawn))
	}
	reasoning, _ := res.Out["reasoning"].(string)
	if !strings.Contains(reasoning, "fanout-cap") {
		t.Errorf("reasoning should note fanout-cap drops; got %q", reasoning)
	}
}

// TestNext_LLMError_Fallback — provider errors → fallback spawn,
// no handler error (chain keeps walking).
func TestNext_LLMError_Fallback(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "", errors.New("network down")
		},
	}
	h := NewNextHandler(NextConfig{Provider: p, Registry: registryWithCodingTurn(t)})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler should swallow provider err: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("expected fallback=true; got %+v", res.Out)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("fallback should spawn coding_turn; got %+v", res.Spawn)
	}
}

// TestNext_ParseError_Fallback — malformed JSON → fallback spawn.
func TestNext_ParseError_Fallback(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "not json at all", nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p, Registry: registryWithCodingTurn(t)})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("expected fallback=true; got %+v", res.Out)
	}
}

// TestNext_FencedJSON_Tolerated — model wraps JSON in ```json ... ```
// fences; parser strips them.
func TestNext_FencedJSON_Tolerated(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "```json\n{\"nodes\":[{\"op\":\"decide.coding_turn\",\"attrs\":{\"prompt\":\"x\"}}]}\n```", nil
		},
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{Provider: p, Registry: reg})
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("fenced JSON should still parse; got %+v", res.Spawn)
	}
}

// TestNext_BudgetExhausted_Fallback — when budget can't afford the
// cost hint, skip the LLM call and fall back without consulting it.
func TestNext_BudgetExhausted_Fallback(t *testing.T) {
	called := false
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			called = true
			return `{"nodes":[]}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	tinyBudget := dag.Budget{LatencyMS: 1, Tokens: 1, Depth: 10}
	res, err := h(context.Background(), map[string]any{"prompt": "x"}, tinyBudget)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called {
		t.Errorf("expected LLM skipped on insufficient budget")
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("expected fallback=true; got %+v", res.Out)
	}
}

// fakeFactory routes a fixed model id to a captured Provider so tests
// can confirm per-call routing fires. Any other id returns the
// fallback Provider with the matching tag.
type fakeFactory struct {
	byID    map[string]llm.Provider
	def     llm.Provider
	getErrs map[string]error
}

func (f *fakeFactory) Get(modelID string) (llm.Provider, error) {
	if err, ok := f.getErrs[modelID]; ok {
		return nil, err
	}
	if p, ok := f.byID[modelID]; ok {
		return p, nil
	}
	if modelID == "" {
		return f.def, nil
	}
	return nil, errors.New("unknown model: " + modelID)
}
func (f *fakeFactory) Default() llm.Provider { return f.def }

// TestNext_PerCallProviderRouting — attrs.model on decide.next routes
// the classifier call through ProviderFactory.Get(model) rather than
// cfg.Provider. The chosen provider's response shape determines what
// gets spawned, confirming the factory hit.
func TestNext_PerCallProviderRouting(t *testing.T) {
	defaultCalled, routedCalled := false, false
	defaultProvider := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			defaultCalled = true
			return `{"nodes":[{"op":"decide.coding_turn","attrs":{"prompt":"from-default"}}]}`, nil
		},
	}
	routedProvider := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			routedCalled = true
			return `{"nodes":[{"op":"decide.coding_turn","attrs":{"prompt":"from-routed"}}]}`, nil
		},
	}
	factory := &fakeFactory{
		byID: map[string]llm.Provider{"strong-classifier": routedProvider},
		def:  defaultProvider,
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{
		Provider:        defaultProvider,
		ProviderFactory: factory,
		Registry:        reg,
	})

	// Call WITHOUT attrs.model → default provider should fire.
	if _, err := h(context.Background(), map[string]any{"prompt": "x"}, mustBudget()); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !defaultCalled {
		t.Errorf("default provider not called on no-model call")
	}
	if routedCalled {
		t.Errorf("routed provider called when no model attr was set")
	}

	// Reset + call WITH attrs.model → routed provider should fire.
	defaultCalled, routedCalled = false, false
	res, err := h(context.Background(),
		map[string]any{"prompt": "x", "model": "strong-classifier"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !routedCalled {
		t.Errorf("routed provider not called when attrs.model=strong-classifier")
	}
	if defaultCalled {
		t.Errorf("default provider called despite attrs.model override")
	}
	// Sanity: the routed provider's response shape leaked through.
	if len(res.Spawn) != 1 || res.Spawn[0].Attrs["prompt"] != "from-routed" {
		t.Errorf("routed response not threaded through to spawn; got %+v", res.Spawn)
	}
}

// TestNext_PerCallProviderRouting_UnknownModelFallsBackToDefault —
// when the factory can't resolve the requested model id (typo,
// uninstalled), the handler falls back to cfg.Provider rather than
// dropping the call. Keeps the chain walking when the LLM hallucinates
// a model id that doesn't exist.
func TestNext_PerCallProviderRouting_UnknownModelFallsBackToDefault(t *testing.T) {
	defaultCalled := false
	defaultProvider := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			defaultCalled = true
			return `{"nodes":[{"op":"decide.coding_turn","attrs":{"prompt":"ok"}}]}`, nil
		},
	}
	factory := &fakeFactory{
		byID: map[string]llm.Provider{}, // empty: every id is unknown
		def:  defaultProvider,
	}
	reg := registryWithCodingTurn(t)
	h := NewNextHandler(NextConfig{
		Provider:        defaultProvider,
		ProviderFactory: factory,
		Registry:        reg,
	})
	_, err := h(context.Background(),
		map[string]any{"prompt": "x", "model": "made-up-id"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler should swallow factory miss: %v", err)
	}
	if !defaultCalled {
		t.Errorf("default provider should fire when factory can't resolve the requested id")
	}
}

// TestNext_RenderPromptSubstitution — confirm catalogs flow into the
// rendered system prompt (so the LLM actually sees them).
func TestNext_RenderPromptSubstitution(t *testing.T) {
	out := renderNextPrompt("hello", "  remember.vector_search(query) - search\n", "  qwen2.5:14b - local\n", "", "", dag.Budget{})
	if !strings.Contains(out, "hello") {
		t.Errorf("user prompt missing")
	}
	if !strings.Contains(out, "remember.vector_search") {
		t.Errorf("op catalog missing")
	}
	if !strings.Contains(out, "qwen2.5:14b") {
		t.Errorf("model catalog missing")
	}
	if strings.Contains(out, "{{OPS}}") || strings.Contains(out, "{{MODELS}}") || strings.Contains(out, "{{PROMPT}}") || strings.Contains(out, "{{CONTEXT}}") {
		t.Errorf("unsubstituted placeholders remain in output")
	}
	if strings.Contains(out, "{{BOUNDARIES}}") {
		t.Errorf("BOUNDARIES placeholder not substituted")
	}
	// With empty boundaries + zero budget, the boundaries block should
	// collapse to "" rather than emitting an awkward empty header.
	if strings.Contains(out, "Physical-system limits") {
		t.Errorf("boundaries block should be absent when boundaries+budget are empty")
	}
}

// TestNext_RenderPromptIncludesBoundariesAndBudget pins the Phase-3
// Slice 2 self-awareness wiring: a non-empty boundaries fact-sheet
// flows verbatim into the prompt AND the remaining budget surfaces as
// a line item the planner can read.
func TestNext_RenderPromptIncludesBoundariesAndBudget(t *testing.T) {
	boundaries := "- Model class: small (qwen2.5-coder:1.5b)\n- Per-tool-call salience cap: 200 tokens\n"
	budget := dag.Budget{LatencyMS: 120000, Tokens: 8000, Depth: 8, OutputTokens: 6000}
	out := renderNextPrompt("explore", "  decide.coding_turn(prompt) - run agent loop\n", "  default\n", "", boundaries, budget)

	for _, want := range []string{
		"Physical-system limits",
		"Model class: small",
		"salience cap: 200",
		"latency=120000ms",
		"output_tokens=6000",
		"favor fan-out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("boundaries block missing %q in:\n%s", want, out)
		}
	}
}

// TestNext_RenderPromptOmitsBoundariesWhenAllZero — pre-Phase-3
// callers passing empty boundaries + zero budget must not see the
// fact-sheet block (back-compat: a quiet-config call still renders the
// same prompt shape it always did).
func TestNext_RenderPromptOmitsBoundariesWhenAllZero(t *testing.T) {
	out := renderNextPrompt("hi", "", "", "", "", dag.Budget{})
	if strings.Contains(out, "Physical-system limits") {
		t.Errorf("boundaries block should be absent for zero-config render; got:\n%s", out)
	}
	if strings.Contains(out, "Planning guidance") {
		t.Errorf("planning guidance should not appear without boundaries")
	}
}
