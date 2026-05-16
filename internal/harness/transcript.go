package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Transcript writes one JSONL line per loop event to
// <workdir>/.cortex/journal/coding/<runID>.jsonl. It is a side-effect
// log, not a writer-class hooked into the journal indexer — coding
// runs are derivations of capture events plus a model's behavior, and
// the indexer doesn't need to project them into SQLite. Keeping the
// log workdir-local also matches the per-eval store invariant: nothing
// about a single coding run leaks to ~/.cortex.
//
// The file is opened on the first Write and held open for the
// lifetime of the Transcript. Close() flushes and releases.
//
// Concurrency: one writer per Transcript. The Loop is single-threaded
// per session, so the mutex only guards against accidental misuse.
type Transcript struct {
	path string

	mu sync.Mutex
	f  *os.File
}

// NewTranscript prepares a transcript file but does not open it yet
// (open happens on first Write). workdir must be absolute; runID is
// any unique string the caller chose (typically a ULID or
// timestamp+nonce).
func NewTranscript(workdir, runID string) (*Transcript, error) {
	if !filepath.IsAbs(workdir) {
		return nil, fmt.Errorf("%w: %q", errWorkdirNotAbsolute, workdir)
	}
	if runID == "" {
		return nil, fmt.Errorf("transcript: runID must not be empty")
	}
	dir := filepath.Join(workdir, ".cortex", "journal", "coding")
	path := filepath.Join(dir, runID+".jsonl")
	return &Transcript{path: path}, nil
}

// Path returns the absolute path of the transcript file.
func (t *Transcript) Path() string { return t.path }

// WriteEntry appends one JSONL line. kind is the entry type
// ("coding.turn", "coding.tool_call", "coding.tool_result",
// "coding.final"); payload is anything JSON-marshalable.
func (t *Transcript) WriteEntry(kind string, payload any) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.f == nil {
		if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
			return fmt.Errorf("mkdir transcript dir: %w", err)
		}
		f, err := os.OpenFile(t.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open transcript: %w", err)
		}
		t.f = f
	}

	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	entry := struct {
		Type    string          `json:"type"`
		TS      time.Time       `json:"ts"`
		Payload json.RawMessage `json:"payload"`
	}{
		Type:    kind,
		TS:      time.Now().UTC(),
		Payload: rawPayload,
	}
	bb, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	bb = append(bb, '\n')
	if _, err := t.f.Write(bb); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// Close flushes (via Sync) and releases the file. Safe to call when
// no writes have happened.
func (t *Transcript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return nil
	}
	if err := t.f.Sync(); err != nil {
		_ = t.f.Close()
		t.f = nil
		return err
	}
	if err := t.f.Close(); err != nil {
		t.f = nil
		return err
	}
	t.f = nil
	return nil
}
