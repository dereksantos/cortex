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
// The database is stored in .context/db/evals.db relative to the current directory.
func NewPersister() (*Persister, error) {
	dbDir := filepath.Join(".context", "db")
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
	CREATE TABLE IF NOT EXISTS eval_runs (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		provider TEXT,
		model TEXT,
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

	// Insert main run record
	_, err = p.db.Exec(`
		INSERT INTO eval_runs (id, timestamp, provider, model, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json, git_commit_sha, git_branch, run_duration_ms, cortex_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, results.Timestamp, results.Provider, results.Model,
		results.AvgBaselineScore, results.AvgCortexScore, results.AvgLift,
		results.TotalCortexWins, results.TotalBaselineWins, results.TotalTies,
		results.PassRate, results.Pass, string(scenariosJSON),
		commitSHA, branch, durationMs, CortexVersion)

	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}

	// Insert scenario results for easy querying
	for _, scenario := range results.Scenarios {
		_, err = p.db.Exec(`
			INSERT INTO eval_scenario_results (run_id, scenario_id, scenario_name, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, has_ranking, avg_ndcg, avg_fast_ndcg, avg_full_ndcg, avg_abr, pass)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, scenario.ScenarioID, scenario.Name,
			scenario.AvgBaselineScore, scenario.AvgCortexScore, scenario.AvgLift,
			scenario.CortexWins, scenario.BaselineWins, scenario.Ties,
			scenario.HasRanking, scenario.AvgNDCG, scenario.AvgFastNDCG, scenario.AvgFullNDCG, scenario.AvgABR,
			scenario.Pass)
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

// Close closes the database connection.
func (p *Persister) Close() error {
	return p.db.Close()
}
