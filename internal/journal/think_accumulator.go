package journal

import (
	"encoding/json"
	"fmt"
)

// Think writer-class entry types — accumulator slice.
//
// The accumulator is a bounded working-memory snapshot maintained
// across a session's micro-nodes. Each update is one compression
// step: (previous snapshot, new observation) → new snapshot. The
// journal carries the full trajectory so a later reader can replay
// the working memory, see what was kept vs dropped at each step,
// and counterfactually re-run with a different compression rule.
//
// Lives in the think class (alongside session_context /
// session_summary) because writing accumulator state IS the model
// thinking about what to remember; it doesn't fit the observation /
// dream / reflect / eval roles. Promotion to a dedicated "attend"
// class is a follow-up if the slice proves out.
const TypeThinkAccumulatorUpdate = "think.accumulator_update"

// TypeThinkAccumulatorCompact records a rolling re-summarization
// step (attend.compact): K previous snapshots merged into one
// tighter snapshot. Distinct from accumulator_update so readers can
// distinguish "folded one observation" from "merged K snapshots"
// when replaying or analyzing.
const TypeThinkAccumulatorCompact = "think.accumulator_compact"

// ThinkAccumulatorUpdatePayload records one bounded-memory update.
//
// Semantics:
//   - PrevSnapshotID is empty on the very first update of a session
//     (no parent). For all subsequent updates it points back to the
//     last accumulator_update emitted for the same SessionID.
//   - Snapshot is the FULL post-compression text — the working
//     memory after this step. Readers don't need to merge anything;
//     the latest entry per session IS the current state.
//   - SourceObservation is what was folded in at this step. Kept
//     for replay so a different compression rule can re-run from
//     the same input.
//   - SnapshotTokens is the 4-char-heuristic estimate (matches
//     pkg/llm/budget.go EstimateChatTokens). Off by ~10–25%; fine
//     for budget tracking.
//   - MaxTokens is the budget the compressor was asked to respect.
//     SnapshotTokens > MaxTokens signals a compressor that ignored
//     the cap — useful when calibrating small-model behavior.
//   - SourceNodeIDs records which DAG nodes contributed this step
//     (typically the act.* / sense.* that produced
//     SourceObservation). Empty when invoked outside a DAG.
//   - Step is the monotonic 0-indexed position in the session's
//     accumulator chain. SessionID + Step uniquely identifies the
//     update.
type ThinkAccumulatorUpdatePayload struct {
	SessionID         string   `json:"session_id"`
	Step              int      `json:"step"`
	PrevSnapshotID    string   `json:"prev_snapshot_id,omitempty"`
	Snapshot          string   `json:"snapshot"`
	SourceObservation string   `json:"source_observation,omitempty"`
	SnapshotTokens    int      `json:"snapshot_tokens"`
	MaxTokens         int      `json:"max_tokens"`
	SourceNodeIDs     []string `json:"source_node_ids,omitempty"`
	CompressorOp      string   `json:"compressor_op,omitempty"` // "attend.accumulate" | "passthrough" | "fallback"
}

// NewThinkAccumulatorUpdateEntry builds an entry for one accumulator
// update. SessionID + Step are required so readers can dedupe and
// order updates without scanning all entries.
func NewThinkAccumulatorUpdateEntry(p ThinkAccumulatorUpdatePayload) (*Entry, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("journal: think.accumulator_update requires SessionID")
	}
	if p.Snapshot == "" {
		return nil, fmt.Errorf("journal: think.accumulator_update requires Snapshot")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal think.accumulator_update: %w", err)
	}
	return &Entry{Type: TypeThinkAccumulatorUpdate, V: 1, Payload: data}, nil
}

// ParseThinkAccumulatorUpdate decodes a think.accumulator_update entry.
func ParseThinkAccumulatorUpdate(e *Entry) (*ThinkAccumulatorUpdatePayload, error) {
	if e.Type != TypeThinkAccumulatorUpdate {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeThinkAccumulatorUpdate)
	}
	var p ThinkAccumulatorUpdatePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse think.accumulator_update: %w", err)
	}
	return &p, nil
}

// ThinkAccumulatorCompactPayload records a rolling re-summarization
// step: K input snapshots merged into one tighter Snapshot. The
// CompactedCount + InputSnapshotIDs let a reader trace which prior
// accumulator_update entries got folded into this compaction.
type ThinkAccumulatorCompactPayload struct {
	SessionID         string   `json:"session_id"`
	Step              int      `json:"step"`
	Snapshot          string   `json:"snapshot"`
	SnapshotTokens    int      `json:"snapshot_tokens"`
	MaxTokens         int      `json:"max_tokens"`
	CompactedCount    int      `json:"compacted_count"`
	InputSnapshotIDs  []string `json:"input_snapshot_ids,omitempty"`
	Fallback          bool     `json:"fallback,omitempty"`
}

// NewThinkAccumulatorCompactEntry builds an entry for one rolling
// re-summarization step. SessionID + Step required.
func NewThinkAccumulatorCompactEntry(p ThinkAccumulatorCompactPayload) (*Entry, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("journal: think.accumulator_compact requires SessionID")
	}
	if p.Snapshot == "" {
		return nil, fmt.Errorf("journal: think.accumulator_compact requires Snapshot")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal think.accumulator_compact: %w", err)
	}
	return &Entry{Type: TypeThinkAccumulatorCompact, V: 1, Payload: data}, nil
}

// ParseThinkAccumulatorCompact decodes a think.accumulator_compact entry.
func ParseThinkAccumulatorCompact(e *Entry) (*ThinkAccumulatorCompactPayload, error) {
	if e.Type != TypeThinkAccumulatorCompact {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeThinkAccumulatorCompact)
	}
	var p ThinkAccumulatorCompactPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse think.accumulator_compact: %w", err)
	}
	return &p, nil
}
