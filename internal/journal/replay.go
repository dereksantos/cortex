package journal

import (
	"encoding/json"
	"fmt"
)

// TypeReplayCounterfactual is the journal entry type for a counterfactual
// replay run: a recorded cognitive mode (reflect/dream/resolve) re-run
// with overridden config (model/provider/temperature/...) plus the
// resulting comparison to the original.
//
// Counterfactual entries live in the writer-class .cortex/journal/replay/
// and reference their source entry by offset via the Sources field on
// the envelope. They are never themselves replayed (no recursive
// counterfactuals).
const TypeReplayCounterfactual = "replay.counterfactual"

// CounterfactualOverrides mirrors commands.ConfigOverrides as a
// JSON-stable shape. Kept in the journal package to avoid an
// internal/journal → cmd/cortex/commands import; the commands package
// converts its allow-listed struct into this serialization layer.
type CounterfactualOverrides struct {
	Model       string   `json:"model,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

// ReplayCounterfactualPayload records one counterfactual run. Fields
// vary by source type; the Status field signals whether the replay
// produced a usable comparison (Executed), was scheduled but not yet
// run (Planned), or failed during execution (Failed).
type ReplayCounterfactualPayload struct {
	// SourceOffset and SourceType identify the original journal entry
	// this counterfactual is comparing against. Mirrors envelope
	// Sources but kept in payload so a payload-only consumer (e.g., a
	// JSONL dump) can interpret rows without parsing envelopes.
	SourceOffset int64  `json:"source_offset"`
	SourceClass  string `json:"source_class"`
	SourceType   string `json:"source_type"`

	Overrides CounterfactualOverrides `json:"overrides"`

	// Status: "planned" | "executed" | "failed".
	Status string `json:"status"`
	// Error is populated when Status == "failed".
	Error string `json:"error,omitempty"`

	// Reflect-specific result fields. Populated when Status==executed
	// AND SourceType==reflect.rerank.
	CounterfactualRankedIDs []string `json:"counterfactual_ranked_ids,omitempty"`
	OriginalRankedIDs       []string `json:"original_ranked_ids,omitempty"`
	JaccardTopK             float64  `json:"jaccard_topk,omitempty"`
	JaccardK                int      `json:"jaccard_k,omitempty"`
}

// Status constants. Use these instead of bare strings to avoid drift.
const (
	ReplayStatusPlanned  = "planned"
	ReplayStatusExecuted = "executed"
	ReplayStatusFailed   = "failed"
)

// NewReplayCounterfactualEntry builds a journal entry for one
// counterfactual. SourceOffset, SourceClass, and Status are required;
// the rest are status-dependent and validated minimally to keep the
// journal layer permissive (downstream tooling can apply richer
// invariants).
func NewReplayCounterfactualEntry(p ReplayCounterfactualPayload) (*Entry, error) {
	if p.SourceOffset <= 0 {
		return nil, fmt.Errorf("journal: replay.counterfactual requires SourceOffset > 0")
	}
	if p.SourceClass == "" {
		return nil, fmt.Errorf("journal: replay.counterfactual requires SourceClass")
	}
	switch p.Status {
	case ReplayStatusPlanned, ReplayStatusExecuted, ReplayStatusFailed:
	default:
		return nil, fmt.Errorf("journal: replay.counterfactual unknown Status %q", p.Status)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal replay.counterfactual: %w", err)
	}
	return &Entry{
		Type:    TypeReplayCounterfactual,
		V:       1,
		Payload: data,
		Sources: []Offset{Offset(p.SourceOffset)},
	}, nil
}

// ParseReplayCounterfactual decodes a replay.counterfactual entry's
// payload.
func ParseReplayCounterfactual(e *Entry) (*ReplayCounterfactualPayload, error) {
	if e.Type != TypeReplayCounterfactual {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeReplayCounterfactual)
	}
	var p ReplayCounterfactualPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse replay.counterfactual: %w", err)
	}
	return &p, nil
}
