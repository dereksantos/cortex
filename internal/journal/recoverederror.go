package journal

import (
	"encoding/json"
	"fmt"
)

// TypeModelRecoveredError is the entry type for a model-call failure the
// turn RECOVERED from (docs/model-self-healing.md §2, issue #117): a
// mid-turn send failed AFTER progress, and the run finalized from what it
// had — the turn succeeded, so `cortex model`'s recent-events view must not
// show it as a FAILED unrecovered failure. The shape mirrors
// ModelFailurePayload; it is a DISTINCT type (not model.failure) so the two
// outcomes — unrecovered (model.failure) and recovered
// (model.recovered_error) — stay tellable apart in the same journal class
// dir, and one failed send can never produce two receipts of the same kind.
const TypeModelRecoveredError = "model.recovered_error"

// ModelRecoveredErrorPayload is one recovered model-call failure.
type ModelRecoveredErrorPayload struct {
	// Role is the role binding that was failing ("code" or "study"/profile).
	Role string `json:"role"`
	// Model is the model id whose call failed.
	Model string `json:"model"`
	// Class is the classified failure ("model-missing", "rate-limited",
	// "server", "auth", "timeout", "unreachable", or "" for unclassified).
	Class string `json:"class"`
	// Status is the HTTP status when one was observed, else 0.
	Status int `json:"status,omitempty"`
	// Detail is a short, truncated slice of the underlying error text
	// (secrets redacted by the caller before it reaches the receipt).
	Detail string `json:"detail,omitempty"`
}

// NewModelRecoveredErrorEntry builds a journal entry for one recovered failure.
func NewModelRecoveredErrorEntry(p ModelRecoveredErrorPayload) (*Entry, error) {
	if p.Role == "" {
		return nil, fmt.Errorf("journal: model.recovered_error requires Role")
	}
	if p.Model == "" {
		return nil, fmt.Errorf("journal: model.recovered_error requires Model")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal model.recovered_error: %w", err)
	}
	return &Entry{Type: TypeModelRecoveredError, V: 1, Payload: data}, nil
}

// ParseModelRecoveredError decodes a model.recovered_error entry's payload.
func ParseModelRecoveredError(e *Entry) (*ModelRecoveredErrorPayload, error) {
	if e.Type != TypeModelRecoveredError {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeModelRecoveredError)
	}
	var p ModelRecoveredErrorPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse model.recovered_error: %w", err)
	}
	return &p, nil
}
