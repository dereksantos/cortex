package journal

import (
	"encoding/json"
	"fmt"
)

// TypeModelFailure is the entry type for a model-call failure the healing
// ladder could not recover from (docs/model-self-healing.md §2): either the
// failure class was healable but every replacement candidate also failed, or
// healing was skipped (non-OpenRouter backend, self_heal off, non-healable
// class) and the failure was still worth a durable receipt. Rides the same
// journal class dir as model.substitution so `cortex model`'s recent-events
// view reads one place.
const TypeModelFailure = "model.failure"

// ModelFailurePayload is one unrecovered model-call failure.
type ModelFailurePayload struct {
	// Role is the role binding that was failing ("code" or "study"/profile).
	Role string `json:"role"`
	// Model is the model id whose call failed.
	Model string `json:"model"`
	// Class is the classified failure ("model-missing", "rate-limited",
	// "server", "auth", "timeout", "unreachable", or "" for unclassified).
	Class string `json:"class"`
	// Status is the HTTP status when one was observed, else 0.
	Status int `json:"status,omitempty"`
	// Detail is a short, truncated slice of the underlying error text.
	Detail string `json:"detail,omitempty"`
}

// NewModelFailureEntry builds a journal entry for one unrecovered failure.
func NewModelFailureEntry(p ModelFailurePayload) (*Entry, error) {
	if p.Role == "" {
		return nil, fmt.Errorf("journal: model.failure requires Role")
	}
	if p.Model == "" {
		return nil, fmt.Errorf("journal: model.failure requires Model")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal model.failure: %w", err)
	}
	return &Entry{Type: TypeModelFailure, V: 1, Payload: data}, nil
}

// ParseModelFailure decodes a model.failure entry's payload.
func ParseModelFailure(e *Entry) (*ModelFailurePayload, error) {
	if e.Type != TypeModelFailure {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeModelFailure)
	}
	var p ModelFailurePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse model.failure: %w", err)
	}
	return &p, nil
}
