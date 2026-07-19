// Package loops implements the Phase 6 loop spec store (GOAL.md §3 P6,
// docs/cortex-web.md D4/D10/D11): a plain-JSON, rebuildable-from-nothing
// list of named recurring-run specs backed by loops.json under the
// resolved user home (internal/userhome). Machine-level state per D4 —
// run history lives in the journal, not here; loops.json stays pure spec.
package loops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dereksantos/cortex/internal/userhome"
)

// DefaultCadenceFloorMinutes is the immutable historical default (D11 —
// tuned down from the original 15-minute floor to 5 minutes). Referenced
// ONLY by cmd/cortex's Config.loopCadenceFloorMin() resolver fallback,
// never reassigned — see that method's sibling comments (config.go) for why
// a resolver's fallback must be an immutable const, not the mutable
// CadenceFloorMinutes var below (which a prior session/test in the same
// process may have already overridden).
const DefaultCadenceFloorMinutes = 5

// CadenceFloorMinutes is the LIVE minimum non-zero interval a loop may run
// on. An IntervalMinutes of 0 means manual-run-only, not a cadence, so it is
// exempt from the floor. cmd/cortex's runServeCLI sets this once from
// serve.loop_cadence_floor_min; unset, it stays DefaultCadenceFloorMinutes.
var CadenceFloorMinutes = DefaultCadenceFloorMinutes

// Spec is one loop definition (docs/cortex-web.md Phase 6): a named,
// project-scoped recurring headless run. Triggers are intervals-or-manual
// only in v1 (D10 — no cron parser); IntervalMinutes 0 means manual
// run-now only. MaxTurns/MaxTokens are the per-run budget caps (D11),
// enforced by the scheduler (M6.4), not validated here beyond presence.
// DisabledReason is the D11 self-pacing/strike-out tuning's terminal-state
// marker: set alongside Enabled=false when a DONE marker (see
// cmd/cortex/loop_run.go) or three consecutive failed runs auto-disable
// the loop, so the UI (and a human re-enabling it) can tell "I turned this
// off" from "the loop declared itself done" or "it kept failing" apart.
// Empty for a loop nobody/nothing has disabled, or one a human disabled by
// hand via the enable/disable toggle (which never sets this field).
type Spec struct {
	Name            string `json:"name"`
	Project         string `json:"project"`
	Prompt          string `json:"prompt"`
	IntervalMinutes int    `json:"interval_minutes,omitempty"`
	MaxTurns        int    `json:"max_turns,omitempty"`
	MaxTokens       int    `json:"max_tokens,omitempty"`
	Enabled         bool   `json:"enabled"`
	DisabledReason  string `json:"disabled_reason,omitempty"`
}

// ErrLoopNotFound is returned by Lookup and Remove when no loop with the
// given name is registered — including when loops.json does not exist
// yet (an empty store has no names).
var ErrLoopNotFound = errors.New("loop not registered")

// ErrCadenceTooLow is returned by Save when a spec's non-zero
// IntervalMinutes is below CadenceFloorMinutes (D11).
var ErrCadenceTooLow = errors.New("loop interval below the 5-minute cadence floor")

// Store is the CRUD seam over the loop spec store, mirroring
// internal/registry's Registry shape. FileStore is the file-backed
// implementation; tests may fake it.
type Store interface {
	List() ([]Spec, error)
	Lookup(name string) (Spec, error)
	Save(s Spec) error
	Remove(name string) error
}

// FileStore is the plain-JSON-file-backed Store implementation, rooted at
// loops.json under the resolved user home.
type FileStore struct {
	path string
}

// New constructs a FileStore backed by loops.json under the resolved user
// home directory (internal/userhome.Path) — redirect it in tests via
// $CORTEX_HOME.
func New() (*FileStore, error) {
	path, err := userhome.Path("loops.json")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve loops.json path: %w", err)
	}
	return &FileStore{path: path}, nil
}

// NewAt constructs a FileStore backed by an explicit file path, bypassing
// internal/userhome — for callers (and tests) that need a store outside
// the resolved user home.
func NewAt(path string) *FileStore {
	return &FileStore{path: path}
}

// List returns every registered loop, sorted by name. An empty or
// not-yet-created store returns an empty (nil) slice, not an error.
func (s *FileStore) List() ([]Spec, error) {
	specs, err := s.readAll()
	if err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// Lookup returns the loop registered under name, or ErrLoopNotFound if
// none exists (including when the store file doesn't exist yet).
func (s *FileStore) Lookup(name string) (Spec, error) {
	specs, err := s.readAll()
	if err != nil {
		return Spec{}, err
	}
	for _, spec := range specs {
		if spec.Name == name {
			return spec, nil
		}
	}
	return Spec{}, fmt.Errorf("%w: %s", ErrLoopNotFound, name)
}

// Save validates the spec (cadence floor), then adds it as a new loop, or
// overwrites the existing entry with the same name in place (update, not
// append). A spec that fails validation is never persisted.
func (s *FileStore) Save(spec Spec) error {
	if err := validate(spec); err != nil {
		return err
	}
	specs, err := s.readAll()
	if err != nil {
		return err
	}
	for i, existing := range specs {
		if existing.Name == spec.Name {
			specs[i] = spec
			return s.writeAll(specs)
		}
	}
	specs = append(specs, spec)
	return s.writeAll(specs)
}

// Remove deletes the loop registered under name, or returns
// ErrLoopNotFound if none exists.
func (s *FileStore) Remove(name string) error {
	specs, err := s.readAll()
	if err != nil {
		return err
	}
	for i, spec := range specs {
		if spec.Name == name {
			specs = append(specs[:i], specs[i+1:]...)
			return s.writeAll(specs)
		}
	}
	return fmt.Errorf("%w: %s", ErrLoopNotFound, name)
}

// validate checks a spec against the invariants Save must enforce before
// persisting. A zero IntervalMinutes means manual-run-only (D10) and is
// exempt from the cadence floor (D11).
func validate(spec Spec) error {
	if spec.IntervalMinutes != 0 && spec.IntervalMinutes < CadenceFloorMinutes {
		return fmt.Errorf("%w: %s requests %dm, floor is %dm", ErrCadenceTooLow, spec.Name, spec.IntervalMinutes, CadenceFloorMinutes)
	}
	return nil
}

// readAll loads the full loop spec list from disk. A missing or empty
// file is treated as an empty store, not an error.
func (s *FileStore) readAll() ([]Spec, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var specs []Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", s.path, err)
	}
	return specs, nil
}

// writeAll persists the full loop spec list to disk, creating the parent
// directory if needed.
func (s *FileStore) writeAll(specs []Spec) error {
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal loops: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", s.path, err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", s.path, err)
	}
	return nil
}
