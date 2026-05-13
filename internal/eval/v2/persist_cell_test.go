package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
)

// validCellResult returns a complete, valid CellResult suitable as a
// baseline for table-driven tests to mutate.
func validCellResult() *CellResult {
	seed := int64(42)
	return &CellResult{
		SchemaVersion:         CellResultSchemaVersion,
		RunID:                 "01HZ-TEST-RUN",
		Timestamp:             "2026-05-10T13:30:00Z",
		GitCommitSHA:          "abc1234",
		GitBranch:             "feat/eval-harness",
		ScenarioID:            "library-service",
		SessionID:             "01-scaffold-and-books",
		Harness:               HarnessAider,
		Provider:              ProviderOpenRouter,
		Model:                 "openai/gpt-oss-20b:free",
		ContextStrategy:       StrategyCortex,
		CortexVersion:         "0.1.0",
		Seed:                  &seed,
		Temperature:           0.0,
		TokensIn:              18342,
		TokensOut:             944,
		InjectedContextTokens: 312,
		CostUSD:               0.0042,
		LatencyMs:             8123,
		AgentTurnsTotal:       9,
		CorrectionTurns:       2,
		TestsPassed:           18,
		TestsFailed:           1,
		TaskSuccess:           true,
		TaskSuccessCriterion:  CriterionTestsPassAll,
	}
}

func TestPersistCell_HappyPath_BothBackends(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	// SQLite: row exists, content matches schema mapping
	var (
		harness, model, strategy, provider string
		tokensIn                           int
		costUSD                            float64
		taskSuccess                        int
	)
	err := p.db.QueryRow(`SELECT harness, model, context_strategy, provider, tokens_in, cost_usd, task_success
		FROM cell_results WHERE run_id=?`, r.RunID).Scan(
		&harness, &model, &strategy, &provider, &tokensIn, &costUSD, &taskSuccess)
	if err != nil {
		t.Fatalf("sqlite query: %v", err)
	}
	if harness != HarnessAider {
		t.Errorf("harness=%q want %q", harness, HarnessAider)
	}
	if model != "openai/gpt-oss-20b:free" {
		t.Errorf("model=%q", model)
	}
	if strategy != StrategyCortex {
		t.Errorf("strategy=%q want %q", strategy, StrategyCortex)
	}
	if provider != ProviderOpenRouter {
		t.Errorf("provider=%q", provider)
	}
	if tokensIn != 18342 || costUSD != 0.0042 {
		t.Errorf("numerics: tokens_in=%d cost_usd=%v", tokensIn, costUSD)
	}
	if taskSuccess != 1 {
		t.Errorf("task_success=%d want 1", taskSuccess)
	}

	// JSONL: file exists, exactly one line, JSON round-trips back to the
	// original CellResult.
	lines := readJSONL(t, p.cellResultsJSONLPath())
	if len(lines) != 1 {
		t.Fatalf("jsonl line count=%d want 1", len(lines))
	}
	var got CellResult
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("jsonl unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&got, r) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, r)
	}
}

func TestPersistCell_ValidationFails_NoSideEffects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*CellResult)
		wantErr string
	}{
		{"missing run_id", func(r *CellResult) { r.RunID = "" }, "run_id"},
		{"unknown harness", func(r *CellResult) { r.Harness = "claude_code" }, "unknown harness"},
		{"unknown provider", func(r *CellResult) { r.Provider = "groq" }, "unknown provider"},
		{"injection on baseline", func(r *CellResult) {
			r.ContextStrategy = StrategyBaseline
			r.CortexVersion = ""
		}, "only cortex strategy may inject"},
		{"unknown criterion", func(r *CellResult) { r.TaskSuccessCriterion = "vibes" }, "task_success_criterion"},
		{"negative cost", func(r *CellResult) { r.CostUSD = -0.01 }, "cost_usd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPersister(t)
			r := validCellResult()
			tc.mutate(r)

			err := p.PersistCell(context.Background(), r)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err=%v, want contains %q", err, tc.wantErr)
			}

			// Neither backend should have been touched.
			var count int
			if err := p.db.QueryRow("SELECT COUNT(*) FROM cell_results").Scan(&count); err != nil {
				t.Fatalf("sqlite count: %v", err)
			}
			if count != 0 {
				t.Errorf("sqlite has %d rows after invalid insert; want 0", count)
			}
			if _, err := os.Stat(p.cellResultsJSONLPath()); !os.IsNotExist(err) {
				t.Errorf("jsonl file exists after invalid insert (err=%v); want non-existent", err)
			}
		})
	}
}

// TestPersistCell_NilInput defends against the trivial nil-pointer panic.
func TestPersistCell_NilInput(t *testing.T) {
	p := newTestPersister(t)
	err := p.PersistCell(context.Background(), nil)
	if err == nil {
		t.Fatal("want error on nil input, got nil")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("err=%v, want contains 'nil'", err)
	}
}

// TestPersistCell_RetryIdempotentOnSQLite documents the documented
// behavior: a duplicate-RunID retry leaves SQLite at one row (UNIQUE +
// INSERT OR IGNORE) but appends a second JSONL line (intentional —
// duplicates are tolerable in the analysis log). Per hard constraint
// #8 a missing row is worse than a duplicate.
func TestPersistCell_RetryIdempotentOnSQLite(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("retry: %v", err)
	}

	var sqliteCount int
	if err := p.db.QueryRow("SELECT COUNT(*) FROM cell_results WHERE run_id=?", r.RunID).Scan(&sqliteCount); err != nil {
		t.Fatalf("sqlite count: %v", err)
	}
	if sqliteCount != 1 {
		t.Errorf("sqlite count after retry=%d, want 1 (UNIQUE/INSERT OR IGNORE failed)", sqliteCount)
	}

	jsonlLines := readJSONL(t, p.cellResultsJSONLPath())
	if len(jsonlLines) != 2 {
		t.Errorf("jsonl line count after retry=%d, want 2 (analysis log is duplicate-tolerant)", len(jsonlLines))
	}
}

// TestPersistCell_OptionalFieldsRoundTrip verifies that a baseline cell
// (no Cortex injection, no seed, no notes, no session) serializes to
// SQL NULL / JSON omitempty correctly and round-trips back unchanged.
func TestPersistCell_OptionalFieldsRoundTrip(t *testing.T) {
	p := newTestPersister(t)
	r := &CellResult{
		SchemaVersion:        CellResultSchemaVersion,
		RunID:                "baseline-no-options",
		Timestamp:            "2026-05-10T14:00:00Z",
		ScenarioID:           "smoke",
		Harness:              HarnessAider,
		Provider:             ProviderOpenRouter,
		Model:                "openai/gpt-oss-20b:free",
		ContextStrategy:      StrategyBaseline,
		Temperature:          0.5,
		TokensIn:             100,
		TokensOut:            50,
		LatencyMs:            1000,
		AgentTurnsTotal:      1,
		TestsPassed:          5,
		TestsFailed:          0,
		TaskSuccess:          true,
		TaskSuccessCriterion: CriterionTestsPassAll,
	}

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	// SQLite: optional columns must be NULL, not empty string.
	var seed, sessionID, cortexVersion, gitSHA, notes interface{}
	err := p.db.QueryRow(`SELECT seed, session_id, cortex_version, git_commit_sha, notes
		FROM cell_results WHERE run_id=?`, r.RunID).Scan(
		&seed, &sessionID, &cortexVersion, &gitSHA, &notes)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for name, v := range map[string]interface{}{
		"seed": seed, "session_id": sessionID, "cortex_version": cortexVersion,
		"git_commit_sha": gitSHA, "notes": notes,
	} {
		if v != nil {
			t.Errorf("%s=%v want NULL", name, v)
		}
	}

	// JSONL: optional fields must be absent (omitempty), and round-trip
	// must reproduce the original.
	lines := readJSONL(t, p.cellResultsJSONLPath())
	if len(lines) != 1 {
		t.Fatalf("jsonl lines=%d", len(lines))
	}
	for _, k := range []string{`"seed"`, `"session_id"`, `"cortex_version"`, `"git_commit_sha"`, `"notes"`, `"backend"`} {
		if strings.Contains(lines[0], k) {
			t.Errorf("baseline jsonl should not contain %s; got: %s", k, lines[0])
		}
	}
	var got CellResult
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&got, r) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, r)
	}
}

// TestRecentCellsFromJSONL exercises the analysis-path readback that
// powers `cortex eval grid --report`. Order is preserved (oldest first
// within the returned slice), and limit truncates from the head.
func TestRecentCellsFromJSONL(t *testing.T) {
	p := newTestPersister(t)

	// Empty file → returns nil, nil (not an error).
	got, err := p.RecentCellsFromJSONL(10)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if got != nil {
		t.Errorf("empty got %d rows, want nil", len(got))
	}

	// Write 5 CellResults with distinct RunIDs so we can assert order.
	for i := 0; i < 5; i++ {
		r := validCellResult()
		r.RunID = fmt.Sprintf("run-%d", i)
		if err := p.PersistCell(context.Background(), r); err != nil {
			t.Fatalf("PersistCell %d: %v", i, err)
		}
	}

	t.Run("limit larger than count returns all", func(t *testing.T) {
		got, err := p.RecentCellsFromJSONL(100)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 5 {
			t.Errorf("len=%d want 5", len(got))
		}
		if got[0].RunID != "run-0" || got[4].RunID != "run-4" {
			t.Errorf("order: %s, %s want run-0, run-4", got[0].RunID, got[4].RunID)
		}
	})

	t.Run("limit truncates to last N", func(t *testing.T) {
		got, err := p.RecentCellsFromJSONL(2)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("len=%d want 2", len(got))
		}
		if got[0].RunID != "run-3" || got[1].RunID != "run-4" {
			t.Errorf("truncated: %s, %s want run-3, run-4", got[0].RunID, got[1].RunID)
		}
	})

	t.Run("limit 0 returns all", func(t *testing.T) {
		got, _ := p.RecentCellsFromJSONL(0)
		if len(got) != 5 {
			t.Errorf("len=%d want 5", len(got))
		}
	})
}

// TestPersistCell_EmitsJournalEntry asserts the unification contract:
// a successful PersistCell appends one eval.cell_result entry to the
// writer-class journal, whose payload mirrors the CellResult. The
// journal is the source of truth; SQLite + JSONL are projections of it.
func TestPersistCell_EmitsJournalEntry(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	// Flush so FsyncPerBatch writes hit disk before the reader opens.
	if err := p.journal.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	reader, err := journal.NewReader(p.journalDir)
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer reader.Close()

	e, err := reader.Next()
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if e.Type != journal.TypeEvalCellResult {
		t.Errorf("entry Type=%q want %q", e.Type, journal.TypeEvalCellResult)
	}

	payload, err := journal.ParseEvalCellResult(e)
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	wantPayload := cellResultToPayload(r)
	if !reflect.DeepEqual(payload, &wantPayload) {
		t.Errorf("payload mismatch:\n got: %+v\nwant: %+v", payload, &wantPayload)
	}

	if _, err := reader.Next(); err != io.EOF {
		t.Errorf("want exactly one entry; second Next returned err=%v", err)
	}
}

// TestProjectCell_NoJournalWrite is the projection-side contract: when
// the indexer replays a journal entry it must materialize SQLite + JSONL
// state WITHOUT producing a duplicate journal entry. ProjectCell is the
// journal-free entry point both rebuild and live PersistCell route
// through.
func TestProjectCell_NoJournalWrite(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()

	if err := p.ProjectCell(context.Background(), r); err != nil {
		t.Fatalf("ProjectCell: %v", err)
	}

	// SQLite: row exists.
	var rows int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM cell_results WHERE run_id=?`, r.RunID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("sqlite rows=%d want 1", rows)
	}
	// JSONL: one line.
	if lines := readJSONL(t, p.cellResultsJSONLPath()); len(lines) != 1 {
		t.Errorf("jsonl lines=%d want 1", len(lines))
	}

	// Journal: empty (ProjectCell must NOT emit).
	if err := p.journal.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	reader, err := journal.NewReader(p.journalDir)
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer reader.Close()
	if _, err := reader.Next(); err != io.EOF {
		t.Errorf("journal should be empty after ProjectCell; got Next err=%v", err)
	}
}

// TestPersistCell_RebuildFromJournalAloneRegeneratesProjections is the
// E2 acceptance test: after PersistCell, truncating SQLite +
// cell_results.jsonl and replaying the journal entry via
// ProjectCellFromEntry must restore identical projection state.
// Demonstrates that the journal is the source of truth — both
// projections are deterministic functions of it.
func TestPersistCell_RebuildFromJournalAloneRegeneratesProjections(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()

	if err := p.PersistCell(context.Background(), r); err != nil {
		t.Fatalf("PersistCell: %v", err)
	}

	// Capture original projection state.
	originalLines := readJSONL(t, p.cellResultsJSONLPath())
	if len(originalLines) != 1 {
		t.Fatalf("setup: jsonl lines=%d want 1", len(originalLines))
	}

	// Truncate both projections so only the journal entry survives.
	if _, err := p.db.Exec("DELETE FROM cell_results"); err != nil {
		t.Fatalf("truncate sqlite: %v", err)
	}
	if err := os.Remove(p.cellResultsJSONLPath()); err != nil {
		t.Fatalf("remove jsonl: %v", err)
	}

	// Flush journal so the entry is on disk for the replay reader.
	if err := p.journal.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Replay: read the eval.cell_result entry and project it.
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
		t.Fatalf("parse payload: %v", err)
	}
	if err := p.ProjectCellFromEntry(context.Background(), payload); err != nil {
		t.Fatalf("ProjectCellFromEntry: %v", err)
	}

	// Both projections should be restored byte-identical.
	var rows int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM cell_results WHERE run_id=?`, r.RunID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("sqlite rows after rebuild=%d want 1", rows)
	}
	rebuiltLines := readJSONL(t, p.cellResultsJSONLPath())
	if !reflect.DeepEqual(originalLines, rebuiltLines) {
		t.Errorf("jsonl mismatch after rebuild:\n original: %v\n rebuilt:  %v", originalLines, rebuiltLines)
	}
}

// TestProjectCellFromEntry mirrors what the journal indexer's projector
// will do: take a parsed payload, project to SQLite + JSONL. Used by
// `cortex journal rebuild` to regenerate eval state from the journal
// alone.
func TestProjectCellFromEntry(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()
	payload := cellResultToPayload(r)

	if err := p.ProjectCellFromEntry(context.Background(), &payload); err != nil {
		t.Fatalf("ProjectCellFromEntry: %v", err)
	}

	var rows int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM cell_results WHERE run_id=?`, r.RunID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("sqlite rows=%d want 1", rows)
	}

	lines := readJSONL(t, p.cellResultsJSONLPath())
	if len(lines) != 1 {
		t.Fatalf("jsonl lines=%d want 1", len(lines))
	}
	var got CellResult
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&got, r) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, r)
	}
}

// TestPersistCell_ValidationFails_NoJournalEntry covers the
// no-side-effects guarantee on the journal: an invalid input must not
// produce a phantom entry that rebuild would later try to project.
func TestPersistCell_ValidationFails_NoJournalEntry(t *testing.T) {
	p := newTestPersister(t)
	r := validCellResult()
	r.RunID = "" // forces validation failure

	if err := p.PersistCell(context.Background(), r); err == nil {
		t.Fatal("PersistCell on invalid input: want error, got nil")
	}

	if err := p.journal.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	reader, err := journal.NewReader(p.journalDir)
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer reader.Close()
	if _, err := reader.Next(); err != io.EOF {
		t.Errorf("journal should be empty after validation failure; got Next err=%v", err)
	}
}

func readJSONL(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open jsonl %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return lines
}
