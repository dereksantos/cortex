package ops

import (
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestRegisterDefaults_registersAllOps(t *testing.T) {
	reg := dag.NewRegistry()
	n, err := RegisterDefaults(reg, DefaultsConfig{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if n != 11 {
		t.Errorf("expected 11 registered (9 Stage-2 ops + sense.prompt + maintain.capture), got %d", n)
	}

	expectedOps := []string{
		"sense.prompt",
		"represent.embed",
		"remember.vector_search",
		"attend.rerank",
		"value.score",
		"value.detect_contradiction",
		"decide.inject",
		"decide.should_capture",
		"model.predict_next",
		"maintain.extract_insight",
		"maintain.capture",
	}
	for _, op := range expectedOps {
		if _, err := reg.Get(op); err != nil {
			t.Errorf("expected %s to be registered: %v", op, err)
		}
	}
}

func TestRegisterDefaults_acceptsNilDeps(t *testing.T) {
	// Nil deps must not crash — ops handle missing deps via fallback
	// at call time, not registration time.
	reg := dag.NewRegistry()
	if _, err := RegisterDefaults(reg, DefaultsConfig{
		Provider: nil,
		Embedder: nil,
		Storage:  nil,
	}); err != nil {
		t.Fatalf("nil deps should register cleanly: %v", err)
	}
}
