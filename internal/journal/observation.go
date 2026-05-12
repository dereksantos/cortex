package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Observation entry types. Observations are journal entries that record
// what Cortex *saw* from an external substrate (Claude transcripts, git
// history, user memory files) without copying the substrate itself —
// content-hash + URI is enough to detect change and trace provenance.
// See principle 3 in docs/journal.md.
const (
	TypeObservationClaudeTranscript = "observation.claude_transcript"
	TypeObservationGitCommit        = "observation.git_commit"
	TypeObservationMemoryFile       = "observation.memory_file"
)

// ObservationPayload is the shared shape for every observation entry. The
// `source_name` field names the producing DreamSource (or other observer);
// `uri` is a stable address for the substrate; `content_hash` is sha256
// of the substrate's full content at observation time; `size` is the
// substrate's byte size; `modified` is its last-modified timestamp if
// available.
//
// Idempotency: observation entries should be skipped on subsequent reads
// if (URI, content_hash) matches an already-recorded observation — the
// content has not changed, no new evidence to record. The projection
// (slice O3) enforces this via a SQLite UNIQUE constraint on (uri,
// content_hash).
type ObservationPayload struct {
	SourceName  string    `json:"source_name"`
	URI         string    `json:"uri"`
	ContentHash string    `json:"content_hash"`
	Size        int64     `json:"size,omitempty"`
	Modified    time.Time `json:"modified,omitempty"`
}

// HashContent returns the canonical sha256 hex digest for substrate bytes.
// Use this when building an ObservationPayload from substrate content.
func HashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// NewObservationEntry constructs a journal.Entry for a substrate observation.
// The caller supplies the entry type (TypeObservation*), the source name,
// a stable URI, and the substrate bytes (or a pre-computed hash). The entry
// is unsigned by offset and TS — those are assigned by Writer.Append.
func NewObservationEntry(typ, sourceName, uri string, content []byte, size int64, modified time.Time) (*Entry, error) {
	if !isObservationType(typ) {
		return nil, fmt.Errorf("journal: %q is not an observation entry type", typ)
	}
	payload := ObservationPayload{
		SourceName:  sourceName,
		URI:         uri,
		ContentHash: HashContent(content),
		Size:        size,
		Modified:    modified,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal observation payload: %w", err)
	}
	return &Entry{
		Type:    typ,
		V:       1,
		Payload: data,
	}, nil
}

// ParseObservation decodes the payload of an observation entry.
func ParseObservation(e *Entry) (*ObservationPayload, error) {
	if !isObservationType(e.Type) {
		return nil, fmt.Errorf("journal: entry type %q is not an observation", e.Type)
	}
	var p ObservationPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse observation payload: %w", err)
	}
	return &p, nil
}

func isObservationType(t string) bool {
	switch t {
	case TypeObservationClaudeTranscript,
		TypeObservationGitCommit,
		TypeObservationMemoryFile:
		return true
	}
	return false
}
