package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// registryWithAct registers a fake act.read_file + act.list_dir so
// the tool_call materializer's registry check can validate names.
// The handlers are no-ops; the test only cares about Exposable spec
// presence.
func registryWithAct(t *testing.T) *dag.Registry {
	t.Helper()
	reg := dag.NewRegistry()
	for _, name := range []string{"read_file", "list_dir"} {
		err := reg.Register(dag.NodeSpec{
			Function:     dag.FuncAct,
			Op:           name,
			Description:  "test " + name,
			Inputs:       []dag.ParamSpec{{Name: "args", Type: "string", Required: true}},
			AxisContract: &dag.AxisContract{Mutator: false},
			Exposable:    true,
			Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
				return dag.NodeResult{}, nil
			},
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return reg
}

// TestToolCall_HappyPath — specialist returns a valid call; the
// handler emits an act.<tool> spawn with structured args marshalled
// into the `args` Attr (matching AdaptToolAsAct's input schema).
func TestToolCall_HappyPath(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"tool_name":"act.read_file","args":{"path":"README.md"},"reasoning":"read the readme"}`, nil
		},
	}
	reg := registryWithAct(t)
	h := NewToolCallHandler(ToolCallConfig{Provider: p, Registry: reg})

	res, err := h(context.Background(), map[string]any{"intent": "read the project readme"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got, _ := res.Out["tool_name"].(string); got != "act.read_file" {
		t.Errorf("tool_name out: got %v", res.Out["tool_name"])
	}
	if len(res.Spawn) != 1 {
		t.Fatalf("spawn len = %d, want 1", len(res.Spawn))
	}
	spawned := res.Spawn[0]
	if spawned.Function != dag.FuncAct || spawned.Op != "read_file" {
		t.Errorf("spawned qname: got %s.%s, want act.read_file", spawned.Function, spawned.Op)
	}
	if args, _ := spawned.Attrs["args"].(string); args != `{"path":"README.md"}` {
		t.Errorf("spawned args attr: got %q, want JSON-marshalled struct", args)
	}
	if confirm, _ := spawned.Attrs["confirm"].(bool); !confirm {
		t.Errorf("spawned confirm attr: got %v, want true (axis-5 auto-opt-in)", spawned.Attrs["confirm"])
	}
}

// TestToolCall_NoProvider — without a provider, the handler emits an
// empty spawn (chain keeps walking).
func TestToolCall_NoProvider(t *testing.T) {
	h := NewToolCallHandler(ToolCallConfig{Registry: registryWithAct(t)})
	res, err := h(context.Background(), map[string]any{"intent": "do something"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 0 {
		t.Errorf("expected empty spawn when no provider; got %d", len(res.Spawn))
	}
	if fb, _ := res.Out["fallback"].(bool); !fb {
		t.Errorf("expected fallback=true; got %v", res.Out)
	}
}

// TestToolCall_MissingIntent — required input check.
func TestToolCall_MissingIntent(t *testing.T) {
	h := NewToolCallHandler(ToolCallConfig{Registry: registryWithAct(t)})
	if _, err := h(context.Background(), map[string]any{}, mustBudget()); err == nil {
		t.Errorf("expected error for missing intent")
	}
}

// TestToolCall_UnknownTool — specialist picks a tool name not in
// the registry; handler drops the spawn with reasoning.
func TestToolCall_UnknownTool(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"tool_name":"act.made_up","args":{},"reasoning":"hallucinated"}`, nil
		},
	}
	h := NewToolCallHandler(ToolCallConfig{Provider: p, Registry: registryWithAct(t)})
	res, err := h(context.Background(), map[string]any{"intent": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 0 {
		t.Errorf("unknown tool should drop spawn; got %d", len(res.Spawn))
	}
	if fb, _ := res.Out["fallback"].(bool); !fb {
		t.Errorf("expected fallback=true; got %v", res.Out)
	}
}

// TestToolCall_NonActPrefix — even if the qualified name parses, the
// materializer refuses anything not in the act.* namespace.
func TestToolCall_NonActPrefix(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"tool_name":"decide.coding_turn","args":{"prompt":"hi"},"reasoning":"wrong fn"}`, nil
		},
	}
	h := NewToolCallHandler(ToolCallConfig{Provider: p, Registry: registryWithAct(t)})
	res, err := h(context.Background(), map[string]any{"intent": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 0 {
		t.Errorf("non-act.* tool_name should drop spawn; got %d", len(res.Spawn))
	}
}

// TestToolCall_ProviderError — specialist call fails; handler emits
// empty spawn with reasoning, no handler-level error.
func TestToolCall_ProviderError(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "", errors.New("network down")
		},
	}
	h := NewToolCallHandler(ToolCallConfig{Provider: p, Registry: registryWithAct(t)})
	res, err := h(context.Background(), map[string]any{"intent": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler should swallow provider err: %v", err)
	}
	if len(res.Spawn) != 0 {
		t.Errorf("provider error should drop spawn; got %d", len(res.Spawn))
	}
	if fb, _ := res.Out["fallback"].(bool); !fb {
		t.Errorf("expected fallback=true; got %v", res.Out)
	}
}

// TestToolCall_ParseError — malformed JSON response; handler drops
// the call without crashing.
func TestToolCall_ParseError(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "i forgot to emit json", nil
		},
	}
	h := NewToolCallHandler(ToolCallConfig{Provider: p, Registry: registryWithAct(t)})
	res, err := h(context.Background(), map[string]any{"intent": "x"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Spawn) != 0 {
		t.Errorf("parse error should drop spawn; got %d", len(res.Spawn))
	}
}

// TestToolCall_PerCallProviderRouting — attrs.model + ProviderFactory
// routes the specialist call through factory.Get(model).
func TestToolCall_PerCallProviderRouting(t *testing.T) {
	defaultCalled, routedCalled := false, false
	defaultP := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			defaultCalled = true
			return `{"tool_name":"act.read_file","args":{"path":"a"}}`, nil
		},
	}
	routedP := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			routedCalled = true
			return `{"tool_name":"act.read_file","args":{"path":"b"}}`, nil
		},
	}
	factory := &fakeFactory{
		byID: map[string]llm.Provider{"xlam-1.5b": routedP},
		def:  defaultP,
	}
	reg := registryWithAct(t)
	h := NewToolCallHandler(ToolCallConfig{Provider: defaultP, ProviderFactory: factory, Registry: reg})

	// Without attrs.model → default.
	if _, err := h(context.Background(), map[string]any{"intent": "x"}, mustBudget()); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !defaultCalled {
		t.Errorf("default provider should fire when no model attr")
	}
	if routedCalled {
		t.Errorf("routed provider should NOT fire without model attr")
	}

	// With attrs.model → routed.
	defaultCalled, routedCalled = false, false
	if _, err := h(context.Background(), map[string]any{"intent": "x", "model": "xlam-1.5b"}, mustBudget()); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !routedCalled {
		t.Errorf("routed provider should fire with attrs.model")
	}
}

// TestToolCall_ToolsSubsetScopesChoice — caller passes a `tools`
// subset; only those qualified names appear in the prompt context.
// (Indirect — we exercise via formatActToolsCatalog directly since
// the prompt rendering is internal.)
func TestToolCall_ToolsSubsetScopesChoice(t *testing.T) {
	reg := registryWithAct(t)
	full := formatActToolsCatalog(reg, map[string]any{})
	if !contains(full, "act.read_file") || !contains(full, "act.list_dir") {
		t.Fatalf("full catalog should list both; got %q", full)
	}
	scoped := formatActToolsCatalog(reg, map[string]any{"tools": []string{"act.read_file"}})
	if !contains(scoped, "act.read_file") || contains(scoped, "act.list_dir") {
		t.Errorf("scoped catalog should only list act.read_file; got %q", scoped)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
