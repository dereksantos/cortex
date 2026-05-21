package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// StateFileName is the basename of the persisted controller state.
// Lives under Config.ContextDir (typically .cortex/bootstrap_state.json).
const StateFileName = "bootstrap_state.json"

// StateVersion is the current on-disk schema version. Bump on breaking
// changes; the loader uses this to refuse incompatible older state.
const StateVersion = 1

// StatePath returns the canonical state file path for a given context
// directory.
func StatePath(contextDir string) string {
	return filepath.Join(contextDir, StateFileName)
}

// LoadState reads BootstrapState from path. Returns (nil, nil) if the
// file does not exist (signal: "never run before"). Other errors are
// returned verbatim so callers can surface a clear message.
func LoadState(path string) (*BootstrapState, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	var s BootstrapState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", path, err)
	}
	if s.Version > StateVersion {
		return nil, fmt.Errorf("state at %s has version %d > %d (newer than this binary)", path, s.Version, StateVersion)
	}
	if s.CoveredChunkIDs != nil {
		sort.Strings(s.CoveredChunkIDs)
	}
	return &s, nil
}

// SaveState writes s atomically (temp + rename) to path. CoveredChunkIDs
// is sorted before encoding so the file is diffable. Best effort: a
// failure is returned as an error and the caller decides what to do
// (controllers typically log + continue, since journal is the source
// of truth and storage is regeneratable).
func SaveState(path string, s *BootstrapState) error {
	if s == nil {
		return fmt.Errorf("save state: nil state")
	}
	s.Version = StateVersion
	if s.CoveredChunkIDs != nil {
		sort.Strings(s.CoveredChunkIDs)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save state mkdir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("save state encode: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("save state write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save state rename: %w", err)
	}
	return nil
}

// IsCompleted reports whether s indicates a finished bootstrap run.
// A nil state means "never run." A non-nil CompletedAt means "done."
func IsCompleted(s *BootstrapState) bool {
	return s != nil && s.CompletedAt != nil
}

// ShouldRunBootstrap returns (run, reason) for first-run detection.
// Callers invoke this on REPL startup or before `cortex bootstrap`
// to decide whether to spawn the controller.
//
// Reasons:
//   - "never_run"      — state file absent
//   - "unreadable"     — file present but unreadable (treat as never)
//   - "corrupt"        — file present but malformed JSON
//   - "incomplete"     — state present with no CompletedAt
//   - "" (empty)       — already completed; skip
func ShouldRunBootstrap(contextDir string) (run bool, reason string) {
	path := StatePath(contextDir)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true, "never_run"
	}
	if err != nil {
		return true, "unreadable"
	}
	var s BootstrapState
	if err := json.Unmarshal(b, &s); err != nil {
		return true, "corrupt"
	}
	if s.CompletedAt == nil {
		return true, "incomplete"
	}
	return false, ""
}
