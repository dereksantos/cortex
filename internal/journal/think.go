package journal

import (
	"encoding/json"
	"fmt"
)

// Think writer-class entry types.
const (
	TypeThinkTopicWeight    = "think.topic_weight"
	TypeThinkSessionContext = "think.session_context"
)

// ThinkTopicWeightPayload records an update to a single topic weight in
// the session context. Useful for granular change-tracking; for full
// snapshots use ThinkSessionContextPayload.
type ThinkTopicWeightPayload struct {
	Topic     string  `json:"topic"`
	Weight    float64 `json:"weight"`
	SessionID string  `json:"session_id,omitempty"`
}

// ThinkSessionContextPayload is a periodic snapshot of the session
// context Think maintains: topic weights, recent queries, and the
// queries for which cached Reflect results have been pre-computed.
// One entry per MaybeThink cycle.
type ThinkSessionContextPayload struct {
	TopicWeights  map[string]float64 `json:"topic_weights"`
	RecentQueries []string           `json:"recent_queries,omitempty"`
	CachedQueries []string           `json:"cached_queries,omitempty"`
	SessionID     string             `json:"session_id,omitempty"`
}

// NewThinkTopicWeightEntry builds an entry for a single topic-weight update.
func NewThinkTopicWeightEntry(p ThinkTopicWeightPayload) (*Entry, error) {
	if p.Topic == "" {
		return nil, fmt.Errorf("journal: think.topic_weight requires Topic")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal think.topic_weight: %w", err)
	}
	return &Entry{Type: TypeThinkTopicWeight, V: 1, Payload: data}, nil
}

// NewThinkSessionContextEntry builds an entry for a session-context snapshot.
func NewThinkSessionContextEntry(p ThinkSessionContextPayload) (*Entry, error) {
	if p.TopicWeights == nil {
		p.TopicWeights = make(map[string]float64)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal think.session_context: %w", err)
	}
	return &Entry{Type: TypeThinkSessionContext, V: 1, Payload: data}, nil
}

// ParseThinkTopicWeight decodes a think.topic_weight entry.
func ParseThinkTopicWeight(e *Entry) (*ThinkTopicWeightPayload, error) {
	if e.Type != TypeThinkTopicWeight {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeThinkTopicWeight)
	}
	var p ThinkTopicWeightPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse think.topic_weight: %w", err)
	}
	return &p, nil
}

// ParseThinkSessionContext decodes a think.session_context entry.
func ParseThinkSessionContext(e *Entry) (*ThinkSessionContextPayload, error) {
	if e.Type != TypeThinkSessionContext {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeThinkSessionContext)
	}
	var p ThinkSessionContextPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse think.session_context: %w", err)
	}
	return &p, nil
}
