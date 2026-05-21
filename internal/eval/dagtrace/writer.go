// Package dagtrace appends per-node TraceEntries from the DAG
// executor (pkg/cognition/dag) to a JSONL file for analysis.
//
// Output: .cortex/db/dag_traces.jsonl (one line per executed node).
// Each row includes turn_id, node_id, parent_node_id, qualified_name,
// ok, cost_consumed, budget_after — the minimal shape Phase 6's tree-
// shape analyses need.
//
// Note on the unified telemetry sink: per Phase 1 of the integration
// roadmap, the long-term plan is for ALL CLI telemetry to land in
// cell_results.jsonl. DAG nodes don't fit cleanly in the CellResult
// schema today (no Model/Harness/Provider for mock handlers), so they
// live in dag_traces.jsonl as a sibling sink. Unification under one
// schema is a deliberate follow-up — flagged in the dag-build-plan.md
// Stage 1 success criteria.
package dagtrace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// Row is one line in dag_traces.jsonl — the wire shape we commit to.
// Subset of TraceEntry; adds turn_id + timestamp + git ref for
// provenance + replay.
type Row struct {
	SchemaVersion   string         `json:"schema_version"`
	Timestamp       string         `json:"timestamp"`
	TurnID          string         `json:"turn_id"`
	NodeID          string         `json:"node_id"`
	ParentID        string         `json:"parent_node_id,omitempty"`
	QualifiedName   string         `json:"qualified_name"`
	OK              bool           `json:"ok"`
	ErrorCode       string         `json:"error_code,omitempty"`
	ErrorMessage    string         `json:"error_message,omitempty"`
	CostLatencyMS   int            `json:"cost_latency_ms"`
	CostTokens      int            `json:"cost_tokens"`
	CostOutputTok   int            `json:"cost_output_tokens,omitempty"`
	BudgetAfterLat  int            `json:"budget_after_latency_ms"`
	BudgetAfterTok  int            `json:"budget_after_tokens"`
	BudgetAfterDep  int            `json:"budget_after_depth"`
	BudgetAfterOut  int            `json:"budget_after_output_tokens,omitempty"`
	SpawnedChildren []string       `json:"spawned_children,omitempty"`
	WallStartUnix   int64          `json:"wall_start_unix_ns"`
	WallEndUnix     int64          `json:"wall_end_unix_ns"`
	Out             map[string]any `json:"out,omitempty"`

	// Salience columns, populated when the parent attached a contract
	// at spawn time (docs/salience-budgets.md). The calibration loop
	// fits per-intent budget-quality curves off these.
	SalienceMaxOutTok int    `json:"salience_max_output_tokens,omitempty"`
	SalienceIntent    string `json:"salience_intent,omitempty"`
}

// schemaVersion bumped to "2" with the addition of cost_output_tokens,
// budget_after_output_tokens, salience_max_output_tokens, and
// salience_intent columns. Phase-1 schema change per
// docs/salience-budgets.md — all new fields are omitempty so a v1
// reader sees them as missing rather than as a parse failure.
const schemaVersion = "2"

// Writer appends DAG trace rows to a JSONL file. Construct one per
// process via NewWriter; mounts under .cortex/db/dag_traces.jsonl
// relative to cwd (or whatever dir New is told).
type Writer struct {
	mu   sync.Mutex
	path string
}

// NewWriter constructs a Writer that appends to
// <dir>/dag_traces.jsonl. dir is created if missing. Empty dir
// defaults to .cortex/db/ relative to cwd.
func NewWriter(dir string) (*Writer, error) {
	if dir == "" {
		dir = ".cortex/db"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return &Writer{path: filepath.Join(dir, "dag_traces.jsonl")}, nil
}

// Append appends one row for the given TraceEntry. Safe for concurrent
// use (mutex-guarded). Errors writing return — caller decides whether
// to abort.
func (w *Writer) Append(turnID string, e dag.TraceEntry) error {
	row := Row{
		SchemaVersion:   schemaVersion,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		TurnID:          turnID,
		NodeID:          e.NodeID,
		ParentID:        e.ParentID,
		QualifiedName:   e.QualifiedName,
		OK:              e.OK,
		ErrorCode:       e.ErrorCode,
		ErrorMessage:    e.ErrorMessage,
		CostLatencyMS:   e.CostConsumed.LatencyMS,
		CostTokens:      e.CostConsumed.Tokens,
		CostOutputTok:   e.CostConsumed.OutputTokens,
		BudgetAfterLat:  e.BudgetAfter.LatencyMS,
		BudgetAfterTok:  e.BudgetAfter.Tokens,
		BudgetAfterDep:  e.BudgetAfter.Depth,
		BudgetAfterOut:  e.BudgetAfter.OutputTokens,
		SpawnedChildren: e.SpawnedChildren,
		WallStartUnix:   e.WallStart.UnixNano(),
		WallEndUnix:     e.WallEnd.UnixNano(),
		Out:             e.Out,
	}
	if e.Salience != nil {
		row.SalienceMaxOutTok = e.Salience.MaxOutputTokens
		row.SalienceIntent = e.Salience.Intent
	}
	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("marshal row: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", w.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", w.path, err)
	}
	return nil
}

// Callback returns a dag.TraceCallback bound to this writer and the
// given turn_id. Wire into dag.NewExecutor as the trace hook.
func (w *Writer) Callback(turnID string) dag.TraceCallback {
	return func(e dag.TraceEntry) {
		if err := w.Append(turnID, e); err != nil {
			// Don't fail the executor on telemetry write errors —
			// surface to stderr and continue.
			fmt.Fprintf(os.Stderr, "[dagtrace] append failed: %v\n", err)
		}
	}
}

// Path returns the absolute or cwd-relative path the writer appends to.
func (w *Writer) Path() string { return w.path }
