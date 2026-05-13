package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// cellResultsSchema is the SQL DDL for the per-cell grid output table.
// Column names mirror CellResult's JSON tags exactly so a downstream
// analyst who reads the JSONL append log via pandas/polars/DuckDB sees
// the same schema as someone querying SQLite directly.
//
// run_id is UNIQUE so PersistCell can use INSERT OR IGNORE on retries
// without duplicating rows in SQLite. (JSONL is append-only and may
// contain duplicates on retry — by design, per hard constraint #8.)
const cellResultsSchema = `
CREATE TABLE IF NOT EXISTS cell_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    schema_version TEXT NOT NULL,
    run_id TEXT NOT NULL UNIQUE,
    timestamp TEXT NOT NULL,
    git_commit_sha TEXT,
    git_branch TEXT,
    scenario_id TEXT NOT NULL,
    session_id TEXT,
    harness TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    backend TEXT,
    context_strategy TEXT NOT NULL,
    cortex_version TEXT,
    seed INTEGER,
    temperature REAL NOT NULL,
    tokens_in INTEGER NOT NULL,
    tokens_out INTEGER NOT NULL,
    injected_context_tokens INTEGER NOT NULL,
    cost_usd REAL NOT NULL,
    latency_ms INTEGER NOT NULL,
    agent_turns_total INTEGER NOT NULL,
    correction_turns INTEGER NOT NULL,
    tests_passed INTEGER NOT NULL,
    tests_failed INTEGER NOT NULL,
    task_success INTEGER NOT NULL,
    task_success_criterion TEXT NOT NULL,
    notes TEXT,
    inserted_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_cell_results_scenario ON cell_results(scenario_id);
CREATE INDEX IF NOT EXISTS idx_cell_results_harness ON cell_results(harness);
CREATE INDEX IF NOT EXISTS idx_cell_results_strategy ON cell_results(context_strategy);
CREATE INDEX IF NOT EXISTS idx_cell_results_timestamp ON cell_results(timestamp);
`

// cellResultsJSONLName is the filename inside dbDir for the append log.
// Stays a constant (not a Persister field) so analysis scripts can
// hardcode the path: <dbDir>/cell_results.jsonl.
const cellResultsJSONLName = "cell_results.jsonl"

// dailySpendSchema tracks USD-spent-per-UTC-day for the multi-tier
// ceiling system. The grid runner reads this between cells to enforce
// daily and lifetime ceilings; values accumulate via INSERT ... ON
// CONFLICT to make repeated AddDailySpendUTC calls additive.
const dailySpendSchema = `
CREATE TABLE IF NOT EXISTS daily_spend (
    date TEXT PRIMARY KEY,
    usd REAL NOT NULL DEFAULT 0
);
`

// AddDailySpendUTC adds usd to the row for t's UTC date, inserting if
// missing. Pass cell.CostUSD after a successful call.
func (p *Persister) AddDailySpendUTC(t time.Time, usd float64) error {
	date := t.UTC().Format("2006-01-02")
	_, err := p.db.Exec(`
        INSERT INTO daily_spend (date, usd) VALUES (?, ?)
        ON CONFLICT(date) DO UPDATE SET usd = usd + excluded.usd
    `, date, usd)
	if err != nil {
		return fmt.Errorf("daily_spend upsert: %w", err)
	}
	return nil
}

// GetDailySpendUTC returns the recorded spend for t's UTC date, or 0
// if the bucket doesn't exist yet (a missing row is the first call's
// state, not an error).
func (p *Persister) GetDailySpendUTC(t time.Time) (float64, error) {
	date := t.UTC().Format("2006-01-02")
	var usd float64
	err := p.db.QueryRow(
		`SELECT COALESCE(SUM(usd), 0) FROM daily_spend WHERE date = ?`, date,
	).Scan(&usd)
	if err != nil {
		return 0, fmt.Errorf("daily_spend select: %w", err)
	}
	return usd, nil
}

// GetLifetimeSpend returns SUM(usd) across all daily buckets — the
// running total against CORTEX_EVAL_LIFETIME_USD_CEILING.
func (p *Persister) GetLifetimeSpend() (float64, error) {
	var usd float64
	err := p.db.QueryRow(`SELECT COALESCE(SUM(usd), 0) FROM daily_spend`).Scan(&usd)
	if err != nil {
		return 0, fmt.Errorf("lifetime_spend select: %w", err)
	}
	return usd, nil
}

// CellSummaryRow is one row of the (model, strategy) aggregate
// powering `cortex eval grid --report-summary`.
type CellSummaryRow struct {
	Model              string
	Strategy           string
	Cells              int
	Passes             int
	PassRate           float64
	MeanTokensIn       float64
	MeanTokensOut      float64
	MeanCostUSD        float64
	TotalCostUSD       float64
	MeanLatencyMs      float64
	MeanInjectedTokens float64
}

// SummarizeCellResults groups SQLite cell_results by (model, strategy)
// and returns aggregate stats. scenarioPrefix is an optional LIKE
// filter on scenario_id ("smoke" matches smoke-hello, smoke-anything;
// empty string means no filter). Sorted by total cost ascending so
// the cheapest (likely free-tier) configurations come first.
func (p *Persister) SummarizeCellResults(scenarioPrefix string) ([]CellSummaryRow, error) {
	var (
		query = `
            SELECT
                model, context_strategy,
                COUNT(*) AS cells,
                SUM(task_success) AS passes,
                AVG(tokens_in) AS mean_in,
                AVG(tokens_out) AS mean_out,
                AVG(cost_usd) AS mean_cost,
                SUM(cost_usd) AS total_cost,
                AVG(latency_ms) AS mean_latency,
                AVG(injected_context_tokens) AS mean_injected
            FROM cell_results
        `
		args []any
	)
	if scenarioPrefix != "" {
		query += " WHERE scenario_id LIKE ? "
		args = append(args, scenarioPrefix+"%")
	}
	query += " GROUP BY model, context_strategy ORDER BY total_cost ASC, model, context_strategy"

	rows, err := p.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("summarize cell_results: %w", err)
	}
	defer rows.Close()

	var out []CellSummaryRow
	for rows.Next() {
		var r CellSummaryRow
		if err := rows.Scan(&r.Model, &r.Strategy, &r.Cells, &r.Passes,
			&r.MeanTokensIn, &r.MeanTokensOut, &r.MeanCostUSD,
			&r.TotalCostUSD, &r.MeanLatencyMs, &r.MeanInjectedTokens); err != nil {
			return nil, fmt.Errorf("scan summary row: %w", err)
		}
		if r.Cells > 0 {
			r.PassRate = float64(r.Passes) / float64(r.Cells)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentCellsFromJSONL returns the last n CellResult rows from the
// JSONL append log (the canonical analysis source per hard constraint
// #8). Reads sequentially — fine for the modest row counts we expect;
// callers wanting tens of thousands should use direct SQLite queries
// instead. Malformed lines are silently skipped.
//
// Returns nil, nil when the JSONL file does not yet exist (first-run
// state, not an error).
func (p *Persister) RecentCellsFromJSONL(n int) ([]CellResult, error) {
	path := p.cellResultsJSONLPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open jsonl: %w", err)
	}
	defer f.Close()

	var all []CellResult
	scanner := bufio.NewScanner(f)
	// CellResult JSON ~1-2 KB per line; cap at 1 MiB to handle outliers
	// without exposing a runaway-line foot-gun.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var c CellResult
		if err := json.Unmarshal(scanner.Bytes(), &c); err != nil {
			continue
		}
		all = append(all, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan jsonl: %w", err)
	}

	if n <= 0 || len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// PersistCell writes one CellResult to all required backends in order:
//   - Journal eval.cell_result entry (source of truth, regen substrate)
//   - SQLite cell_results table (fast queries, uniqueness)
//   - JSONL append log <dbDir>/cell_results.jsonl (portable analysis)
//
// Validation runs first; an invalid row touches no backend.
//
// The journal entry is appended FIRST so rebuild can regenerate SQLite +
// JSONL from the journal alone via ProjectCellFromEntry. Hard constraint
// #8 stays satisfied because both projections are deterministic
// functions of the journal.
//
// SQLite uses INSERT OR IGNORE on run_id so retries — including the
// "JSONL append failed, caller retries" path — stay idempotent. JSONL is
// append-only and may legitimately accumulate duplicates on retry;
// downstream consumers should de-dup by run_id when that matters. The
// journal itself is also append-only: a retry produces a second entry
// with a new offset, and the projector handles dedup at the SQLite
// layer.
//
// JSONL append failure does NOT roll back the SQLite insert nor the
// journal entry: a missing row is worse than a duplicate.
func (p *Persister) PersistCell(ctx context.Context, r *CellResult) error {
	if r == nil {
		return errors.New("PersistCell: nil CellResult")
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	if err := p.appendCellJournal(r); err != nil {
		return fmt.Errorf("journal append: %w", err)
	}

	return p.projectCell(ctx, r)
}

// ProjectCell writes one CellResult to the read-side projections (SQLite
// + JSONL) WITHOUT emitting a journal entry. This is the entry point
// callers use when replaying journal entries during `cortex journal
// rebuild`: the journal already has the entry, we just need to
// regenerate derived state.
//
// Validation still runs — a malformed payload in the journal (e.g., from
// a future writer version with looser invariants) should not corrupt the
// projection.
func (p *Persister) ProjectCell(ctx context.Context, r *CellResult) error {
	if r == nil {
		return errors.New("ProjectCell: nil CellResult")
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return p.projectCell(ctx, r)
}

// ProjectCellFromEntry is the projector adapter wired into the journal
// indexer: take a parsed eval.cell_result payload, materialize SQLite +
// JSONL state. Idempotent on retries via SQLite's INSERT OR IGNORE.
func (p *Persister) ProjectCellFromEntry(ctx context.Context, payload *journal.EvalCellResultPayload) error {
	if payload == nil {
		return errors.New("ProjectCellFromEntry: nil payload")
	}
	r := payloadToCellResult(payload)
	return p.ProjectCell(ctx, r)
}

// projectCell is the shared SQLite + JSONL write path. Callers must
// validate before invoking — PersistCell does this once before journal
// append, ProjectCell does it as a guard against unprojectable journal
// entries.
func (p *Persister) projectCell(ctx context.Context, r *CellResult) error {
	if err := p.insertCellSQLite(ctx, r); err != nil {
		return fmt.Errorf("sqlite insert: %w", err)
	}
	if err := p.appendCellJSONL(r); err != nil {
		return fmt.Errorf("jsonl append: %w", err)
	}
	return nil
}

// appendCellJournal writes one eval.cell_result entry to the journal
// writer-class. Returns an error if the writer is not configured —
// every Persister opened via NewPersister or newTestPersister has one;
// a nil journal is a programming error from a hand-built Persister.
func (p *Persister) appendCellJournal(r *CellResult) error {
	if p.journal == nil {
		return errors.New("eval journal writer is nil; construct Persister via NewPersister")
	}
	entry, err := journal.NewEvalCellResultEntry(cellResultToPayload(r))
	if err != nil {
		return fmt.Errorf("build entry: %w", err)
	}
	if _, err := p.journal.Append(entry); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}

// payloadToCellResult is the inverse of cellResultToPayload — used by
// the rebuild projector to turn a journal-decoded payload back into the
// CellResult the validator and SQLite/JSONL writers expect.
func payloadToCellResult(p *journal.EvalCellResultPayload) *CellResult {
	return &CellResult{
		SchemaVersion:         p.SchemaVersion,
		RunID:                 p.RunID,
		Timestamp:             p.Timestamp,
		GitCommitSHA:          p.GitCommitSHA,
		GitBranch:             p.GitBranch,
		ScenarioID:            p.ScenarioID,
		SessionID:             p.SessionID,
		Harness:               p.Harness,
		Provider:              p.Provider,
		Model:                 p.Model,
		Backend:               p.Backend,
		ContextStrategy:       p.ContextStrategy,
		CortexVersion:         p.CortexVersion,
		Seed:                  p.Seed,
		Temperature:           p.Temperature,
		TokensIn:              p.TokensIn,
		TokensOut:             p.TokensOut,
		InjectedContextTokens: p.InjectedContextTokens,
		CostUSD:               p.CostUSD,
		LatencyMs:             p.LatencyMs,
		AgentTurnsTotal:       p.AgentTurnsTotal,
		CorrectionTurns:       p.CorrectionTurns,
		TestsPassed:           p.TestsPassed,
		TestsFailed:           p.TestsFailed,
		TaskSuccess:           p.TaskSuccess,
		TaskSuccessCriterion:  p.TaskSuccessCriterion,
		Notes:                 p.Notes,
	}
}

// cellResultToPayload maps a CellResult to its journal payload. The two
// types are intentionally mirror-image structs (every CellResult field
// has the same JSON tag in EvalCellResultPayload) so this is a flat copy
// rather than a transform.
func cellResultToPayload(r *CellResult) journal.EvalCellResultPayload {
	return journal.EvalCellResultPayload{
		SchemaVersion:         r.SchemaVersion,
		RunID:                 r.RunID,
		Timestamp:             r.Timestamp,
		GitCommitSHA:          r.GitCommitSHA,
		GitBranch:             r.GitBranch,
		ScenarioID:            r.ScenarioID,
		SessionID:             r.SessionID,
		Harness:               r.Harness,
		Provider:              r.Provider,
		Model:                 r.Model,
		Backend:               r.Backend,
		ContextStrategy:       r.ContextStrategy,
		CortexVersion:         r.CortexVersion,
		Seed:                  r.Seed,
		Temperature:           r.Temperature,
		TokensIn:              r.TokensIn,
		TokensOut:             r.TokensOut,
		InjectedContextTokens: r.InjectedContextTokens,
		CostUSD:               r.CostUSD,
		LatencyMs:             r.LatencyMs,
		AgentTurnsTotal:       r.AgentTurnsTotal,
		CorrectionTurns:       r.CorrectionTurns,
		TestsPassed:           r.TestsPassed,
		TestsFailed:           r.TestsFailed,
		TaskSuccess:           r.TaskSuccess,
		TaskSuccessCriterion:  r.TaskSuccessCriterion,
		Notes:                 r.Notes,
	}
}

func (p *Persister) insertCellSQLite(ctx context.Context, r *CellResult) error {
	const stmt = `
        INSERT OR IGNORE INTO cell_results (
            schema_version, run_id, timestamp, git_commit_sha, git_branch,
            scenario_id, session_id, harness, provider, model, backend,
            context_strategy, cortex_version, seed, temperature,
            tokens_in, tokens_out, injected_context_tokens, cost_usd,
            latency_ms, agent_turns_total, correction_turns,
            tests_passed, tests_failed, task_success, task_success_criterion,
            notes
        ) VALUES (
            ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
            ?, ?, ?, ?, ?, ?, ?, ?
        )
    `
	_, err := p.db.ExecContext(ctx, stmt,
		r.SchemaVersion, r.RunID, r.Timestamp,
		nullableString(r.GitCommitSHA), nullableString(r.GitBranch),
		r.ScenarioID, nullableString(r.SessionID),
		r.Harness, r.Provider, r.Model, nullableString(r.Backend),
		r.ContextStrategy, nullableString(r.CortexVersion),
		nullableSeed(r.Seed),
		r.Temperature,
		r.TokensIn, r.TokensOut, r.InjectedContextTokens, r.CostUSD,
		r.LatencyMs, r.AgentTurnsTotal, r.CorrectionTurns,
		r.TestsPassed, r.TestsFailed, boolToInt(r.TaskSuccess), r.TaskSuccessCriterion,
		nullableString(r.Notes),
	)
	return err
}

// appendCellJSONL appends one JSON-encoded line to the jsonl log,
// fsync'd before return. Uses O_APPEND so concurrent writers from a
// single process serialize at the kernel level (single small writes
// under PIPE_BUF are atomic on POSIX).
func (p *Persister) appendCellJSONL(r *CellResult) error {
	path := p.cellResultsJSONLPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return f.Sync()
}

// cellResultsJSONLPath returns the absolute (or cwd-relative) path to
// the JSONL log. Honors p.dbDir when set (tests + NewPersister both set
// it); otherwise falls back to the canonical .cortex/db/ location.
func (p *Persister) cellResultsJSONLPath() string {
	dir := p.dbDir
	if dir == "" {
		dir = filepath.Join(".cortex", "db")
	}
	return filepath.Join(dir, cellResultsJSONLName)
}

// nullableString maps Go zero-value strings to SQL NULL so optional
// CellResult fields (git_commit_sha, session_id, backend, notes, ...)
// store as NULL rather than empty strings — keeps analysis-side
// COALESCE/IS NULL semantics correct.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableSeed maps an unset *int64 (Seed pointer) to SQL NULL. A
// stored NULL means "seed not specified"; a stored 0 means
// "deterministically seeded with 0".
func nullableSeed(s *int64) any {
	if s == nil {
		return nil
	}
	return *s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
