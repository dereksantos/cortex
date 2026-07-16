package llm

import (
	"encoding/json"
	"fmt"
)

// effort.go is the vocabulary + translation seam docs/thinking-models.md §1-2
// describe: a small closed set of reasoning-effort intents (Effort), and a
// pure per-dialect translation from intent to wire fields. It lives in
// pkg/llm (not cmd/cortex, where ModelSpec lives today) because both the
// composition root (cmd/cortex) and any future factory-routed caller need
// the same translation, and the wire shapes below are provider dialects —
// exactly what this package already owns for everything else on the wire.
//
// Terminology (CLAUDE.md): new identifiers say "reasoning" (the trace/
// tokens) and "effort" (the control) — never "think"/"thinking" in code.
// "Thinking" survives only in user-facing strings and the JSON config key
// (`"thinking"`, chosen for readability in a hand-edited config file).

// EffortLevel is the closed vocabulary docs/thinking-models.md §1 defines.
type EffortLevel string

const (
	// EffortUnset is the zero value: no effort was specified for this
	// binding. Translation treats it exactly like EffortOn (today's
	// implicit "model default" behavior) but callers can distinguish
	// "explicitly on" from "never set" when that matters (e.g. deciding
	// whether a config override should win over a role default).
	EffortUnset EffortLevel = ""
	// EffortOff suppresses reasoning.
	EffortOff EffortLevel = "off"
	// EffortOn requests the model's default reasoning behavior — today's
	// implicit state, now sayable.
	EffortOn EffortLevel = "on"
	// EffortLow/EffortMedium/EffortHigh are effort levels for dialects that
	// have them (OpenRouter's reasoning.effort); dialects without levels
	// degrade them to EffortOn (see Translate).
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
)

// Level→budget tiers used when a dialect needs a token budget but the
// caller only gave a level (docs/thinking-models.md "Open decisions").
const (
	EffortBudgetLow    = 1024
	EffortBudgetMedium = 4096
	EffortBudgetHigh   = 16384
)

// Effort is the resolved reasoning-effort intent for one model binding or
// request — what ModelSpec.Thinking carries and what Translate consumes.
// The zero value (Effort{}) is EffortUnset with no budget: "not specified."
type Effort struct {
	Level EffortLevel
	// Budget is an explicit token budget (docs/thinking-models.md's
	// `{"budget": N}` form). 0 means no explicit budget was given. A
	// non-zero Budget with Level == EffortUnset means "deliberate, with
	// this budget" — BudgetTier resolves it to a level-shaped tier for
	// dialects that need one.
	Budget int
}

// IsZero reports whether e carries no explicit intent at all (JSON key
// absent, or the legacy bool never set) — the state a role-policy default or
// a config override should still be free to fill in.
func (e Effort) IsZero() bool { return e.Level == EffortUnset && e.Budget == 0 }

// BudgetTier maps a level to a fixed token-budget tier (the strawman values
// docs/thinking-models.md's "Open decisions" proposes), for dialects that
// need a budget but were only given a level. An explicit e.Budget always
// wins.
func (e Effort) BudgetTier() int {
	if e.Budget > 0 {
		return e.Budget
	}
	switch e.Level {
	case EffortLow:
		return EffortBudgetLow
	case EffortMedium:
		return EffortBudgetMedium
	case EffortHigh:
		return EffortBudgetHigh
	default:
		return 0
	}
}

// UnmarshalJSON accepts the three JSON shapes docs/thinking-models.md §1
// declares: a legacy bool (false→off, true→on), a level string
// ("off"/"on"/"low"/"medium"/"high"), or {"budget": N}.
func (e *Effort) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		if b {
			*e = Effort{Level: EffortOn}
		} else {
			*e = Effort{Level: EffortOff}
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch EffortLevel(s) {
		case EffortOff, EffortOn, EffortLow, EffortMedium, EffortHigh:
			*e = Effort{Level: EffortLevel(s)}
			return nil
		default:
			return fmt.Errorf("invalid thinking level %q (want off, on, low, medium, or high)", s)
		}
	}
	var obj struct {
		Budget int `json:"budget"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.Budget > 0 {
		*e = Effort{Budget: obj.Budget}
		return nil
	}
	return fmt.Errorf("invalid thinking value %s (want a bool, a level string, or {\"budget\": N})", string(data))
}

// MarshalJSON emits the canonical form: null for unset, {"budget": N} for a
// pure budget ask, otherwise the level string. This is what a resolved
// binding (e.g. GET /api/models) presents — round-tripping a config still
// works because UnmarshalJSON accepts the level-string form it produces.
func (e Effort) MarshalJSON() ([]byte, error) {
	if e.IsZero() {
		return []byte("null"), nil
	}
	if e.Level == EffortUnset && e.Budget > 0 {
		return json.Marshal(struct {
			Budget int `json:"budget"`
		}{e.Budget})
	}
	return json.Marshal(string(e.Level))
}

// Dialect selects which provider wire shape Translate targets. Only the two
// dialects reachable from cmd/cortex today are implemented (see
// docs/thinking-models.md "Open decisions" — Anthropic and Ollama-native are
// deferred).
type Dialect int

const (
	// DialectTemplateKwargs is llama.cpp / LiteLLM's chat_template_kwargs —
	// today's only mechanism. Levels have no representation there and
	// degrade to "on" (i.e. no kwarg at all); a budget is unsupported and
	// also degrades to "on".
	DialectTemplateKwargs Dialect = iota
	// DialectOpenRouter is OpenRouter's request-body `reasoning: {...}`.
	DialectOpenRouter
)

// Reasoning is OpenRouter's request-body reasoning object
// (docs/thinking-models.md §2's dialect table). Fields are omitempty so a
// caller that only sets one still marshals compactly.
type Reasoning struct {
	// Enabled is a pointer so `false` (off) is distinguishable from unset
	// (omit the field entirely, e.g. for EffortOn — let the model default).
	Enabled *bool  `json:"enabled,omitempty"`
	Effort  string `json:"effort,omitempty"`
	// MaxTokens is OpenRouter's reasoning token budget.
	MaxTokens int `json:"max_tokens,omitempty"`
}

var falseVal = false

// Translate converts a resolved Effort into the wire fields for one dialect:
// chat_template_kwargs (kwargs, non-nil only for DialectTemplateKwargs) and
// OpenRouter's reasoning body (reasoning, non-nil only for
// DialectOpenRouter). Exactly one return is ever non-nil, matching the two
// AgentRequest fields (ChatTemplateKwargs, Reasoning) that are each
// populated by exactly one dialect. Pure function: no I/O, no defaults
// beyond the vocabulary itself.
func Translate(d Dialect, e Effort) (kwargs map[string]any, reasoning *Reasoning) {
	switch d {
	case DialectTemplateKwargs:
		if e.Level == EffortOff {
			return map[string]any{"enable_thinking": false}, nil
		}
		// Unset, on, every level, and a bare budget all degrade to "on" —
		// this dialect has no representation for levels or budgets
		// (docs/thinking-models.md §2 dialect table), so the model just
		// runs its own default reasoning behavior.
		return nil, nil
	case DialectOpenRouter:
		switch {
		case e.Level == EffortOff:
			return nil, &Reasoning{Enabled: &falseVal}
		case e.Level == EffortLow || e.Level == EffortMedium || e.Level == EffortHigh:
			return nil, &Reasoning{Effort: string(e.Level)}
		case e.Level == EffortUnset && e.Budget > 0:
			return nil, &Reasoning{MaxTokens: e.Budget}
		default:
			// EffortUnset or EffortOn: no reasoning field at all — model default.
			return nil, nil
		}
	default:
		return nil, nil
	}
}
