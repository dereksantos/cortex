package commands

import (
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
)

func TestSeedForIntent_greetingAboveThresholdRoutesToPassthrough(t *testing.T) {
	seed := seedForIntent("greeting", 0.9, "hello")
	if len(seed) != 1 {
		t.Fatalf("expected 1 seed node, got %d", len(seed))
	}
	if seed[0].QualifiedName() != "act.passthrough" {
		t.Errorf("greeting (high conf) must route to act.passthrough, got %q", seed[0].QualifiedName())
	}
	if p, _ := seed[0].Attrs["prompt"].(string); p != "hello" {
		t.Errorf("seed must carry the original prompt, got %q", p)
	}
}

func TestSeedForIntent_greetingBelowThresholdFallsThrough(t *testing.T) {
	// Below the confidence threshold the trivial path is unsafe — a
	// canned "Hi" to someone who actually wanted help is worse than
	// paying the coding-turn cost.
	seed := seedForIntent("greeting", 0.5, "hello")
	if seed[0].QualifiedName() != "sense.prompt" {
		t.Errorf("low-confidence greeting must fall through to sense.prompt, got %q", seed[0].QualifiedName())
	}
}

func TestSeedForIntent_nonGreetingIntentsAlwaysFallThrough(t *testing.T) {
	for _, intent := range []string{"code", "review", "meta", "recall", "clarify", "unknown"} {
		t.Run(intent, func(t *testing.T) {
			seed := seedForIntent(intent, 0.95, "do the thing")
			if seed[0].QualifiedName() != "sense.prompt" {
				t.Errorf("intent=%q must seed sense.prompt (the trivial-intent short-circuit is greeting-only today), got %q",
					intent, seed[0].QualifiedName())
			}
		})
	}
}

func TestClassifyIntentForTurn_missingRegistrationFallsBackToCode(t *testing.T) {
	// A registry without sense.classify_intent must yield the safe
	// default — never block the turn on a missing op registration.
	reg := dag.NewRegistry()
	intent, conf := classifyIntentForTurn(reg, "hello")
	if intent != ops.IntentCode {
		t.Errorf("expected fallback intent=%q, got %q", ops.IntentCode, intent)
	}
	if conf != 0 {
		t.Errorf("expected fallback confidence=0, got %v", conf)
	}
}

func TestClassifyIntentForTurn_registeredHandlerReturnsResult(t *testing.T) {
	// Register classify_intent with a nil provider — the handler's
	// internal fallback returns intent=code,confidence=0. This proves
	// the registry → spec → handler invocation path works without
	// requiring a real provider in unit tests.
	reg := dag.NewRegistry()
	if err := reg.Register(ops.ClassifyIntentSpec(ops.ClassifyIntentConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	intent, conf := classifyIntentForTurn(reg, "hello")
	if intent != ops.IntentCode {
		t.Errorf("nil-provider fallback should yield intent=%q, got %q", ops.IntentCode, intent)
	}
	if conf != 0 {
		t.Errorf("nil-provider fallback should yield confidence=0, got %v", conf)
	}
}
