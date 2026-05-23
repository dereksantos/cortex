package journal

import (
	"encoding/json"
	"fmt"
)

// Think writer-class entry types.
const (
	TypeThinkTopicWeight    = "think.topic_weight"
	TypeThinkSessionContext = "think.session_context"
	// TypeThinkSessionSummary is one compressed turn summary the REPL
	// emits at finalize. Designed as the durable substitute for raw
	// (user, assistant) prior-message pairs — subsequent turns inject
	// a "summary of last K turns" string built from these entries
	// instead of replaying full transcripts. See
	// docs/salience-budgets.md "Cross-turn context (per-session)".
	TypeThinkSessionSummary = "think.session_summary"
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

// ThinkSessionSummaryPayload is one per-turn compressed summary. The
// REPL emits one of these at finalize on every accepted turn; the
// next turn's prior-messages slot pulls the most recent N to inject
// as compressed context instead of the raw (user, assistant)
// transcript. See docs/salience-budgets.md.
//
// Fields are intentionally a flat shape — the calibration loop reads
// them without recursive JSON parsing.
type ThinkSessionSummaryPayload struct {
	SessionID    string   `json:"session_id"`
	Turn         int      `json:"turn"`
	UserPrompt   string   `json:"user_prompt"`
	Summary      string   `json:"summary"` // compressed prose (1-3 sentences typically)
	FilesChanged []string `json:"files_changed,omitempty"`
	VerifyKind   string   `json:"verify_kind,omitempty"` // mirrors session JSONL field
	VerifyOK     bool     `json:"verify_ok"`
	OrigTokens   int      `json:"orig_tokens,omitempty"` // pre-compression size (approx)
	KeptTokens   int      `json:"kept_tokens,omitempty"` // post-compression size (approx)
	CompressOp   string   `json:"compress_op,omitempty"` // "attend.compress" / "passthrough" / "fallback"

	// Intent is the per-turn classification picked by
	// sense.classify_intent at seed time (greeting | recall | clarify |
	// code | review | meta). Drives per-turn budget + seed shape and
	// — once persisted here — lets cross-session memory weight prior
	// summaries by intent. Empty on entries written before the field
	// existed; readers tolerate the absence rather than failing the
	// parse. IntentConfidence is the classifier's self-reported
	// probability; 0 when the classifier wasn't consulted or fell back.
	Intent           string  `json:"intent,omitempty"`
	IntentConfidence float64 `json:"intent_confidence,omitempty"`
}

// NewThinkSessionSummaryEntry builds an entry for a per-turn rolling
// summary. SessionID + Turn together uniquely identify the entry — a
// reader can dedupe by (session_id, turn) when replaying.
func NewThinkSessionSummaryEntry(p ThinkSessionSummaryPayload) (*Entry, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("journal: think.session_summary requires SessionID")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal think.session_summary: %w", err)
	}
	return &Entry{Type: TypeThinkSessionSummary, V: 1, Payload: data}, nil
}

// ParseThinkSessionSummary decodes a think.session_summary entry.
func ParseThinkSessionSummary(e *Entry) (*ThinkSessionSummaryPayload, error) {
	if e.Type != TypeThinkSessionSummary {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeThinkSessionSummary)
	}
	var p ThinkSessionSummaryPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse think.session_summary: %w", err)
	}
	return &p, nil
}
