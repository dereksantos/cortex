package journal

import (
	"encoding/json"
	"fmt"
)

// TypeModelSubstitution is the entry type for a live-catalog substitution of
// a curated OpenRouter :free model pick (docs/completion-roadmap.md Track
// E2): a model a session bound at startup turned out to be no longer served,
// and the startup preflight (cmd/cortex/preflight.go) picked a replacement
// for this process only — this event is the receipt. Mirrors study.go's
// shape: a single typed payload, no shared eval vocabulary to embed, since a
// substitution isn't a scored task rep.
const TypeModelSubstitution = "model.substitution"

// ModelSubstitutionPayload is one substitution event.
type ModelSubstitutionPayload struct {
	// Role is the role binding that was substituted ("code" or "study").
	Role string `json:"role"`
	// Old is the bound model id the preflight found missing from
	// OpenRouter's live catalog.
	Old string `json:"old"`
	// New is the id the preflight substituted for this process.
	New string `json:"new"`
	// Reason is a short, human-readable explanation of how New was chosen
	// (e.g. "next curated pick still served" or "coder-named :free model
	// with the largest context") — the same text surfaced in the matching
	// stderr line.
	Reason string `json:"reason"`
}

// NewModelSubstitutionEntry builds a journal entry for one substitution event.
func NewModelSubstitutionEntry(p ModelSubstitutionPayload) (*Entry, error) {
	if p.Role == "" {
		return nil, fmt.Errorf("journal: model.substitution requires Role")
	}
	if p.Old == "" || p.New == "" {
		return nil, fmt.Errorf("journal: model.substitution requires Old and New")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal model.substitution: %w", err)
	}
	return &Entry{Type: TypeModelSubstitution, V: 1, Payload: data}, nil
}

// ParseModelSubstitution decodes a model.substitution entry's payload.
func ParseModelSubstitution(e *Entry) (*ModelSubstitutionPayload, error) {
	if e.Type != TypeModelSubstitution {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeModelSubstitution)
	}
	var p ModelSubstitutionPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse model.substitution: %w", err)
	}
	return &p, nil
}
