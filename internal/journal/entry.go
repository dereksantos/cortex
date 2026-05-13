// Package journal implements an append-only event log that serves as the
// canonical write side of Cortex's CQRS storage model. SQLite is treated
// as a derived, regeneratable projection of the journal — see docs/journal.md
// for the architectural rationale and the ten principles.
package journal

import (
	"encoding/json"
	"time"
)

// Offset is a monotonic position within a writer-class. Offsets begin at 1.
// A value of 0 means "unset" or "before the start of the journal."
type Offset int64

// Entry is the universal journal envelope. Every line in every segment has
// this shape; the Payload field holds class-specific content.
//
// Type is "<writer-class>.<kind>" — e.g. "capture.event", "dream.insight".
// V is the schema version for Type; unknown versions are forward-compat
// (log and skip) per principle 6 of docs/journal.md.
// Sources lists upstream offsets this entry derives from. Empty for
// capture.event and observation.*; required for derivations and feedback.
type Entry struct {
	Type    string          `json:"type"`
	V       int             `json:"v"`
	Offset  Offset          `json:"offset"`
	TS      time.Time       `json:"ts"`
	Sources []Offset        `json:"sources,omitempty"`
	Payload json.RawMessage `json:"payload"`
}
