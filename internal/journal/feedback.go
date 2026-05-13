package journal

import (
	"encoding/json"
	"fmt"
)

// Feedback writer-class entry types. Each grades a prior derivation by
// referencing its journal offset.
const (
	TypeFeedbackCorrection   = "feedback.correction"
	TypeFeedbackConfirmation = "feedback.confirmation"
	TypeFeedbackRetraction   = "feedback.retraction"
)

// FeedbackPayload is the shared shape across all feedback entry types.
// GradedOffset names the derivation entry being graded; GradedID is the
// derivation's user-facing ID (e.g. insight ID) for cases where offset is
// not yet resolvable. Note is the human-readable rationale.
//
// Subtype-specific fields:
//   - correction: Replacement holds the corrected content/text.
//   - confirmation: no extra fields.
//   - retraction: Reason is the retraction rationale (often "user said
//     /cortex-forget").
type FeedbackPayload struct {
	GradedOffset Offset `json:"graded_offset,omitempty"`
	GradedID     string `json:"graded_id,omitempty"`
	Note         string `json:"note,omitempty"`
	Replacement  string `json:"replacement,omitempty"`
	Reason       string `json:"reason,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

// NewFeedbackEntry builds a feedback entry. typ must be one of
// TypeFeedback*. At least one of GradedOffset/GradedID must be set so the
// projector can link the feedback to the derivation it grades.
func NewFeedbackEntry(typ string, p FeedbackPayload) (*Entry, error) {
	if !isFeedbackType(typ) {
		return nil, fmt.Errorf("journal: %q is not a feedback entry type", typ)
	}
	if p.GradedOffset == 0 && p.GradedID == "" {
		return nil, fmt.Errorf("journal: feedback requires GradedOffset or GradedID")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal feedback: %w", err)
	}
	entry := &Entry{Type: typ, V: 1, Payload: data}
	if p.GradedOffset != 0 {
		entry.Sources = []Offset{p.GradedOffset}
	}
	return entry, nil
}

// ParseFeedback decodes a feedback entry's payload. The Type indicates
// which subtype (correction/confirmation/retraction).
func ParseFeedback(e *Entry) (*FeedbackPayload, error) {
	if !isFeedbackType(e.Type) {
		return nil, fmt.Errorf("journal: entry type %q is not feedback", e.Type)
	}
	var p FeedbackPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse feedback: %w", err)
	}
	return &p, nil
}

func isFeedbackType(t string) bool {
	switch t {
	case TypeFeedbackCorrection, TypeFeedbackConfirmation, TypeFeedbackRetraction:
		return true
	}
	return false
}
