package study

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// StateFileName is the basename of the persisted controller state.
// Lives under Config.ContextDir (typically .cortex/study_state.json).
const StateFileName = "study_state.json"

// legacyStateFileName is the pre-rename name (when the package was
// called "bootstrap"). LoadState transparently renames it to
// StateFileName on first read so an existing project doesn't lose its
// covered set after upgrading.
const legacyStateFileName = "bootstrap_state.json"

// StateVersion is the current on-disk schema version. Bump on breaking
// changes; the loader uses this to refuse incompatible older state.
//
// v2 introduces per-file coverage (CoveredFiles map keyed by relpath,
// stamped with file ContentHash) and the no_drift halt reason. v1
// state is auto-migrated by the controller on the first v2 run via
// migrateV1Coverage.
const StateVersion = 2

// StatePath returns the canonical state file path for a given context
// directory.
func StatePath(contextDir string) string {
	return filepath.Join(contextDir, StateFileName)
}

// LoadState reads State from path. Returns (nil, nil) if the
// file does not exist (signal: "never run before"). Other errors are
// returned verbatim so callers can surface a clear message.
//
// If the canonical study_state.json is missing but the legacy
// bootstrap_state.json exists in the same directory, it's renamed
// in-place before reading — a one-shot upgrade for projects that
// studied under the old "bootstrap" name.
func LoadState(path string) (*State, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		legacy := filepath.Join(filepath.Dir(path), legacyStateFileName)
		if _, lerr := os.Stat(legacy); lerr == nil {
			_ = os.Rename(legacy, path)
		}
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", path, err)
	}
	if s.Version > StateVersion {
		return nil, fmt.Errorf("state at %s has version %d > %d (newer than this binary)", path, s.Version, StateVersion)
	}
	if s.CoveredChunkIDs != nil {
		sort.Strings(s.CoveredChunkIDs)
	}
	for rel, fc := range s.CoveredFiles {
		if len(fc.ChunkIDs) > 0 {
			sort.Strings(fc.ChunkIDs)
			s.CoveredFiles[rel] = fc
		}
	}
	return &s, nil
}

// SaveState writes s atomically (temp + rename) to path. Per-file
// ChunkIDs are sorted before encoding so the file is diffable.
// CoveredChunkIDs (legacy flat list) is left nil on v2 — the per-file
// map is canonical; jq users walk CoveredFiles[*].chunk_ids.
//
// Best effort: a failure is returned as an error and the caller decides
// what to do (controllers typically log + continue, since the journal
// is the source of truth and storage is regeneratable from it).
func SaveState(path string, s *State) error {
	if s == nil {
		return fmt.Errorf("save state: nil state")
	}
	s.Version = StateVersion
	// v2: drop the legacy flat list; per-file map is canonical.
	s.CoveredChunkIDs = nil
	for rel, fc := range s.CoveredFiles {
		if len(fc.ChunkIDs) > 0 {
			sort.Strings(fc.ChunkIDs)
			s.CoveredFiles[rel] = fc
		}
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

// IsCompleted reports whether s has ever hit target coverage. A nil
// state means "never run." A non-nil CompletedAt means "we hit target
// at least once" — but this is no longer the gate for re-running. The
// gate is drift: callers consult the current snapshot's coverage vs
// target, not a one-shot completion bit.
func IsCompleted(s *State) bool {
	return s != nil && s.CompletedAt != nil
}

// ShouldRun returns (run, reason) — a coarse first-pass hint
// that does NOT require a boundary scan. Use it for the cheap REPL
// startup check ("should we even spawn the controller?"). The
// controller itself does the precise drift check inside Run() and
// short-circuits with reason="no_drift" when there's nothing to do.
//
// Reasons:
//   - "never_run"      — state file absent
//   - "unreadable"     — file present but unreadable (treat as never)
//   - "corrupt"        — file present but malformed JSON
//   - "incomplete"     — state present, never hit target — keep going
//   - "drift_possible" — state completed but project may have drifted
//     since; the controller will short-circuit if no drift in fact
func ShouldRun(contextDir string) (run bool, reason string) {
	path := StatePath(contextDir)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true, "never_run"
	}
	if err != nil {
		return true, "unreadable"
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return true, "corrupt"
	}
	if s.CompletedAt == nil {
		return true, "incomplete"
	}
	// Previously hit target, but the project may have drifted. Always
	// spawn — the controller short-circuits cheaply if nothing changed.
	return true, "drift_possible"
}

// DeficitSummary is the cheap, scan-free view used by surfaces like
// the REPL boundaries line and `cortex status`: "what does the state
// file say about prior coverage, and when did it last hit target?"
// Drift since then is unknown without a boundary scan.
type DeficitSummary struct {
	HasState        bool       // false ⇒ never run
	LastCompletedAt *time.Time // last time we hit target (nil if never)
	LastCoveredEff  float64    // covered_eff_lines / eff_total_lines at last save
	LastCoveredFile float64    // file count fraction at last save
	CoveredFileN    int        // files in the persisted CoveredFiles map
	InsightsEmitted int
	LastHalt        string
	// Absolute size signals (zero when HasState is false). Surfaced so
	// callers reasoning about scope / budget have the raw denominators
	// alongside the coverage ratios — a 600-file project and a 6-file
	// project both show "100% covered" but warrant very different
	// budgets for an audit-class prompt. See sense.estimate_scope.
	TotalFiles    int
	EffTotalLines int
}

// WipeCoverage clears the per-file coverage map from the on-disk
// state file. Used by `cortex study --force` / `cortex study
// --force` when the user wants every file re-extracted (e.g., after
// changing the prompt or the extract op). The state file itself is
// preserved — only CoveredFiles + CoveredChunkIDs + CoveredEffLines +
// CoveredFileN + CompletedAt are reset.
//
// Returns nil if there's no state file (nothing to wipe) — a no-op.
func WipeCoverage(contextDir string) error {
	path := StatePath(contextDir)
	s, err := LoadState(path)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.CoveredFiles = nil
	s.CoveredChunkIDs = nil
	s.CoveredEffLines = 0
	s.CoveredFileN = 0
	s.CompletedAt = nil
	s.Halted = ""
	return SaveState(path, s)
}

// LoadDeficitSummary reads the persisted state and returns a snapshot
// for read-only callers. Does not perform a boundary scan, so the
// reported numbers are last-save values — a file edited after the
// last study won't be reflected here. The controller's pre-iteration
// pruneDrift step is what produces the up-to-date picture.
func LoadDeficitSummary(contextDir string) DeficitSummary {
	s, err := LoadState(StatePath(contextDir))
	if err != nil || s == nil {
		return DeficitSummary{}
	}
	d := DeficitSummary{
		HasState:        true,
		LastCompletedAt: s.CompletedAt,
		CoveredFileN:    s.CoveredFileN,
		InsightsEmitted: s.InsightsEmitted,
		LastHalt:        s.Halted,
		TotalFiles:      s.TotalFiles,
		EffTotalLines:   s.EffTotalLines,
	}
	if s.EffTotalLines > 0 {
		d.LastCoveredEff = float64(s.CoveredEffLines) / float64(s.EffTotalLines)
	}
	if s.TotalFiles > 0 {
		d.LastCoveredFile = float64(s.CoveredFileN) / float64(s.TotalFiles)
	}
	return d
}
