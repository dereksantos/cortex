// Package dag — salience-cap self-calibration.
//
// Phase 3 Slice 5: the per-tool-call output-token cap that the REPL
// pins via SalienceCapForClass starts as a static guess (200 / 500 /
// 1500 across the small / medium / large context classes). The
// dag_traces.jsonl rolling window records what each attend.compress
// invocation actually kept — and whether the compressor fell back to
// the truncate-stub because the budget was too tight. This file
// closes the loop: read those rows, fit a per-intent suggested cap
// from observed kept_tokens + fallback rate, and persist the result
// to .cortex/calibration/salience.json. The REPL loads the snapshot
// at session start and overrides SalienceCapForClass with the
// calibrated values.
//
// Bounded by Auditability (per docs/eval-strategy.md and
// docs/salience-budgets.md): every calibrated cap carries
// (sample_count, p50_kept_tokens, fallback_rate, computed_at) so a
// surprising override can be traced back to its inputs.
package dag

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultSalienceCalibrationPath is the on-disk artifact location
// relative to .cortex/calibration/. Separate from
// .cortex/db/op_cost_hints.json on purpose — the salience calibration
// is consumed by pkg/llm (SalienceCapForClass) while the cost-hint
// calibration is consumed by the DAG executor; co-locating them
// would conflate two independent calibration cycles.
const DefaultSalienceCalibrationPath = ".cortex/calibration/salience.json"

// DefaultSalienceWindow is the rolling-window row count for salience
// calibration. Larger than DefaultCalibrationWindow (100) because
// attend.compress samples are sparser than typical op samples — a
// long session may only emit a few dozen compression rows.
const DefaultSalienceWindow = 500

// SalienceCalibration is the persisted shape written to
// .cortex/calibration/salience.json. Loaded by the REPL via
// LoadSalienceCalibration and applied as an override over
// SalienceCapForClass.
type SalienceCalibration struct {
	SchemaVersion string                    `json:"schema_version"`
	SourcePath    string                    `json:"source_path"`
	WindowSize    int                       `json:"window_size"`
	CalibratedAt  time.Time                 `json:"calibrated_at"`
	Samples       int                       `json:"samples"`
	PerIntent     map[string]SalienceCapFit `json:"per_intent"`
	GlobalCap     int                       `json:"global_cap_tokens"`
}

// SalienceCapFit is one intent's calibrated cap with audit fields.
//
// SuggestedCap is the value the REPL should apply when this intent
// is in play. It is computed as p90(kept_tokens) bumped up to leave
// headroom; for high-fallback intents (the compressor truncated more
// often than not under the original budget), the cap is bumped
// further so the next session has enough room.
//
// P50KeptTokens and FallbackRate are not consumed by the REPL — they
// exist so the operator can audit "why did the cap move?".
type SalienceCapFit struct {
	SuggestedCap  int     `json:"cap_tokens"`
	Samples       int     `json:"samples"`
	P50KeptTokens int     `json:"p50_kept_tokens"`
	P90KeptTokens int     `json:"p90_kept_tokens"`
	FallbackRate  float64 `json:"fallback_rate"`
}

// SalienceCalibrateOptions configures one calibration pass.
type SalienceCalibrateOptions struct {
	TracePath    string // dag_traces.jsonl source; empty = .cortex/db/dag_traces.jsonl
	SnapshotPath string // output JSON; empty = DefaultSalienceCalibrationPath
	WindowSize   int    // rolling window of recent rows; 0 = DefaultSalienceWindow

	// HeadroomFactor multiplies p90(kept_tokens) before clamping to a
	// minimum floor. 1.20 is a generous default — the calibration
	// loop's job is to find a cap that fits real usage, not to be
	// aggressive. Zero or negative falls back to 1.20.
	HeadroomFactor float64

	// MinCap is the floor any calibrated cap must clear so the
	// suggested value can't dip below operational viability. Zero
	// falls back to 60 (matches the act.list_dir floor in
	// docs/salience-budgets.md "Default allocation policy").
	MinCap int
}

func (o *SalienceCalibrateOptions) normalize() {
	if o.TracePath == "" {
		o.TracePath = ".cortex/db/dag_traces.jsonl"
	}
	if o.SnapshotPath == "" {
		o.SnapshotPath = DefaultSalienceCalibrationPath
	}
	if o.WindowSize <= 0 {
		o.WindowSize = DefaultSalienceWindow
	}
	if o.HeadroomFactor <= 0 {
		o.HeadroomFactor = 1.20
	}
	if o.MinCap <= 0 {
		o.MinCap = 60
	}
}

// CalibrateSalience reads the trace file's rolling window, isolates
// attend.compress rows, fits a per-intent SuggestedCap, and persists
// the snapshot. Returns the snapshot (empty if no compression rows).
//
// Missing trace file is not an error (cold start). Errors writing
// the snapshot are returned so a caller's tooling can distinguish
// "calibration ran but didn't persist" from "calibration failed".
func CalibrateSalience(opts SalienceCalibrateOptions) (*SalienceCalibration, error) {
	opts.normalize()

	rows, err := readSalienceRowsTail(opts.TracePath, opts.WindowSize)
	if err != nil {
		return nil, err
	}
	snap := &SalienceCalibration{
		SchemaVersion: "1",
		SourcePath:    opts.TracePath,
		WindowSize:    opts.WindowSize,
		CalibratedAt:  time.Now(),
		PerIntent:     map[string]SalienceCapFit{},
	}
	if len(rows) == 0 {
		// Empty/cold trace — write an empty snapshot so downstream
		// callers can tell "calibration ran, no data" from "no
		// calibration file at all".
		if err := writeSalienceSnapshot(opts.SnapshotPath, snap); err != nil {
			return snap, err
		}
		return snap, nil
	}

	groups := map[string][]salienceRow{}
	for _, r := range rows {
		if r.Intent == "" {
			groups["_unset"] = append(groups["_unset"], r)
			continue
		}
		groups[r.Intent] = append(groups[r.Intent], r)
	}

	var allKept []int
	totalSamples := 0
	for intent, rs := range groups {
		kept := make([]int, 0, len(rs))
		fallbacks := 0
		for _, r := range rs {
			kept = append(kept, r.KeptTokens)
			if r.Fallback {
				fallbacks++
			}
		}
		allKept = append(allKept, kept...)
		fit := SalienceCapFit{
			Samples:       len(rs),
			P50KeptTokens: percentile(kept, 50),
			P90KeptTokens: percentile(kept, 90),
			FallbackRate:  float64(fallbacks) / float64(len(rs)),
		}
		// Suggested cap = p90(kept) × headroom. When the fallback rate
		// is high, the cap was too tight — bump further. (>= 50%
		// fallback means more than half the compressions had to stub
		// out; the operator clearly needs a wider budget than the row
		// kept_tokens suggest.)
		base := math.Ceil(float64(fit.P90KeptTokens) * opts.HeadroomFactor)
		if fit.FallbackRate >= 0.50 {
			base *= 1.50
		}
		fit.SuggestedCap = int(base)
		if fit.SuggestedCap < opts.MinCap {
			fit.SuggestedCap = opts.MinCap
		}
		snap.PerIntent[intent] = fit
		totalSamples += len(rs)
	}
	snap.Samples = totalSamples

	// GlobalCap is a single class-agnostic override the REPL can pin
	// when it doesn't have a per-intent hint yet. p90 of every row's
	// kept_tokens, bumped by the headroom factor, with the same
	// floor. Provides a single knob for the "I just want one number"
	// case without forcing the REPL to enumerate intents.
	if len(allKept) > 0 {
		g := math.Ceil(float64(percentile(allKept, 90)) * opts.HeadroomFactor)
		if int(g) < opts.MinCap {
			g = float64(opts.MinCap)
		}
		snap.GlobalCap = int(g)
	}

	if err := writeSalienceSnapshot(opts.SnapshotPath, snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// LoadSalienceCalibration reads a persisted snapshot from disk.
// Missing-file is not an error — returns (nil, nil) so the caller
// can fall back to the static SalienceCapForClass defaults.
func LoadSalienceCalibration(path string) (*SalienceCalibration, error) {
	if path == "" {
		path = DefaultSalienceCalibrationPath
	}
	bb, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var snap SalienceCalibration
	if err := json.Unmarshal(bb, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &snap, nil
}

// salienceRow is the minimal subset of a dag_traces.jsonl row the
// salience calibration needs. Mirrors the dagtrace.Row shape without
// importing the package (which would create a cycle).
type salienceRow struct {
	QualifiedName string `json:"qualified_name"`
	Intent        string `json:"salience_intent"`
	OK            bool   `json:"ok"`
	// kept_tokens + fallback live under the row's nested `out` map.
	// We re-extract them in readSalienceRowsTail.
	KeptTokens int
	Fallback   bool
}

// readSalienceRowsTail reads dag_traces.jsonl, keeps only the last
// `window` attend.compress rows, and pulls kept_tokens + fallback
// out of the row's nested out map. Streams line-by-line.
func readSalienceRowsTail(path string, window int) ([]salienceRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.UseNumber()
	tail := make([]salienceRow, 0, window)
	for {
		var raw struct {
			QualifiedName string         `json:"qualified_name"`
			Intent        string         `json:"salience_intent"`
			OK            bool           `json:"ok"`
			Out           map[string]any `json:"out"`
		}
		if err := dec.Decode(&raw); err != nil {
			// EOF or a corrupt line — stop. Returning what we have so
			// far means a partial trace still produces a useful
			// calibration.
			break
		}
		if raw.QualifiedName != "attend.compress" {
			continue
		}
		if !raw.OK {
			continue
		}
		r := salienceRow{
			QualifiedName: raw.QualifiedName,
			Intent:        raw.Intent,
			OK:            raw.OK,
		}
		if raw.Out != nil {
			if v, ok := raw.Out["kept_tokens"]; ok {
				r.KeptTokens = asInt(v)
			}
			if v, ok := raw.Out["fallback"]; ok {
				if b, ok := v.(bool); ok {
					r.Fallback = b
				}
			}
		}
		if len(tail) == window {
			copy(tail, tail[1:])
			tail = tail[:window-1]
		}
		tail = append(tail, r)
	}
	return tail, nil
}

// asInt coerces a json.Number / float64 / int into an int, returning
// 0 on anything unexpected so a single corrupt row doesn't poison
// the whole calibration.
func asInt(v any) int {
	switch x := v.(type) {
	case json.Number:
		i, err := x.Int64()
		if err == nil {
			return int(i)
		}
		f, err := x.Float64()
		if err == nil {
			return int(f)
		}
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

// writeSalienceSnapshot persists the calibration artifact. Atomic via
// write-to-tmp + rename so a concurrent reader never sees a partial
// file. Mirrors writeSnapshot's contract.
func writeSalienceSnapshot(path string, snap *SalienceCalibration) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	bb, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bb, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// percentile returns the nth percentile of a slice of ints
// (n in [0, 100]). Empty slice → 0. Uses the nearest-rank method —
// no interpolation, no float64 detour for budget axes. Same
// rationale as p50 in calibrate.go.
func percentile(vals []int, n int) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	sort.Ints(sorted)
	if n <= 0 {
		return sorted[0]
	}
	if n >= 100 {
		return sorted[len(sorted)-1]
	}
	// nearest-rank: ceil(n/100 * len)
	rank := int(math.Ceil(float64(n) / 100.0 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
