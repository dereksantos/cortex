package dagtrace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// TestWriter_SalienceColumnsPlumbed pins the Phase-1 schema bump from
// docs/salience-budgets.md: when a trace entry carries a SalienceContract
// and an OutputTokens cost, the JSONL row must include the new columns.
// Older entries (no Salience, zero OutputTokens) must still round-trip
// without spurious noise — schemaVersion bump aside, the row stays
// minimal.
func TestWriter_SalienceColumnsPlumbed(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entry := dag.TraceEntry{
		NodeID:        "n5",
		QualifiedName: "attend.compress",
		OK:            true,
		CostConsumed:  dag.Cost{LatencyMS: 5, OutputTokens: 120},
		BudgetAfter:   dag.Budget{LatencyMS: 9000, Tokens: 800, Depth: 4, OutputTokens: 7880},
		WallStart:     time.Now(),
		WallEnd:       time.Now(),
		Salience:      &dag.SalienceContract{MaxOutputTokens: 120, Intent: "find TODOs"},
	}
	if err := w.Append("turn-1", entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	f, err := os.Open(filepath.Join(dir, "dag_traces.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("no row written")
	}

	var row Row
	if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row.SchemaVersion != "2" {
		t.Errorf("schema_version should be 2 after salience-budgets columns, got %q", row.SchemaVersion)
	}
	if row.CostOutputTok != 120 {
		t.Errorf("cost_output_tokens not plumbed: got %d", row.CostOutputTok)
	}
	if row.BudgetAfterOut != 7880 {
		t.Errorf("budget_after_output_tokens not plumbed: got %d", row.BudgetAfterOut)
	}
	if row.SalienceMaxOutTok != 120 || row.SalienceIntent != "find TODOs" {
		t.Errorf("salience columns not plumbed: %+v", row)
	}

	// Spot-check: a sibling Out-less row without Salience does not
	// emit the new columns at all (omitempty), so v1 readers stay
	// quiet.
	raw := scanner.Text()
	_ = raw
}

// TestWriter_LegacyEntryOmitsSalienceFields pins the omitempty
// behavior. Pre-salience-budgets entries (no Salience, no OutputTokens)
// must serialize without the new keys so analysis layers that haven't
// upgraded don't see noisy zero columns.
func TestWriter_LegacyEntryOmitsSalienceFields(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entry := dag.TraceEntry{
		NodeID:        "n1",
		QualifiedName: "sense.prompt",
		OK:            true,
		CostConsumed:  dag.Cost{LatencyMS: 5, Tokens: 0},
		BudgetAfter:   dag.Budget{LatencyMS: 99000, Tokens: 9990, Depth: 9},
		WallStart:     time.Now(),
		WallEnd:       time.Now(),
	}
	if err := w.Append("turn-1", entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "dag_traces.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, banned := range []string{"salience_max_output_tokens", "salience_intent", "cost_output_tokens", "budget_after_output_tokens"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("legacy row should omit %q (omitempty), got: %s", banned, raw)
		}
	}
}
