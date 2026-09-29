package journal

import (
	"encoding/json"
	"fmt"
)

// TypeReasoningFallback is the entry type for one issue #149 reasoning
// fallback receipt: an empty finish (no content, no tool calls) from a role
// whose reasoning is ON was recovered by re-sending the SAME request once
// with reasoning pinned off (the natural-finish path) or with the role's
// own (on) reasoning (the forced-finalize path, whose deferred
// disableEffortForSend had pinned it off for the forced finalize itself).
// One event type covers both paths — the recovery is the same in spirit
// (a reasoning model that spent its whole turn deliberating and came back
// with nothing, recovered by one targeted retry); the Path field tells
// them apart for a reader building per-path frequency counts. Written to
// the project-scope recovery class dir (.cortex/journal/recovery/) — see
// cmd/cortex/recovery_journal.go's appendReasoningFallback for the write
// path — so telemetry shows which models keep needing it per project,
// alongside the project-scope study.result / eval / capture receipts.
const TypeReasoningFallback = "recovery.reasoning_fallback"

// ReasoningFallbackPath distinguishes the two call sites that can fire the
// recovery. "natural" is runLoop's natural-finish branch (no tool calls,
// empty content, mid-loop — the model answered with nothing); "forced" is
// finalizeLoop's forced-finalize empty branch (a bound tripped — max-iter,
// stuck, read-budget, token-budget, no-progress — and the forced finalize
// itself came back empty). Both set loopStats.ReasoningFallback; this is
// the field that tells them apart, mirroring SalvagedUnclamped's
// narrow-a-broader-field role on the prompt-based salvage paths.
const (
	ReasoningFallbackPathNatural = "natural"
	ReasoningFallbackPathForced  = "forced"
)

// ReasoningFallbackPayload is one fallback receipt. Change is never
// included: the recovery is a re-send of the SAME request with a different
// effort, not a model substitution (which is what model.substitution
// records) — there is no old→new model pair to record. The receipt names
// the model that needed the fallback (Model), the role that was running
// (Role), which path fired it (Path), and enough context for a reader to
// correlate with the surrounding turn (the stop reason the recovery
// attributed, the clamp state at the time) without re-deriving it from the
// transcript.
type ReasoningFallbackPayload struct {
	// Model is the in-flight model id the recovery ran against — the model
	// that spent its turn deliberating and came back empty. The field the
	// issue #149 telemetry asks for ("which models keep needing it").
	Model string `json:"model"`
	// Role is the role binding that was running ("code" or "study") — the
	// same vocabulary model.substitution / model.failure use.
	Role string `json:"role"`
	// Path is ReasoningFallbackPathNatural or ReasoningFallbackPathForced —
	// see the const block above.
	Path string `json:"path"`
	// StopReason is the stop reason the recovery attributed (always
	// "salvaged-finalize" today — the recovery's attribution; a future
	// reason the recovery could attribute without relabeling the turn
	// would still land here rather than inventing a new field).
	StopReason string `json:"stop_reason"`
	// MaxTokensClamped is the clamp state at the time of the recovery —
	// the same field loopStats.MaxTokensClamped records, kept here so a
	// reader can tell "the model hit the clamp and then came back empty"
	// from "the model just stopped with nothing" without re-reading the
	// turn's transcript.
	MaxTokensClamped bool `json:"max_tokens_clamped"`
	// SalvagedUnclamped is the same field loopStats.SalvagedUnclamped
	// records — narrowed from MaxTokensClamped for the recovery's
	// attribution, kept for parity with the prompt-based salvage receipts
	// so one reader can compare "how often does the on/off retry recover"
	// against "how often does the prompt-based salvage recover" on the
	// same clamp axis.
	SalvagedUnclamped bool `json:"salvaged_unclamped,omitempty"`
}

// NewReasoningFallbackEntry builds a journal entry for one fallback receipt.
func NewReasoningFallbackEntry(p ReasoningFallbackPayload) (*Entry, error) {
	if p.Model == "" {
		return nil, fmt.Errorf("journal: recovery.reasoning_fallback requires Model")
	}
	if p.Role == "" {
		return nil, fmt.Errorf("journal: recovery.reasoning_fallback requires Role")
	}
	if p.Path != ReasoningFallbackPathNatural && p.Path != ReasoningFallbackPathForced {
		return nil, fmt.Errorf("journal: recovery.reasoning_fallback requires Path (%q or %q), got %q",
			ReasoningFallbackPathNatural, ReasoningFallbackPathForced, p.Path)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal recovery.reasoning_fallback: %w", err)
	}
	return &Entry{Type: TypeReasoningFallback, V: 1, Payload: data}, nil
}

// ParseReasoningFallback decodes a recovery.reasoning_fallback entry's payload.
func ParseReasoningFallback(e *Entry) (*ReasoningFallbackPayload, error) {
	if e.Type != TypeReasoningFallback {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeReasoningFallback)
	}
	var p ReasoningFallbackPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse recovery.reasoning_fallback: %w", err)
	}
	return &p, nil
}
