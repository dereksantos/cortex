package journal

import (
	"encoding/json"
	"fmt"
)

// TypeResolveRetrieval is the entry type for Resolve's inject/wait/queue
// decisions.
const TypeResolveRetrieval = "resolve.retrieval"

// ResolveRetrievalPayload records one retrieval decision. Captures the
// query, decision, confidence, candidate counts, and the IDs of results
// that were ultimately injected (subset of all candidates). Enables
// replay against the same inputs with different thresholds (counterfactual
// eval), and feeds an aggregate stats projection.
//
// Mode is "fast" or "full"; ResolveMs and TotalMs are populated by the
// retriever (session.writeRetrievalStats path) and consumed by the watch
// UI. Old entries without latencies parse fine (omitempty); their watch
// projections show "-" for unknown timings.
type ResolveRetrievalPayload struct {
	QueryText   string   `json:"query_text"`
	Decision    string   `json:"decision"` // "inject" | "wait" | "queue" | "skip"
	Confidence  float64  `json:"confidence"`
	ResultCount int      `json:"result_count"`
	InjectedIDs []string `json:"injected_ids,omitempty"`
	AvgScore    float64  `json:"avg_score,omitempty"`
	MaxScore    float64  `json:"max_score,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	Mode        string   `json:"mode,omitempty"`       // "fast" | "full"
	ResolveMs   int64    `json:"resolve_ms,omitempty"` // Resolve step latency
	TotalMs     int64    `json:"total_ms,omitempty"`   // End-to-end retrieve latency
}

// NewResolveRetrievalEntry builds a journal entry for one resolve decision.
func NewResolveRetrievalEntry(p ResolveRetrievalPayload) (*Entry, error) {
	if p.Decision == "" {
		return nil, fmt.Errorf("journal: resolve.retrieval requires Decision")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal resolve.retrieval: %w", err)
	}
	return &Entry{
		Type:    TypeResolveRetrieval,
		V:       1,
		Payload: data,
	}, nil
}

// ParseResolveRetrieval decodes a resolve.retrieval entry's payload.
func ParseResolveRetrieval(e *Entry) (*ResolveRetrievalPayload, error) {
	if e.Type != TypeResolveRetrieval {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeResolveRetrieval)
	}
	var p ResolveRetrievalPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse resolve.retrieval: %w", err)
	}
	return &p, nil
}
