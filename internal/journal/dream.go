package journal

import (
	"encoding/json"
	"fmt"
)

// TypeDreamInsight is the entry type for insights extracted by Dream.
const TypeDreamInsight = "dream.insight"

// DreamInsightPayload is the serialized form of a dream.insight entry. It
// captures everything storage.StoreInsightWithSession needs plus
// provenance metadata for replay and counterfactual evaluation.
//
// SourceItemID names the cognition.DreamItem the insight derived from
// (e.g. "memory:MEMORY.md:Direction" or a session/transcript anchor).
// SourceName names the DreamSource (memory-md, claude-history, git, ...).
//
// Journal-offset provenance (the canonical `sources` field on Entry) is
// not yet populated for dream.insight in v1 because DreamItem.ID is not
// resolved to a capture/observation offset at extraction time. A
// follow-up will close that loop; for now, the SourceItemID + SourceName
// combination is enough for projection and human inspection.
type DreamInsightPayload struct {
	InsightID    string   `json:"insight_id"`
	Category     string   `json:"category"`
	Content      string   `json:"content"`
	Importance   int      `json:"importance"` // 0-10
	Tags         []string `json:"tags,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	SourceItemID string   `json:"source_item_id,omitempty"`
	SourceName   string   `json:"source_name,omitempty"`
}

// NewDreamInsightEntry builds a journal entry for a dream.insight. The
// returned entry is unsigned by offset and TS — Writer.Append fills those.
func NewDreamInsightEntry(p DreamInsightPayload) (*Entry, error) {
	if p.InsightID == "" {
		return nil, fmt.Errorf("journal: dream.insight requires InsightID")
	}
	if p.Content == "" {
		return nil, fmt.Errorf("journal: dream.insight requires Content")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal dream.insight: %w", err)
	}
	return &Entry{
		Type:    TypeDreamInsight,
		V:       1,
		Payload: data,
	}, nil
}

// ParseDreamInsight decodes a dream.insight entry's payload.
func ParseDreamInsight(e *Entry) (*DreamInsightPayload, error) {
	if e.Type != TypeDreamInsight {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeDreamInsight)
	}
	var p DreamInsightPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse dream.insight: %w", err)
	}
	return &p, nil
}
