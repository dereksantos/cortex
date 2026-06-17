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
	if n != 17 {
		t.Errorf("expected 17 registered (10 Stage-2 ops + sense.prompt + maintain.capture + value.detect_unfamiliarity + remember.fetch_external + attend.compress + attend.distill + decide.route_message), got %d", n)
	}

	expectedOps := []string{
		"sense.prompt",
		"represent.embed",
		"remember.vector_search",
		"attend.rerank",
		"attend.compress",
		"attend.distill",
		"value.score",
		"value.detect_contradiction",
		"decide.inject",
		"decide.should_capture",
		"decide.route_message",
		"decide.plan",
		"model.predict_next",
		"maintain.extract_insight",
		"maintain.capture",
		"value.detect_unfamiliarity",
		"remember.fetch_external",
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
