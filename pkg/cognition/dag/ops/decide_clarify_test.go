package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestClarify_modelProducesQuestion(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"question":"Which file should the new endpoint live in?","why":"target file unspecified"}`,
		available: true,
	}
	var captured string
	h := NewClarifyHandler(ClarifyConfig{
		Provider:   p,
		OnResponse: func(r string) { captured = r },
	})
	got, err := h(context.Background(), map[string]any{"prompt": "add an endpoint"}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	q, _ := got.Out["question"].(string)
	if q != "Which file should the new endpoint live in?" {
		t.Errorf("got question=%q", q)
	}
	if !strings.HasSuffix(q, "?") {
		t.Error("clarifying question should end with a question mark")
	}
	if captured != q {
		t.Errorf("OnResponse captured %q, expected %q", captured, q)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Error("fallback should be false on successful parse")
	}
}

func TestClarify_providerUnavailableUsesMechanicalFallback(t *testing.T) {
	var captured string
	h := NewClarifyHandler(ClarifyConfig{
		Provider:   nil,
		OnResponse: func(r string) { captured = r },
	})
	got, _ := h(context.Background(), map[string]any{"prompt": "do the thing"}, dag.DefaultTurnBudget())
	q, _ := got.Out["question"].(string)
	if q != mechanicalClarifyQuestion {
		t.Errorf("expected mechanical fallback question, got %q", q)
	}
	if captured != mechanicalClarifyQuestion {
		t.Error("OnResponse should fire with the mechanical fallback too")
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true with nil provider")
	}
}

func TestClarify_emptyQuestionFromModelFallsBack(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"question":"   ","why":"x"}`,
		available: true,
	}
	h := NewClarifyHandler(ClarifyConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "x"}, dag.DefaultTurnBudget())
	if q, _ := got.Out["question"].(string); q != mechanicalClarifyQuestion {
		t.Errorf("expected fallback when model returns whitespace, got %q", q)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true when model returns empty question")
	}
}

func TestClarify_parseErrorFallsBack(t *testing.T) {
	p := &scriptedProvider{response: "not json", available: true}
	h := NewClarifyHandler(ClarifyConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "x"}, dag.DefaultTurnBudget())
	if q, _ := got.Out["question"].(string); q != mechanicalClarifyQuestion {
		t.Errorf("expected mechanical fallback on parse error, got %q", q)
	}
}

func TestClarify_missingPrompt(t *testing.T) {
	p := &scriptedProvider{available: true}
	h := NewClarifyHandler(ClarifyConfig{Provider: p})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Error("expected error when prompt is missing")
	}
}

func TestClarifySpec(t *testing.T) {
	spec := ClarifySpec(ClarifyConfig{})
	if spec.QualifiedName() != "decide.clarify" {
		t.Errorf("qualified name = %q, want decide.clarify", spec.QualifiedName())
	}
	if !spec.Exposable {
		t.Error("clarify should be Exposable so decide.next can re-invoke it mid-chain")
	}
}

func TestClarify_fitsInClarifyBudget(t *testing.T) {
	// Defense-in-depth: the clarify op's cost must fit under
	// BudgetForIntent("clarify"). If someone bumps clarifyCostHint
	// without adjusting the intent budget, the clarify seed would
	// silently fail the spawn gate and degrade to the fallback chain
	// (which routes through coding_turn and DOES afford its own budget,
	// but defeats the whole point of intent-aware routing).
	spec := ClarifySpec(ClarifyConfig{})
	if !dag.BudgetForIntent("clarify").CanAfford(spec.Cost) {
		t.Errorf("decide.clarify Cost=%+v does not fit under BudgetForIntent(\"clarify\")=%+v",
			spec.Cost, dag.BudgetForIntent("clarify"))
	}
}
