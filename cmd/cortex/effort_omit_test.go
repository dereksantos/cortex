// effort_omit_test.go — issue #132's "thinking": "omit" escape hatch at the
// cmd/cortex resolution layer: the explicit send-nothing state the operator
// pins for a provider or model that rejects the reasoning parameter (an
// endpoint with parameter checks 404s on it). The wire side (Translate,
// unmarshal, marshal) is covered in pkg/llm (effort_omit_test.go); this file
// pins the two places the omit decision is honored: degradeForThinkingMode
// must not second-guess it, and resolveBinding must keep it against the
// fleet's catalog — the fleet can't override a decision to send nothing.
package main

import (
	"encoding/json"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestConfigModelSpecOmitUnmarshal pins the config surface: a hand-edited
// config's "thinking": "omit" parses to EffortOmit (IsZero() false — it is a
// decided value, so the config override wins over the role default).
func TestConfigModelSpecOmitUnmarshal(t *testing.T) {
	var spec ModelSpec
	if err := json.Unmarshal([]byte(`{"thinking": "omit"}`), &spec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if spec.Thinking != (llm.Effort{Level: llm.EffortOmit}) {
		t.Errorf("Thinking = %+v, want EffortOmit", spec.Thinking)
	}
	if spec.Thinking.IsZero() {
		t.Errorf("Thinking.IsZero() = true for omit, want false (a decided value)")
	}
}

// TestDegradeForThinkingModeOmitPassesThrough pins the catalog's
// non-interference: EffortOmit is the explicit send-nothing state, so the
// fleet's thinking_mode — which degrades asks the model can't honor — must
// pass it through in EVERY mode (none, hybrid, always, levels), unlike any
// other ask (which "none" drops, "hybrid" flattens, "always" flips).
func TestDegradeForThinkingModeOmitPassesThrough(t *testing.T) {
	omit := llm.Effort{Level: llm.EffortOmit}
	for _, mode := range []string{"none", "hybrid", "always", "levels"} {
		t.Run(mode, func(t *testing.T) {
			if got := degradeForThinkingMode(omit, mode); got != omit {
				t.Errorf("degradeForThinkingMode(omit, %q) = %+v, want omit unchanged", mode, got)
			}
		})
	}
}

// TestResolveBindingOmitSurvivesFleet pins the escape hatch end to end: a
// config "thinking": "omit" survives applyFleet's thinking_mode degradation
// — against a fleet that says the model can't reason at all (none) and one
// that says it's a hybrid toggle — and the re-stamp after applyFleet keeps
// the user's explicit pin. The resolved spec then translates to no wire
// fields (nothing sent), which is the whole point: a provider with
// parameter checks enabled 404s on the reasoning field, so the request must
// stay clean.
func TestResolveBindingOmitSurvivesFleet(t *testing.T) {
	omit := llm.Effort{Level: llm.EffortOmit}
	for _, tc := range []struct {
		name  string
		fleet Fleet
	}{
		{"empty fleet (no catalog)", nil},
		{"hybrid-mode fleet entry", Fleet{"m": {Thinking: true}}},
		{"none-mode fleet entry", Fleet{"m": {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Backend: Backend{Type: "litellm"},
				Models:  map[string]ModelSpec{roleCode: {Model: "m", Thinking: omit}},
			}
			spec := cfg.resolveBinding(roleCode, tc.fleet)
			if spec.Thinking != omit {
				t.Errorf("resolveBinding(code).Thinking = %+v, want omit (the fleet must not override it)", spec.Thinking)
			}
			if spec.Thinking.IsZero() {
				t.Errorf("Thinking.IsZero() = true, want false (omit is decided, not unset)")
			}
			// Nothing on the wire: both dialects translate omit to no field.
			if kw := spec.TemplateKwargs(); kw != nil {
				t.Errorf("TemplateKwargs() = %v, want nil (no reasoning field sent)", kw)
			}
			if r := spec.Reasoning(); r != nil {
				t.Errorf("Reasoning() = %+v, want nil (no reasoning field sent)", r)
			}
		})
	}
}
