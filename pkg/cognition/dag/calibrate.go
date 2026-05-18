// Package dag — per-op cost-hint self-calibration.
//
// The Executor's pre-spawn CanAfford check uses each op's registered
// Cost as the gate. Cost values start as guesses (or measurements
// from one calibration run); real per-op latency + tokens drift as
// prompts change, models change, or workloads change. Calibration
// closes the loop: read the rolling window of dag_traces.jsonl rows,
// compute per-op p50 latency + p50 tokens from successful calls, and
// update the registry's Cost field in place.
//
// Bound by Auditability — the calibration source data (window file,
// sample count per op, observed timestamps) is persisted alongside
// the calibrated costs so a surprising new Cost value can be traced
// back to the input rows.
//
// Invoked at Executor construction time (cheap; in-memory read of
// the persisted hints) and on demand via Calibrate() (re-reads the
// trace file). Real recalibration is a CLI command in Stage 5 once
// the calibrate cycle runs on a schedule.
package dag

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// OpCostHint is the persisted shape for one calibrated op. Includes
// audit fields so a future surprising value can be traced back to
// its inputs.
type OpCostHint struct {
	QualifiedName string    `json:"qualified_name"`
	LatencyMS     int       `json:"latency_ms"`
	Tokens        int       `json:"tokens"`
	Samples       int       `json:"samples"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
	CalibratedAt  time.Time `json:"calibrated_at"`
}

// CalibrationSnapshot is the full on-disk artifact written to
// .cortex/db/op_cost_hints.json. Includes the source path + window
// so the caller can audit what fed the calibration.
type CalibrationSnapshot struct {
	SchemaVersion string                `json:"schema_version"`
	SourcePath    string                `json:"source_path"`
	WindowSize    int                   `json:"window_size"`
	CalibratedAt  time.Time             `json:"calibrated_at"`
	Hints         map[string]OpCostHint `json:"hints"`
}

// DefaultCalibrationWindow is the rolling-window row count over which
// per-op p50s are computed. 100 trades responsiveness (small windows
// react quickly) against noise (small windows are noisy).
const DefaultCalibrationWindow = 100

// DefaultCalibrationSnapshotPath is the on-disk artifact location
// relative to .cortex/db/.
const DefaultCalibrationSnapshotPath = ".cortex/db/op_cost_hints.json"

// CalibrateOptions configures one calibration pass.
type CalibrateOptions struct {
	TracePath    string // dag_traces.jsonl source; empty = .cortex/db/dag_traces.jsonl
	SnapshotPath string // output JSON; empty = .cortex/db/op_cost_hints.json
	WindowSize   int    // rolling window of recent rows; 0 = DefaultCalibrationWindow
}

func (o *CalibrateOptions) normalize() {
	if o.TracePath == "" {
		o.TracePath = ".cortex/db/dag_traces.jsonl"
	}
	if o.SnapshotPath == "" {
		o.SnapshotPath = DefaultCalibrationSnapshotPath
	}
	if o.WindowSize <= 0 {
		o.WindowSize = DefaultCalibrationWindow
	}
}

// Calibrate reads the trace file's rolling window, computes per-op
// p50 latency + tokens from successful (ok=true) rows, applies the
// resulting hints to the registry, and persists a snapshot. Returns
// the snapshot (or empty if no rows).
//
// Errors reading the trace file return; missing-file is not an
// error (cold start — no traces yet). Errors writing the snapshot
// also return (so callers know the in-memory registry was updated
// but the on-disk hints did not persist).
func Calibrate(reg *Registry, opts CalibrateOptions) (*CalibrationSnapshot, error) {
	opts.normalize()

	rows, err := readTraceRowsTail(opts.TracePath, opts.WindowSize)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &CalibrationSnapshot{
			SchemaVersion: "1",
			SourcePath:    opts.TracePath,
			WindowSize:    opts.WindowSize,
			CalibratedAt:  time.Now(),
			Hints:         map[string]OpCostHint{},
		}, nil
	}

	hints := computeHints(rows)
	applyHints(reg, hints)

	snap := &CalibrationSnapshot{
		SchemaVersion: "1",
		SourcePath:    opts.TracePath,
		WindowSize:    opts.WindowSize,
		CalibratedAt:  time.Now(),
		Hints:         hints,
	}
	if err := writeSnapshot(opts.SnapshotPath, snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// LoadCalibrationSnapshot reads a persisted snapshot from disk and
// applies its hints to the registry. Used at Executor construction
// to warm the registry from the prior process's calibration without
// re-reading the trace file. Missing-file is not an error.
func LoadCalibrationSnapshot(reg *Registry, path string) (*CalibrationSnapshot, error) {
	if path == "" {
		path = DefaultCalibrationSnapshotPath
	}
	bb, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot %s: %w", path, err)
	}
	var snap CalibrationSnapshot
	if err := json.Unmarshal(bb, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot %s: %w", path, err)
	}
	applyHints(reg, snap.Hints)
	return &snap, nil
}

// traceRow is the parsed shape of one dag_traces.jsonl line, kept
// minimal — only the fields calibration needs. (Mirrors the Row
// shape in internal/eval/dagtrace.Writer without importing it, which
// would create a cycle.)
type traceRow struct {
	QualifiedName string `json:"qualified_name"`
	OK            bool   `json:"ok"`
	CostLatencyMS int    `json:"cost_latency_ms"`
	CostTokens    int    `json:"cost_tokens"`
	WallStartUnix int64  `json:"wall_start_unix_ns"`
}

// readTraceRowsTail returns the last `window` rows from the trace
// file. Streams line-by-line + keeps only the tail to avoid loading
// the whole file when the window is small.
func readTraceRowsTail(path string, window int) ([]traceRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	tail := make([]traceRow, 0, window)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var r traceRow
		if err := json.Unmarshal(raw, &r); err != nil {
			continue // tolerate corrupt lines
		}
		if len(tail) == window {
			copy(tail, tail[1:])
			tail = tail[:window-1]
		}
		tail = append(tail, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return tail, nil
}

// computeHints groups rows by qualified_name, filters to OK=true, and
// emits per-op p50 latency + tokens with sample count and window.
func computeHints(rows []traceRow) map[string]OpCostHint {
	groups := map[string][]traceRow{}
	for _, r := range rows {
		if !r.OK {
			continue
		}
		groups[r.QualifiedName] = append(groups[r.QualifiedName], r)
	}

	hints := map[string]OpCostHint{}
	now := time.Now()
	for qname, rs := range groups {
		latencies := make([]int, 0, len(rs))
		tokens := make([]int, 0, len(rs))
		var windowStart, windowEnd time.Time
		for i, r := range rs {
			latencies = append(latencies, r.CostLatencyMS)
			tokens = append(tokens, r.CostTokens)
			ts := time.Unix(0, r.WallStartUnix)
			if i == 0 || ts.Before(windowStart) {
				windowStart = ts
			}
			if i == 0 || ts.After(windowEnd) {
				windowEnd = ts
			}
		}
		hints[qname] = OpCostHint{
			QualifiedName: qname,
			LatencyMS:     p50(latencies),
			Tokens:        p50(tokens),
			Samples:       len(rs),
			WindowStart:   windowStart,
			WindowEnd:     windowEnd,
			CalibratedAt:  now,
		}
	}
	return hints
}

// applyHints updates the registry's NodeSpec.Cost field for each
// known op. Unregistered ops in hints are skipped (they may come
// from old traces of since-removed ops); registered ops not in
// hints are left at their current Cost (no observation, no change).
func applyHints(reg *Registry, hints map[string]OpCostHint) {
	if reg == nil {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for qname, h := range hints {
		spec, ok := reg.specs[qname]
		if !ok {
			continue
		}
		spec.Cost = Cost{LatencyMS: h.LatencyMS, Tokens: h.Tokens}
		reg.specs[qname] = spec
	}
}

// writeSnapshot persists the calibration artifact. Atomic via
// write-to-tmp + rename so a concurrent reader never sees a partial
// file.
func writeSnapshot(path string, snap *CalibrationSnapshot) error {
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

// p50 returns the median of a slice of ints. Empty slice → 0.
// Uses the lower-median for even-length slices (deterministic; the
// difference vs. averaging the two middle values is at most one
// unit and not worth a float64 detour for budget axes).
func p50(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}
