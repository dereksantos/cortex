package llm

import (
	"encoding/json"
	"testing"
)

// TestEffortUnmarshalJSON covers the three JSON shapes docs/thinking-models.md
// §1 declares (bool, level string, {"budget": N}) plus the invalid cases.
func TestEffortUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Effort
		wantErr bool
	}{
		{"legacy false", `false`, Effort{Level: EffortOff}, false},
		{"legacy true", `true`, Effort{Level: EffortOn}, false},
		{"level off", `"off"`, Effort{Level: EffortOff}, false},
		{"level on", `"on"`, Effort{Level: EffortOn}, false},
		{"level low", `"low"`, Effort{Level: EffortLow}, false},
		{"level medium", `"medium"`, Effort{Level: EffortMedium}, false},
		{"level high", `"high"`, Effort{Level: EffortHigh}, false},
		{"budget object", `{"budget": 8192}`, Effort{Budget: 8192}, false},
		{"invalid level string", `"blazing"`, Effort{}, true},
		{"invalid shape", `[1,2,3]`, Effort{}, true},
		{"zero budget object", `{"budget": 0}`, Effort{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e Effort
			err := json.Unmarshal([]byte(tt.in), &e)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Unmarshal(%s) = %+v, want error", tt.in, e)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.in, err)
			}
			if e != tt.want {
				t.Errorf("Unmarshal(%s) = %+v, want %+v", tt.in, e, tt.want)
			}
		})
	}
}

// TestEffortUnmarshalJSONFieldAbsent proves the zero value (EffortUnset, no
// budget) is what a struct field decodes to when the JSON key is simply
// absent — the "not specified" state role defaults and config overrides key
// off of.
func TestEffortUnmarshalJSONFieldAbsent(t *testing.T) {
	var spec struct {
		Thinking Effort `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(`{}`), &spec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !spec.Thinking.IsZero() {
		t.Errorf("Thinking = %+v, want zero value (unset)", spec.Thinking)
	}
}

// TestEffortMarshalJSON round-trips through the canonical output form.
func TestEffortMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		e    Effort
		want string
	}{
		{"unset", Effort{}, "null"},
		{"off", Effort{Level: EffortOff}, `"off"`},
		{"on", Effort{Level: EffortOn}, `"on"`},
		{"high", Effort{Level: EffortHigh}, `"high"`},
		{"budget only", Effort{Budget: 2048}, `{"budget":2048}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.e)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("Marshal(%+v) = %s, want %s", tt.e, b, tt.want)
			}
		})
	}
}

// TestEffortBudgetTier covers the level→budget tiers docs/thinking-models.md
// "Open decisions" strawmans (low=1024, medium=4096, high=16384), and that an
// explicit Budget always wins over a level's tier.
func TestEffortBudgetTier(t *testing.T) {
	tests := []struct {
		name string
		e    Effort
		want int
	}{
		{"unset", Effort{}, 0},
		{"off", Effort{Level: EffortOff}, 0},
		{"on", Effort{Level: EffortOn}, 0},
		{"low", Effort{Level: EffortLow}, EffortBudgetLow},
		{"medium", Effort{Level: EffortMedium}, EffortBudgetMedium},
		{"high", Effort{Level: EffortHigh}, EffortBudgetHigh},
		{"explicit budget wins over level", Effort{Level: EffortHigh, Budget: 500}, 500},
		{"bare explicit budget", Effort{Budget: 777}, 777},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.e.BudgetTier(); got != tt.want {
				t.Errorf("BudgetTier() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestTranslateTemplateKwargs covers the chat_template_kwargs dialect
// (llama.cpp/LiteLLM): off and on are both said affirmatively (on-as-omission
// is indistinguishable from off on hosted defaults that resolve to no
// reasoning); levels and budgets — which this dialect has no representation
// for — degrade to the affirmative on. Only unset sends nothing.
func TestTranslateTemplateKwargs(t *testing.T) {
	on := map[string]any{"enable_thinking": true}
	tests := []struct {
		name string
		e    Effort
		want map[string]any
	}{
		{"unset: no kwarg", Effort{}, nil},
		{"off: enable_thinking=false", Effort{Level: EffortOff}, map[string]any{"enable_thinking": false}},
		{"on: enable_thinking=true", Effort{Level: EffortOn}, on},
		{"low degrades to affirmative on", Effort{Level: EffortLow}, on},
		{"medium degrades to affirmative on", Effort{Level: EffortMedium}, on},
		{"high degrades to affirmative on", Effort{Level: EffortHigh}, on},
		{"bare budget degrades to affirmative on", Effort{Budget: 4096}, on},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kwargs, reasoning := Translate(DialectTemplateKwargs, tt.e)
			if reasoning != nil {
				t.Errorf("reasoning = %+v, want nil (this dialect never sets it)", reasoning)
			}
			if (kwargs == nil) != (tt.want == nil) {
				t.Fatalf("kwargs = %v, want %v", kwargs, tt.want)
			}
			if kwargs != nil && kwargs["enable_thinking"] != tt.want["enable_thinking"] {
				t.Errorf("kwargs = %v, want %v", kwargs, tt.want)
			}
		})
	}
}

// TestTranslateOpenRouter covers the OpenRouter reasoning dialect: off →
// {enabled:false}, on → {enabled:true} (affirmative — hosted defaults often
// resolve to no reasoning), levels → {effort:"..."}, a bare budget →
// {max_tokens:N}, unset → no reasoning field at all (model default).
func TestTranslateOpenRouter(t *testing.T) {
	tests := []struct {
		name string
		e    Effort
		want *Reasoning
	}{
		{"unset: nil", Effort{}, nil},
		{"on: enabled true", Effort{Level: EffortOn}, &Reasoning{Enabled: &trueVal}},
		{"off: enabled false", Effort{Level: EffortOff}, &Reasoning{Enabled: &falseVal}},
		{"low: effort low", Effort{Level: EffortLow}, &Reasoning{Effort: "low"}},
		{"medium: effort medium", Effort{Level: EffortMedium}, &Reasoning{Effort: "medium"}},
		{"high: effort high", Effort{Level: EffortHigh}, &Reasoning{Effort: "high"}},
		{"bare budget: max_tokens", Effort{Budget: 8192}, &Reasoning{MaxTokens: 8192}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kwargs, reasoning := Translate(DialectOpenRouter, tt.e)
			if kwargs != nil {
				t.Errorf("kwargs = %v, want nil (this dialect never sets it)", kwargs)
			}
			if (reasoning == nil) != (tt.want == nil) {
				t.Fatalf("reasoning = %+v, want %+v", reasoning, tt.want)
			}
			if reasoning == nil {
				return
			}
			if reasoning.Effort != tt.want.Effort || reasoning.MaxTokens != tt.want.MaxTokens {
				t.Errorf("reasoning = %+v, want %+v", reasoning, tt.want)
			}
			gotEnabled := reasoning.Enabled != nil && *reasoning.Enabled
			wantEnabled := tt.want.Enabled != nil && *tt.want.Enabled
			if (reasoning.Enabled == nil) != (tt.want.Enabled == nil) || gotEnabled != wantEnabled {
				t.Errorf("reasoning.Enabled = %v, want %v", reasoning.Enabled, tt.want.Enabled)
			}
		})
	}
}

// TestTranslateExistingConfigByteForByte pins the P2 compatibility
// requirement: a config with "thinking": false must produce EXACTLY today's
// {"enable_thinking": false} kwargs on the chat_template_kwargs dialect —
// nothing more, nothing less.
func TestTranslateExistingConfigByteForByte(t *testing.T) {
	var e Effort
	if err := json.Unmarshal([]byte(`false`), &e); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	kwargs, reasoning := Translate(DialectTemplateKwargs, e)
	b, err := json.Marshal(kwargs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"enable_thinking":false}` {
		t.Errorf("kwargs = %s, want {\"enable_thinking\":false}", b)
	}
	if reasoning != nil {
		t.Errorf("reasoning = %+v, want nil", reasoning)
	}
}
