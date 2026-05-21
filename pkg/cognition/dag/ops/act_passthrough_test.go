package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestPassthrough_helloVariants(t *testing.T) {
	h := NewPassthroughHandler(PassthroughConfig{})
	for _, p := range []string{"hi", "hello", "Hi", "  hey ", "HEY"} {
		got, err := h(context.Background(), map[string]any{"prompt": p}, dag.DefaultTurnBudget())
		if err != nil {
			t.Fatalf("prompt=%q: %v", p, err)
		}
		resp, _ := got.Out["response"].(string)
		if !strings.Contains(strings.ToLower(resp), "hi") {
			t.Errorf("prompt=%q got response=%q, expected greeting reply", p, resp)
		}
	}
}

func TestPassthrough_thanks(t *testing.T) {
	h := NewPassthroughHandler(PassthroughConfig{})
	for _, p := range []string{"thanks", "thank you", "TY"} {
		got, _ := h(context.Background(), map[string]any{"prompt": p}, dag.DefaultTurnBudget())
		if resp, _ := got.Out["response"].(string); resp != "Anytime." {
			t.Errorf("prompt=%q: expected \"Anytime.\", got %q", p, resp)
		}
	}
}

func TestPassthrough_genericFallback(t *testing.T) {
	h := NewPassthroughHandler(PassthroughConfig{})
	got, _ := h(context.Background(), map[string]any{"prompt": "something else entirely"}, dag.DefaultTurnBudget())
	resp, _ := got.Out["response"].(string)
	if resp == "" {
		t.Error("expected a non-empty fallback reply")
	}
}

func TestPassthrough_callsOnResponse(t *testing.T) {
	var captured string
	h := NewPassthroughHandler(PassthroughConfig{
		OnResponse: func(r string) { captured = r },
	})
	got, _ := h(context.Background(), map[string]any{"prompt": "thanks"}, dag.DefaultTurnBudget())
	resp, _ := got.Out["response"].(string)
	if captured != resp {
		t.Errorf("OnResponse captured %q, Out had %q — must match", captured, resp)
	}
	if captured == "" {
		t.Error("expected OnResponse to be invoked with a non-empty reply")
	}
}

func TestPassthrough_zeroTokenCost(t *testing.T) {
	h := NewPassthroughHandler(PassthroughConfig{})
	got, _ := h(context.Background(), map[string]any{"prompt": "hi"}, dag.DefaultTurnBudget())
	if got.CostConsumed.Tokens != 0 {
		t.Errorf("expected zero token cost (mechanical op), got %d", got.CostConsumed.Tokens)
	}
}

func TestPassthrough_missingPrompt(t *testing.T) {
	h := NewPassthroughHandler(PassthroughConfig{})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Error("expected error when prompt is missing")
	}
}

func TestPassthroughSpec_axisContract(t *testing.T) {
	spec := PassthroughSpec(PassthroughConfig{})
	if spec.AxisContract == nil {
		t.Fatal("act-typed spec must have AxisContract")
	}
	if spec.AxisContract.Mutator {
		t.Error("passthrough must be Mutator=false")
	}
	if spec.AxisContract.RequiresConfirmation {
		t.Error("passthrough must be RequiresConfirmation=false")
	}
}

func TestPassthroughSpec_qualifiedName(t *testing.T) {
	spec := PassthroughSpec(PassthroughConfig{})
	if spec.QualifiedName() != "act.passthrough" {
		t.Errorf("qualified name = %q, want act.passthrough", spec.QualifiedName())
	}
}

func TestPassthrough_fitsInGreetingBudget(t *testing.T) {
	// Defense-in-depth integration check: act.passthrough's cost hint
	// MUST fit under BudgetForIntent("greeting"). If someone bumps the
	// cost without adjusting the budget, the greeting path silently
	// degrades to the fallback chain — this test catches that.
	spec := PassthroughSpec(PassthroughConfig{})
	if !dag.BudgetForIntent("greeting").CanAfford(spec.Cost) {
		t.Errorf("act.passthrough Cost=%+v cannot fit under BudgetForIntent(\"greeting\")=%+v",
			spec.Cost, dag.BudgetForIntent("greeting"))
	}
}
