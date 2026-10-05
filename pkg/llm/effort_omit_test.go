// effort_omit_test.go — issue #132's "thinking": "omit" wire vocabulary:
// the explicit send-nothing state (EffortOmit) round-trips through the JSON
// config shape and translates to NOTHING on the wire for both dialects —
// neither chat_template_kwargs nor the OpenRouter reasoning body — unlike
// EffortUnset, which a role-policy default may still fill in (see
// Effort.IsZero in effort.go).
package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEffortUnmarshalOmit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Effort
	}{
		{"omit", `"omit"`, Effort{Level: EffortOmit}},
		{"bool true maps to on", `true`, Effort{Level: EffortOn}},
		{"bool false maps to off", `false`, Effort{Level: EffortOff}},
		{"level off", `"off"`, Effort{Level: EffortOff}},
		{"level on", `"on"`, Effort{Level: EffortOn}},
		{"level low", `"low"`, Effort{Level: EffortLow}},
		{"level medium", `"medium"`, Effort{Level: EffortMedium}},
		{"level high", `"high"`, Effort{Level: EffortHigh}},
		{"budget object", `{"budget": 8192}`, Effort{Budget: 8192}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e Effort
			if err := json.Unmarshal([]byte(c.in), &e); err != nil {
				t.Fatalf("UnmarshalJSON(%s) error: %v", c.in, err)
			}
			if e != c.want {
				t.Errorf("UnmarshalJSON(%s) = %+v, want %+v", c.in, e, c.want)
			}
		})
	}

	t.Run("invalid level is rejected", func(t *testing.T) {
		var e Effort
		if err := json.Unmarshal([]byte(`"verbose"`), &e); err == nil {
			t.Errorf("UnmarshalJSON(\"verbose\") should fail, got %v", e)
		}
	})
}

// TestEffortMarshalOmit locks the round trip: MarshalJSON's canonical form for
// omit is the level string, and UnmarshalJSON accepts that string back.
func TestEffortMarshalOmit(t *testing.T) {
	out, err := Effort{Level: EffortOmit}.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(out) != `"omit"` {
		t.Errorf("MarshalJSON(omit) = %s, want \"omit\"", out)
	}
	var e Effort
	if err := json.Unmarshal(out, &e); err != nil {
		t.Fatalf("round-trip UnmarshalJSON: %v", err)
	}
	if e != (Effort{Level: EffortOmit}) {
		t.Errorf("round-trip = %+v, want omit", e)
	}
	// Unset marshals to null (the documented canonical form for "never set").
	unset, err := Effort{}.MarshalJSON()
	if err != nil || string(unset) != "null" {
		t.Errorf("MarshalJSON(unset) = %s (err %v), want null", unset, err)
	}
}

// TestEffortIsZero distinguishes the two send-nothing states: Unset is the
// absent state a role default may fill in; Omit is a decided state that must
// survive resolution.
func TestEffortIsZero(t *testing.T) {
	if !(Effort{}).IsZero() {
		t.Error("Effort{} should be zero")
	}
	if (Effort{Level: EffortOmit}).IsZero() {
		t.Error("Effort{omit} must NOT be zero: it is an explicit decision to send nothing")
	}
	if (Effort{Level: EffortOn}).IsZero() {
		t.Error("Effort{on} should not be zero")
	}
}

// TestTranslateOmit is the #132 fix itself: an explicit omit sends NOTHING on
// the wire for both dialects — a provider with parameter checks enabled 404s
// on the reasoning field, so "send nothing" must be reachable from config.
func TestTranslateOmit(t *testing.T) {
	for name, d := range map[string]Dialect{
		"template_kwargs": DialectTemplateKwargs,
		"openrouter":      DialectOpenRouter,
	} {
		t.Run(name, func(t *testing.T) {
			kwargs, reasoning := Translate(d, Effort{Level: EffortOmit})
			if kwargs != nil || reasoning != nil {
				t.Errorf("Translate(%s, omit) = (%v, %+v), want (nil, nil)", name, kwargs, reasoning)
			}
		})
	}

	// The contrast that makes omit meaningful: on every OTHER dialect path the
	// reasoning field IS sent (or enable_thinking is set) — omit is the only
	// explicit level that sends nothing.
	t.Run("on still sends something for contrast", func(t *testing.T) {
		if kwargs, _ := Translate(DialectTemplateKwargs, Effort{Level: EffortOn}); !reflect.DeepEqual(kwargs, map[string]any{"enable_thinking": true}) {
			t.Errorf("Translate(kwargs, on) = %v, want enable_thinking true", kwargs)
		}
		if _, reasoning := Translate(DialectOpenRouter, Effort{Level: EffortOn}); reasoning == nil || reasoning.Enabled == nil || !*reasoning.Enabled {
			t.Errorf("Translate(openrouter, on) = %+v, want enabled true", reasoning)
		}
	})
}
