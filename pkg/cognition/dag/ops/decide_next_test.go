package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeProvider returns canned responses keyed by the system prompt
// substring (so we can drive different test scenarios off one mock).
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

// TestNext_RuleBasedFallback_Greeting — no provider, short greeting →
// converse arm (matches isShortConversational).
func TestNext_RuleBasedFallback_Greeting(t *testing.T) {
	h := NewNextHandler(NextConfig{})
	res, err := h(context.Background(), map[string]any{"prompt": "hi"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "converse" {
		t.Errorf("expected converse; got %v", got)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("converse should spawn one coding_turn; got %+v", res.Spawn)
	}
}

// TestNext_RuleBasedFallback_LongPrompt — no provider, long prompt →
// code arm (default).
func TestNext_RuleBasedFallback_LongPrompt(t *testing.T) {
	h := NewNextHandler(NextConfig{})
	res, err := h(context.Background(),
		map[string]any{"prompt": "add a method foo to the Bar struct that returns the elapsed time"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "code" {
		t.Errorf("expected code; got %v", got)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("code should spawn one coding_turn; got %+v", res.Spawn)
	}
}

// TestNext_RuleBasedFallback_Empty — empty prompt → done (no spawn).
// Empty prompt also produces an error (handler validates 'prompt'),
// so we test via direct ruleBasedNextAction.
func TestNext_RuleBasedFallback_Empty(t *testing.T) {
	if got := ruleBasedNextAction("", false); got != NextActionDone {
		t.Errorf("empty prompt: expected done; got %s", got)
	}
}

// TestNext_LLMArm_Code — provider returns code arm; spawn = coding_turn.
func TestNext_LLMArm_Code(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"next_action":"code","reasoning":"user asks for a code change"}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(), map[string]any{"prompt": "edit main.go"}, mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "code" {
		t.Errorf("expected code; got %v", got)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("code should spawn coding_turn; got %+v", res.Spawn)
	}
}

// TestNext_LLMArm_Search — provider returns search; spawn =
// vector_search → decide.next (with already_searched=true).
func TestNext_LLMArm_Search(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"next_action":"search","reasoning":"may exist already"}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(),
		map[string]any{"prompt": "how does the auth flow work?"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "search" {
		t.Errorf("expected search; got %v", got)
	}
	if len(res.Spawn) != 2 {
		t.Fatalf("search should spawn 2 (vector_search, decide.next); got %d", len(res.Spawn))
	}
	if res.Spawn[0].Op != "vector_search" {
		t.Errorf("first spawn should be vector_search; got %+v", res.Spawn[0])
	}
	if res.Spawn[1].Op != "next" {
		t.Errorf("second spawn should be decide.next; got %+v", res.Spawn[1])
	}
	if v, _ := res.Spawn[1].Attrs["already_searched"].(bool); !v {
		t.Errorf("follow-up decide.next must carry already_searched=true; attrs=%v", res.Spawn[1].Attrs)
	}
}

// TestNext_LLMArm_Converse — provider returns converse; spawn = coding_turn.
func TestNext_LLMArm_Converse(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"next_action":"converse","reasoning":"prose answer fits"}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(),
		map[string]any{"prompt": "explain what a context broker is"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "converse" {
		t.Errorf("expected converse; got %v", got)
	}
	if len(res.Spawn) != 1 || res.Spawn[0].Op != "coding_turn" {
		t.Errorf("converse should spawn coding_turn; got %+v", res.Spawn)
	}
}

// TestNext_LLMArm_Done — provider returns done; empty spawn.
func TestNext_LLMArm_Done(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"next_action":"done","reasoning":"already addressed"}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(),
		map[string]any{"prompt": "thanks!", "already_searched": true},
		mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "done" {
		t.Errorf("expected done; got %v", got)
	}
	if len(res.Spawn) != 0 {
		t.Errorf("done should spawn nothing; got %d spawns", len(res.Spawn))
	}
}

// TestNext_SuppressSearchWhenAlreadySearched — even if the model
// picks search, the handler coerces to code when already_searched=true
// (prevents infinite search loops).
func TestNext_SuppressSearchWhenAlreadySearched(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return `{"next_action":"search","reasoning":"loop me"}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(),
		map[string]any{"prompt": "fix the bug", "already_searched": true},
		mustBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["next_action"]; got != "code" {
		t.Errorf("expected code (search suppressed); got %v", got)
	}
}

// TestNext_LLMError_FallsBackToRules — provider errors out, handler
// returns rule-based action (no error returned from the handler).
func TestNext_LLMError_FallsBackToRules(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "", errors.New("network down")
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(),
		map[string]any{"prompt": "fix the bug"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler should swallow provider err and fall back; got %v", err)
	}
	if got := res.Out["next_action"]; got != "code" {
		t.Errorf("fallback expected code; got %v", got)
	}
}

// TestNext_ParseError_FallsBackToRules — malformed JSON response →
// rule-based action.
func TestNext_ParseError_FallsBackToRules(t *testing.T) {
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			return "not json at all", nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	res, err := h(context.Background(),
		map[string]any{"prompt": "fix the bug"},
		mustBudget())
	if err != nil {
		t.Fatalf("handler should swallow parse err and fall back; got %v", err)
	}
	if got := res.Out["next_action"]; got != "code" {
		t.Errorf("fallback expected code; got %v", got)
	}
}

// TestNext_BudgetExhausted_FallsBackToRules — when budget can't
// afford the cost hint, handler skips the LLM call.
func TestNext_BudgetExhausted_FallsBackToRules(t *testing.T) {
	called := false
	p := &fakeProvider{
		respond: func(prompt, system string) (string, error) {
			called = true
			return `{"next_action":"code"}`, nil
		},
	}
	h := NewNextHandler(NextConfig{Provider: p})
	tinyBudget := dag.Budget{LatencyMS: 1, Tokens: 1, Depth: 10}
	res, err := h(context.Background(), map[string]any{"prompt": "fix the bug"}, tinyBudget)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called {
		t.Errorf("expected LLM skipped on insufficient budget")
	}
	if got := res.Out["next_action"]; got != "code" {
		t.Errorf("fallback expected code; got %v", got)
	}
}
