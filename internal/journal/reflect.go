package journal

import (
	"encoding/json"
	"fmt"
)

// TypeReflectRerank is the entry type for Reflect's reranking decisions.
const TypeReflectRerank = "reflect.rerank"

// ContradictionRecord captures one pairwise (or n-way) contradiction
// detected during reranking. IDs are candidate result IDs; Reason is the
// LLM's natural-language explanation.
type ContradictionRecord struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

// ReflectRerankPayload records one reranking operation: the query, the
// candidate IDs that went in, the order that came out, and any
// contradictions detected. Enables replay against the same inputs with
// different configs (counterfactual eval, slice X2) and a contradictions
// table for audit / debugging.
//
// InputContents (slice X2.2, optional) snapshots the content of each
// candidate at the moment of reranking. Without it, counterfactual
// replay cannot reconstruct the candidate list — IDs alone are not
// portable across storage rotations. Old entries without
// InputContents parse fine (omitempty); they just can't be replayed.
type ReflectRerankPayload struct {
	QueryText      string                `json:"query_text"`
	InputIDs       []string              `json:"input_ids"`
	InputContents  map[string]string     `json:"input_contents,omitempty"`
	RankedIDs      []string              `json:"ranked_ids"`
	Contradictions []ContradictionRecord `json:"contradictions,omitempty"`
	Reasoning      string                `json:"reasoning,omitempty"`
	SessionID      string                `json:"session_id,omitempty"`
}

// NewReflectRerankEntry builds a journal entry for one rerank operation.
func NewReflectRerankEntry(p ReflectRerankPayload) (*Entry, error) {
	if p.QueryText == "" {
		return nil, fmt.Errorf("journal: reflect.rerank requires QueryText")
	}
	if len(p.RankedIDs) == 0 {
		return nil, fmt.Errorf("journal: reflect.rerank requires non-empty RankedIDs")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal reflect.rerank: %w", err)
	}
	return &Entry{
		Type:    TypeReflectRerank,
		V:       1,
		Payload: data,
	}, nil
}

// ParseReflectRerank decodes a reflect.rerank entry's payload.
func ParseReflectRerank(e *Entry) (*ReflectRerankPayload, error) {
	if e.Type != TypeReflectRerank {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeReflectRerank)
	}
	var p ReflectRerankPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse reflect.rerank: %w", err)
	}
	return &p, nil
}
