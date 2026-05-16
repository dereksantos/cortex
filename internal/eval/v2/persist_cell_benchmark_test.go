package eval

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
)

// TestPersistCell_BenchmarkFieldRoundTrip verifies the new optional
// Benchmark field round-trips through all three projections without
// requiring a SchemaVersion bump.
func TestPersistCell_BenchmarkFieldRoundTrip(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()
	r.Benchmark = "longmemeval"
	r.ScenarioID = "longmemeval/qa_017"

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	// SQLite: benchmark column populated.
	var benchmark string
	if err := p.db.QueryRow(
		`SELECT benchmark FROM cell_results WHERE run_id=?`, r.RunID,
	).Scan(&benchmark); err != nil {
		t.Fatalf("query benchmark: %v", err)
	}
	if benchmark != "longmemeval" {
		t.Errorf("sqlite benchmark=%q want %q", benchmark, "longmemeval")
	}

	// JSONL: benchmark field present in serialized form and round-trips.
	lines := readJSONL(t, p.cellResultsJSONLPath())
	if len(lines) != 1 {
		t.Fatalf("jsonl lines=%d", len(lines))
	}
	if !strings.Contains(lines[0], `"benchmark":"longmemeval"`) {
		t.Errorf("jsonl missing benchmark field: %s", lines[0])
	}
	var got CellResult
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&got, r) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, r)
	}

	// Journal: payload carries Benchmark.
	if err := p.journal.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	reader, err := journal.NewReader(p.journalDir)
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer reader.Close()
	entry, err := reader.Next()
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	payload, err := journal.ParseEvalCellResult(entry)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if payload.Benchmark != "longmemeval" {
		t.Errorf("journal payload Benchmark=%q want %q", payload.Benchmark, "longmemeval")
	}
}

// TestPersistCell_BenchmarkFieldOmittedWhenEmpty verifies that the
// existing non-benchmark write path is unaffected by the new field.
func TestPersistCell_BenchmarkFieldOmittedWhenEmpty(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()
	r.Benchmark = ""

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	lines := readJSONL(t, p.cellResultsJSONLPath())
	if len(lines) != 1 {
		t.Fatalf("jsonl lines=%d", len(lines))
	}
	if strings.Contains(lines[0], `"benchmark"`) {
		t.Errorf("empty benchmark should be omitted from JSON; got: %s", lines[0])
	}

	// SQLite stores NULL (not the empty string).
	var benchmark *string
	if err := p.db.QueryRow(
		`SELECT benchmark FROM cell_results WHERE run_id=?`, r.RunID,
	).Scan(&benchmark); err != nil {
		t.Fatalf("query benchmark: %v", err)
	}
	if benchmark != nil {
		t.Errorf("empty benchmark stored as %v; want NULL", *benchmark)
	}
}

// TestPersistCell_BenchmarkFieldSurvivesRebuild is the projection-
// rebuild contract for the new field: the Benchmark value must
// survive a journal-only rebuild.
func TestPersistCell_BenchmarkFieldSurvivesRebuild(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()
	r.Benchmark = "mteb"
	r.ScenarioID = "mteb/NFCorpus"

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	if err := p.journal.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	reader, err := journal.NewReader(p.journalDir)
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer reader.Close()
	entry, err := reader.Next()
	if err != nil && err != io.EOF {
		t.Fatalf("read entry: %v", err)
	}
	payload, err := journal.ParseEvalCellResult(entry)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Drop projections, replay from journal.
	if _, err := p.db.Exec("DELETE FROM cell_results"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := p.ProjectCellFromEntry(context.Background(), payload); err != nil {
		t.Fatalf("ProjectCellFromEntry: %v", err)
	}

	var benchmark string
	if err := p.db.QueryRow(
		`SELECT benchmark FROM cell_results WHERE run_id=?`, r.RunID,
	).Scan(&benchmark); err != nil {
		t.Fatalf("query after rebuild: %v", err)
	}
	if benchmark != "mteb" {
		t.Errorf("rebuilt benchmark=%q want %q", benchmark, "mteb")
	}
}
