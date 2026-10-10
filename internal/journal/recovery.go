package journal

import (
	"encoding/json"
	"fmt"
)

// TypeReasoningFallback is the entry type for one issue #149 reasoning
// fallback receipt: an empty finish (no content, no tool calls) from a role
// whose reasoning is ON was recovered by re-sending the SAME request once
// with reasoning pinned off (runLoop's natural-finish path —
// salvageEmptyReasoningRetry). One event type covers the whole recovery
// (a reasoning model that spent its whole turn deliberating and came back
// with nothing, recovered by one targeted retry); the Path field is the
// call site that fired it, kept so a future second path could be told apart
// for per-path frequency counts without a new entry type. Written to the
// project-scope recovery class dir (.cortex/journal/recovery/) — see
// cmd/cortex/recovery_journal.go's appendReasoningFallback for the write
// path — so telemetry shows which models keep needing it per project,
// alongside the project-scope study.result / eval / capture receipts.
const TypeReasoningFallback = "recovery.reasoning_fallback"

// ReasoningFallbackPath distinguishes the call sites that can fire the
// recovery. Today only "natural" exists: runLoop's natural-finish branch
// (no tool calls, empty content, mid-loop — the model answered with nothing).
const (
	ReasoningFallbackPathNatural = "natural"
)

// Outcome values for ReasoningFallbackPayload.Outcome: the two shapes the
// reasoning-off retry can recover the round in.
const (
	// OutcomeAnswer: the retry returned prose — used as the turn's answer.
	OutcomeAnswer = "answer"
	// OutcomeToolCalls: the retry returned tool calls — dispatched like a
	// normal round, the loop continues.
	OutcomeToolCalls = "tool_calls"
)

// ReasoningFallbackPayload is one fallback receipt. Change is never
// included: the recovery is a re-send of the SAME request with a different
// effort, not a model substitution (which is what model.substitution
// records) — there is no old→new model pair to record. The receipt names
// the model that needed the fallback (Model), the role that was running
// (Role), which path fired it (Path), how the retry recovered the round
// (Outcome — the retry answered with prose or with tool calls), and enough
// context for a reader to correlate with the surrounding turn (the stop
// reason the recovery attributed, the clamp state at the time) without
// re-deriving it from the transcript.
type ReasoningFallbackPayload struct {
	// Outcome is how the reasoning-off retry recovered the round: OutcomeAnswer
	// (it answered with prose, returned as the turn's answer) or OutcomeToolCalls
	// (it answered with tool calls, dispatched like a normal round). Lets a
	// reader tell the two recoveries apart — the tool-call path continues the
	// loop rather than ending it.
	Outcome string `json:"outcome,omitempty"`
	// Model is the in-flight model id the recovery ran against — the model
	// that spent its turn deliberating and came back empty. The field the
	// issue #149 telemetry asks for ("which models keep needing it").
	Model string `json:"model"`
	// Role is the role binding that was running ("code" or "study") — the
	// same vocabulary model.substitution / model.failure use.
	Role string `json:"role"`
	// Path is ReasoningFallbackPathNatural — see the const block above.
	Path string `json:"path"`
	// StopReason is the stop reason the recovery attributed — the recovery's
	// own attribution, not the run's final stop reason: "salvaged-finalize"
	// when the retry answered with prose (OutcomeAnswer), and "tool-round"
	// when it answered with tool calls (OutcomeToolCalls) that the loop then
	// dispatches as an ordinary round — the run's StopReason is left to the
	// round the loop actually ends in, which the receipt does not predict.
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
	if p.Path != ReasoningFallbackPathNatural {
		return nil, fmt.Errorf("journal: recovery.reasoning_fallback requires Path (%q), got %q",
			ReasoningFallbackPathNatural, p.Path)
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
