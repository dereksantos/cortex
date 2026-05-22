package journal

import (
	"encoding/json"
	"fmt"
)

// TypeDreamInsight is the entry type for insights extracted by Dream.
const TypeDreamInsight = "dream.insight"

// TypeDreamSessionDigest is a consolidated narrative summarizing
// K recent think.session_summary entries. Emitted by the REPL when
// the per-turn summary count crosses a threshold — keeps the cross-
// session prior-context block bounded as turns accumulate. The next
// turn's priorMessagesForHarness reads the most recent digest plus
// only the summaries written after it, instead of every summary in
// the journal.
const TypeDreamSessionDigest = "dream.session_digest"

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

// DreamSessionDigestPayload is the serialized form of a per-session
// rolling digest. One entry summarizes SummaryCountIn prior
// think.session_summary entries into a single Narrative. The
// hydration path prefers the most recent digest (latest entry by TS)
// plus any session_summary entries with TS > the digest's TS so
// long sessions don't blow the prior-context budget.
//
// CoversSessionIDs lists the distinct session ids the folded
// summaries came from — useful for the user's introspection
// (`cortex journal show --type=dream.session_digest`) and for a
// future invalidation path where a feedback.retraction inside the
// covered window marks the digest stale.
type DreamSessionDigestPayload struct {
	Narrative        string   `json:"narrative"`        // consolidated prose, ~2-3k tokens
	SummaryCountIn   int      `json:"summary_count_in"` // how many session_summary entries were folded in
	CoversSessionIDs []string `json:"covers_session_ids,omitempty"`
	OrigTokens       int      `json:"orig_tokens,omitempty"` // pre-compression total across folded summaries
	KeptTokens       int      `json:"kept_tokens,omitempty"` // post-compression size of Narrative
	CompressOp       string   `json:"compress_op,omitempty"` // "dream.session_digest" / "fallback"
}

// NewDreamSessionDigestEntry builds a journal entry for a
// dream.session_digest. Returns an error when Narrative is empty or
// SummaryCountIn is zero — the digest is meaningless without source
// material to summarize.
func NewDreamSessionDigestEntry(p DreamSessionDigestPayload) (*Entry, error) {
	if p.Narrative == "" {
		return nil, fmt.Errorf("journal: dream.session_digest requires Narrative")
	}
	if p.SummaryCountIn <= 0 {
		return nil, fmt.Errorf("journal: dream.session_digest requires SummaryCountIn > 0")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal dream.session_digest: %w", err)
	}
	return &Entry{
		Type:    TypeDreamSessionDigest,
		V:       1,
		Payload: data,
	}, nil
}

// ParseDreamSessionDigest decodes a dream.session_digest entry's
// payload. Returns an error when the entry's type doesn't match.
func ParseDreamSessionDigest(e *Entry) (*DreamSessionDigestPayload, error) {
	if e.Type != TypeDreamSessionDigest {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeDreamSessionDigest)
	}
	var p DreamSessionDigestPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse dream.session_digest: %w", err)
	}
	return &p, nil
}
