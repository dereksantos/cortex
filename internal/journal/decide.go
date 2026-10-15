package journal

import (
	"encoding/json"
	"fmt"
)

// TypeDecideShadow is the entry type for one shadow decision (docs/decide.md):
// the parallel decider's answer to one question at one point of a turn,
// written to .cortex/journal/decide/. Shadow means log-only — the answer was
// never shown to the coding model. Each row pairs the answer with what the
// agent actually did next, so a later analysis can ask whether the decider
// predicted anything.
const TypeDecideShadow = "decide.shadow"

// DecideActual is what the agent actually did after a decision point.
type DecideActual struct {
	// Kind is "tools" (it ran another batch), "finalize" (it answered), or
	// "forced:<stop reason>" (a bound ended the turn); "" when unknown (the
	// session closed first) or for the finalize point itself.
	Kind  string   `json:"kind,omitempty"`
	Moves []string `json:"moves,omitempty"` // the next batch on the next_move vocabulary
	Tools []string `json:"tools,omitempty"` // the next batch's tool names
}

// DecideShadowPayload is one shadow decision for one question.
type DecideShadowPayload struct {
	Session  string   `json:"session,omitempty"`
	Turn     int      `json:"turn"`
	Round    int      `json:"round"`           // the tool batch the point follows (1-based)
	Point    string   `json:"point"`           // "after_batch" | "finalize"
	Question string   `json:"question"`        // next_move | looping | claim_check | recall_target
	Choices  []string `json:"choices"`         // the closed set offered
	Picks    []string `json:"picks,omitempty"` // empty unless Status is "ok"
	// Confidence is the backend's score for the pick (0 = unreported).
	Confidence float64 `json:"confidence,omitempty"`

	Backend       string `json:"backend"`
	Model         string `json:"model,omitempty"`
	Status        string `json:"status"` // ok | timeout | rate_limited | parse_error | budget | error | busy
	Detail        string `json:"detail,omitempty"`
	LatencyMs     int64  `json:"latency_ms"`
	PromptVersion string `json:"prompt_version,omitempty"`

	// RoundsLate is how many sends went out between the decision's start
	// and its completion: 1 = ready for the send after the agent's next
	// round (the earliest any decision can be — the engine sends the next
	// request the moment a batch ends), 2 = one round later, … -1 = no
	// answer (not ok, busy, or cut off). Always -1 for the finalize point.
	RoundsLate int `json:"rounds_late"`

	Actual DecideActual `json:"actual"`
}

// NewDecideShadowEntry builds a journal entry for one shadow decision.
func NewDecideShadowEntry(p DecideShadowPayload) (*Entry, error) {
	if p.Question == "" {
		return nil, fmt.Errorf("journal: decide.shadow requires Question")
	}
	switch p.Point {
	case "after_batch", "finalize":
	default:
		return nil, fmt.Errorf("journal: decide.shadow Point must be %q or %q, got %q", "after_batch", "finalize", p.Point)
	}
	if p.Status == "" {
		return nil, fmt.Errorf("journal: decide.shadow requires Status")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal decide.shadow: %w", err)
	}
	return &Entry{Type: TypeDecideShadow, V: 1, Payload: data}, nil
}

// ParseDecideShadow decodes a decide.shadow entry's payload.
func ParseDecideShadow(e *Entry) (*DecideShadowPayload, error) {
	if e.Type != TypeDecideShadow {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeDecideShadow)
	}
	var p DecideShadowPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse decide.shadow: %w", err)
	}
	return &p, nil
}
