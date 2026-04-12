package eval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// CortexVersion is the current version of Cortex.
const CortexVersion = "0.1.0"

// Persister saves eval results to SQLite.
type Persister struct {
	db *sql.DB
}

// NewPersister creates a new SQLite persister.
// The database is stored in .cortex/db/evals.db relative to the current directory.
func NewPersister() (*Persister, error) {
	dbDir := filepath.Join(".cortex", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	dbPath := filepath.Join(dbDir, "evals_v2.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	p := &Persister{db: db}
	if err := p.init(); err != nil {
		db.Close()
		return nil, err
	}

	return p, nil
}

// init creates the schema if needed.
func (p *Persister) init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS agentic_eval_runs (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		total_baseline_tool_calls INTEGER,
		total_cortex_tool_calls INTEGER,
		avg_tool_call_reduction REAL,
		avg_time_reduction REAL,
		avg_cost_reduction REAL,
		avg_lift REAL,
		cortex_wins INTEGER,
		baseline_wins INTEGER,
		ties INTEGER,
		pass_rate REAL,
		pass BOOLEAN,
		scenarios_json TEXT,
		tool_calls_by_type_json TEXT,
		git_commit_sha TEXT,
		git_branch TEXT,
		run_duration_ms INTEGER,
		cortex_version TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_agentic_runs_timestamp ON agentic_eval_runs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_agentic_runs_reduction ON agentic_eval_runs(avg_tool_call_reduction);

	CREATE TABLE IF NOT EXISTS eval_runs (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		provider TEXT,
		model TEXT,
		scenario_id TEXT,
		scenario_name TEXT,
		avg_baseline_score REAL,
		avg_cortex_score REAL,
		avg_lift REAL,
		cortex_wins INTEGER,
		baseline_wins INTEGER,
		ties INTEGER,
		pass_rate REAL,
		pass BOOLEAN,
		scenarios_json TEXT,
		git_commit_sha TEXT,
		git_branch TEXT,
		run_duration_ms INTEGER,
		cortex_version TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_eval_runs_timestamp ON eval_runs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_eval_runs_lift ON eval_runs(avg_lift);
	CREATE INDEX IF NOT EXISTS idx_eval_runs_scenario ON eval_runs(scenario_id);

	CREATE TABLE IF NOT EXISTS eval_scenario_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id TEXT NOT NULL,
		scenario_id TEXT NOT NULL,
		scenario_name TEXT,
		avg_baseline_score REAL,
		avg_cortex_score REAL,
		avg_lift REAL,
		cortex_wins INTEGER,
		baseline_wins INTEGER,
		ties INTEGER,
		has_ranking BOOLEAN,
		avg_ndcg REAL,
		avg_fast_ndcg REAL,
		avg_full_ndcg REAL,
		avg_abr REAL,
		pass BOOLEAN,
		FOREIGN KEY(run_id) REFERENCES eval_runs(id)
	);

	CREATE INDEX IF NOT EXISTS idx_scenario_results_run_id ON eval_scenario_results(run_id);
	CREATE INDEX IF NOT EXISTS idx_scenario_results_scenario_id ON eval_scenario_results(scenario_id);
	`
	_, err := p.db.Exec(schema)
	if err != nil {
		return err
	}

	// Migrate: add new columns to existing eval_runs table if missing
	migrations := []string{
		"ALTER TABLE eval_runs ADD COLUMN git_commit_sha TEXT",
		"ALTER TABLE eval_runs ADD COLUMN git_branch TEXT",
		"ALTER TABLE eval_runs ADD COLUMN run_duration_ms INTEGER",
		"ALTER TABLE eval_runs ADD COLUMN cortex_version TEXT",
		"ALTER TABLE eval_runs ADD COLUMN scenario_id TEXT",
		"ALTER TABLE eval_runs ADD COLUMN scenario_name TEXT",
		// Judge scoring columns
		"ALTER TABLE eval_runs ADD COLUMN judge_used INTEGER DEFAULT 0",
		"ALTER TABLE eval_runs ADD COLUMN judge_model TEXT",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_baseline_judge_correctness REAL",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_cortex_judge_correctness REAL",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_baseline_judge_understanding REAL",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_cortex_judge_understanding REAL",
		// Token usage columns
		"ALTER TABLE eval_runs ADD COLUMN total_baseline_tokens INTEGER DEFAULT 0",
		"ALTER TABLE eval_runs ADD COLUMN total_cortex_tokens INTEGER DEFAULT 0",
		"ALTER TABLE eval_runs ADD COLUMN avg_token_reduction REAL DEFAULT 0",
		"ALTER TABLE eval_runs ADD COLUMN avg_abr REAL DEFAULT 0",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_baseline_tokens INTEGER DEFAULT 0",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_cortex_tokens INTEGER DEFAULT 0",
		"ALTER TABLE eval_scenario_results ADD COLUMN token_reduction REAL DEFAULT 0",
		// MPR columns
		"ALTER TABLE eval_runs ADD COLUMN compare_provider TEXT",
		"ALTER TABLE eval_runs ADD COLUMN compare_model TEXT",
		"ALTER TABLE eval_runs ADD COLUMN avg_compare_score REAL DEFAULT 0",
		"ALTER TABLE eval_runs ADD COLUMN avg_mpr REAL DEFAULT 0",
		"ALTER TABLE eval_runs ADD COLUMN total_compare_tokens INTEGER DEFAULT 0",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_compare_score REAL DEFAULT 0",
		"ALTER TABLE eval_scenario_results ADD COLUMN avg_mpr REAL DEFAULT 0",
	}
	for _, m := range migrations {
		p.db.Exec(m) // Ignore errors (column already exists)
	}

	return nil
}

// getGitInfo retrieves the current git commit SHA and branch.
// Returns empty strings if git is not available or not in a git repository.
func getGitInfo() (commitSHA, branch string) {
	// Get commit SHA
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err == nil {
		commitSHA = strings.TrimSpace(string(out))
	}

	// Get branch name
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err = cmd.Output()
	if err == nil {
		branch = strings.TrimSpace(string(out))
	}

	return commitSHA, branch
}

// Persist saves eval results to the database.
// durationMs is the elapsed time for the eval run in milliseconds.
func (p *Persister) Persist(results *Results, durationMs int64) error {
	id := fmt.Sprintf("eval-%s", time.Now().Format("20060102-150405"))
	results.Timestamp = Timestamp()

	scenariosJSON, err := json.Marshal(results.Scenarios)
	if err != nil {
		return fmt.Errorf("marshal scenarios: %w", err)
	}

	// Get git info
	commitSHA, branch := getGitInfo()

	// Extract scenario info for single-scenario runs
	var scenarioID, scenarioName string
	if len(results.Scenarios) == 1 {
		scenarioID = results.Scenarios[0].ScenarioID
		scenarioName = results.Scenarios[0].Name
	}

	// Insert main run record
	_, err = p.db.Exec(`
		INSERT INTO eval_runs (id, timestamp, provider, model, scenario_id, scenario_name, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json, git_commit_sha, git_branch, run_duration_ms, cortex_version, total_baseline_tokens, total_cortex_tokens, avg_token_reduction, avg_abr, compare_provider, compare_model, avg_compare_score, avg_mpr, total_compare_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, results.Timestamp, results.Provider, results.Model, scenarioID, scenarioName,
		results.AvgBaselineScore, results.AvgCortexScore, results.AvgLift,
		results.TotalCortexWins, results.TotalBaselineWins, results.TotalTies,
		results.PassRate, results.Pass, string(scenariosJSON),
		commitSHA, branch, durationMs, CortexVersion,
		results.TotalBaselineTokens, results.TotalCortexTokens, results.AvgTokenReduction, results.AvgABR,
		results.CompareProvider, results.CompareModel, results.AvgCompareScore, results.AvgMPR, results.TotalCompareTokens)

	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}

	// Insert scenario results for easy querying
	for _, scenario := range results.Scenarios {
		_, err = p.db.Exec(`
			INSERT INTO eval_scenario_results (run_id, scenario_id, scenario_name, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, has_ranking, avg_ndcg, avg_fast_ndcg, avg_full_ndcg, avg_abr, pass, avg_baseline_tokens, avg_cortex_tokens, token_reduction, avg_compare_score, avg_mpr)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, scenario.ScenarioID, scenario.Name,
			scenario.AvgBaselineScore, scenario.AvgCortexScore, scenario.AvgLift,
			scenario.CortexWins, scenario.BaselineWins, scenario.Ties,
			scenario.HasRanking, scenario.AvgNDCG, scenario.AvgFastNDCG, scenario.AvgFullNDCG, scenario.AvgABR,
			scenario.Pass, scenario.AvgBaselineTokens, scenario.AvgCortexTokens, scenario.TokenReduction,
			scenario.AvgCompareScore, scenario.AvgMPR)
		if err != nil {
			return fmt.Errorf("insert scenario result %s: %w", scenario.ScenarioID, err)
		}
	}

	return nil
}

// GetLatest returns the most recent eval run.
func (p *Persister) GetLatest() (*Results, error) {
	row := p.db.QueryRow(`
		SELECT timestamp, provider, model, avg_baseline_score, avg_cortex_score, avg_lift,
		       cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json
		FROM eval_runs
		ORDER BY timestamp DESC
		LIMIT 1
	`)

	var r Results
	var scenariosJSON string
	err := row.Scan(&r.Timestamp, &r.Provider, &r.Model,
		&r.AvgBaselineScore, &r.AvgCortexScore, &r.AvgLift,
		&r.TotalCortexWins, &r.TotalBaselineWins, &r.TotalTies,
		&r.PassRate, &r.Pass, &scenariosJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(scenariosJSON), &r.Scenarios); err != nil {
		return nil, err
	}

	return &r, nil
}

// GetTrend returns lift values over the last N runs.
func (p *Persister) GetTrend(n int) ([]float64, error) {
	rows, err := p.db.Query(`
		SELECT avg_lift FROM eval_runs
		ORDER BY timestamp DESC
		LIMIT ?
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lifts []float64
	for rows.Next() {
		var lift float64
		if err := rows.Scan(&lift); err != nil {
			return nil, err
		}
		lifts = append(lifts, lift)
	}

	// Reverse to get chronological order
	for i, j := 0, len(lifts)-1; i < j; i, j = i+1, j-1 {
		lifts[i], lifts[j] = lifts[j], lifts[i]
	}

	return lifts, nil
}

// ABRTrendPoint represents a single data point in the ABR trend.
type ABRTrendPoint struct {
	Timestamp    string  `json:"timestamp"`
	AvgABR       float64 `json:"avg_abr"`
	RunID        string  `json:"run_id"`
	GitCommitSHA string  `json:"git_commit_sha"`
}

// GetABRTrend returns ABR values over the last N runs.
func (p *Persister) GetABRTrend(n int) ([]ABRTrendPoint, error) {
	rows, err := p.db.Query(`
		SELECT r.id, r.timestamp, COALESCE(r.git_commit_sha, ''), AVG(s.avg_abr)
		FROM eval_runs r
		JOIN eval_scenario_results s ON s.run_id = r.id
		WHERE s.has_ranking = 1 AND s.avg_abr > 0
		GROUP BY r.id
		ORDER BY r.timestamp DESC
		LIMIT ?
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []ABRTrendPoint
	for rows.Next() {
		var pt ABRTrendPoint
		if err := rows.Scan(&pt.RunID, &pt.Timestamp, &pt.GitCommitSHA, &pt.AvgABR); err != nil {
			return nil, err
		}
		points = append(points, pt)
	}

	// Reverse to get chronological order
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}

	return points, nil
}

// Close closes the database connection.
func (p *Persister) Close() error {
	return p.db.Close()
}

// PersistAgentic saves agentic eval results to the database.
func (p *Persister) PersistAgentic(results *AgenticResults, durationMs int64) error {
	id := fmt.Sprintf("agentic-%s", time.Now().Format("20060102-150405"))
	results.Timestamp = Timestamp()

	scenariosJSON, err := json.Marshal(results.Scenarios)
	if err != nil {
		return fmt.Errorf("marshal scenarios: %w", err)
	}

	// Aggregate tool calls by type across all scenarios
	toolCallsByType := make(map[string][2]int) // [baseline, cortex]
	for _, s := range results.Scenarios {
		for _, t := range s.Tests {
			for tool, count := range t.BaselineCallsByType {
				v := toolCallsByType[tool]
				v[0] += count
				toolCallsByType[tool] = v
			}
			for tool, count := range t.CortexCallsByType {
				v := toolCallsByType[tool]
				v[1] += count
				toolCallsByType[tool] = v
			}
		}
	}
	toolCallsJSON, err := json.Marshal(toolCallsByType)
	if err != nil {
		return fmt.Errorf("marshal tool calls: %w", err)
	}

	// Get git info
	commitSHA, branch := getGitInfo()

	_, err = p.db.Exec(`
		INSERT INTO agentic_eval_runs (id, timestamp, total_baseline_tool_calls, total_cortex_tool_calls, avg_tool_call_reduction, avg_time_reduction, avg_cost_reduction, avg_lift, cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json, tool_calls_by_type_json, git_commit_sha, git_branch, run_duration_ms, cortex_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, results.Timestamp,
		results.TotalBaselineToolCalls, results.TotalCortexToolCalls,
		results.AvgToolCallReduction, results.AvgTimeReduction, results.AvgCostReduction,
		results.AvgLift, results.TotalCortexWins, results.TotalBaselineWins, results.TotalTies,
		results.PassRate, results.Pass, string(scenariosJSON), string(toolCallsJSON),
		commitSHA, branch, durationMs, CortexVersion)

	if err != nil {
		return fmt.Errorf("insert agentic run: %w", err)
	}

	return nil
}

// AgenticTrendPoint represents a single data point in the agentic trend.
type AgenticTrendPoint struct {
	Timestamp         string  `json:"timestamp"`
	ToolCallReduction float64 `json:"tool_call_reduction"`
	TimeReduction     float64 `json:"time_reduction"`
	CostReduction     float64 `json:"cost_reduction"`
	BaselineToolCalls int     `json:"baseline_tool_calls"`
	CortexToolCalls   int     `json:"cortex_tool_calls"`
}

// GetAgenticTrend returns tool call reduction over the last N runs.
func (p *Persister) GetAgenticTrend(n int) ([]AgenticTrendPoint, error) {
	rows, err := p.db.Query(`
		SELECT timestamp, avg_tool_call_reduction, avg_time_reduction, avg_cost_reduction,
		       total_baseline_tool_calls, total_cortex_tool_calls
		FROM agentic_eval_runs
		ORDER BY timestamp DESC
		LIMIT ?
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []AgenticTrendPoint
	for rows.Next() {
		var pt AgenticTrendPoint
		if err := rows.Scan(&pt.Timestamp, &pt.ToolCallReduction, &pt.TimeReduction,
			&pt.CostReduction, &pt.BaselineToolCalls, &pt.CortexToolCalls); err != nil {
			return nil, err
		}
		points = append(points, pt)
	}

	// Reverse to get chronological order
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}

	return points, nil
}
