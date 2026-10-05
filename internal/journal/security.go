package journal

import (
	"encoding/json"
	"fmt"
)

// TypeSecurityTaint is the entry type for one issue #102 taint event:
// attacker-controllable web content (a fetch_url / web_search result,
// detected by its untrusted-content framing) entered a turn, and — once a
// Risky command is gated under the taint — the follow-up event that says
// the raised bar actually engaged. The receipt is telemetry for the
// security behavior it accompanies: a taint raises the approval bar for
// Risky shell commands for the rest of the turn, so a reader can answer
// "how often does web content reach a turn, and how often does such a turn
// then want an approval-gated command" without the journal ever carrying
// the content itself. Written to the project-scope security class dir
// (.cortex/journal/security/) — see cmd/cortex/untrusted_journal.go's
// journalUntrustedContent for the write path and its best-effort posture.
const TypeSecurityTaint = "security.taint"

// TaintSourceWebFetch and TaintSourceWebSearch name the two delivery paths
// a taint can arrive by — the tools whose results carry the untrusted-
// content framing (internal/tools, issue #102). An unknown tool name is
// allowed through (a future web-path tool taints fail-closed like today's
// pair), but the known set is pinned so a typo'd source can't silently
// pollute the stream.
const (
	TaintSourceWebFetch  = "fetch_url"
	TaintSourceWebSearch = "web_search"
)

// SecurityTaintPayload is one taint event. Minimal by design: the tool that
// delivered the framed result (Source), why the turn is tainted (Reason),
// and whether a Risky command was later gated under the taint (RiskyGated).
// The content itself is never journalled — the receipt proves the class of
// event happened, not what the page said.
type SecurityTaintPayload struct {
	// Source is the tool whose observation carried the framing
	// ("fetch_url" or "web_search"; any non-empty name is accepted, see
	// TaintSourceWebFetch / TaintSourceWebSearch).
	Source string `json:"source"`
	// Reason is why the turn was tainted — the framing the source's result
	// carried ("untrusted-content marker in observation"). Kept so a reader
	// never has to re-derive the cause from the tool name alone.
	Reason string `json:"reason,omitempty"`
	// TurnNo is the in-flight turn's 1-based ordinal at record time (0 is
	// rejected: outside a turn there is nothing to taint and no receipt is
	// written).
	TurnNo int `json:"turn_no"`
	// RiskyGated marks the follow-up event: a Risky command was gated
	// (prompted or blocked) while the turn was tainted. The first arrival
	// of each source writes a receipt with RiskyGated=false; the first
	// Risky gate under the taint writes one more with RiskyGated=true.
	RiskyGated bool `json:"risky_gated,omitempty"`
}

// NewSecurityTaintEntry builds a journal entry for one taint event.
func NewSecurityTaintEntry(p SecurityTaintPayload) (*Entry, error) {
	if p.Source == "" {
		return nil, fmt.Errorf("journal: security.taint requires Source")
	}
	if p.TurnNo <= 0 {
		return nil, fmt.Errorf("journal: security.taint requires a positive TurnNo, got %d", p.TurnNo)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal security.taint: %w", err)
	}
	return &Entry{Type: TypeSecurityTaint, V: 1, Payload: data}, nil
}

// ParseSecurityTaint decodes a security.taint entry's payload.
func ParseSecurityTaint(e *Entry) (*SecurityTaintPayload, error) {
	if e.Type != TypeSecurityTaint {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeSecurityTaint)
	}
	var p SecurityTaintPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse security.taint: %w", err)
	}
	return &p, nil
}
