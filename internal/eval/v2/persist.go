package eval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

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
		scenarios_json TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_eval_runs_timestamp ON eval_runs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_eval_runs_lift ON eval_runs(avg_lift);
	`
	_, err := p.db.Exec(schema)
	return err
}

// Persist saves eval results to the database.
func (p *Persister) Persist(results *Results) error {
	id := fmt.Sprintf("eval-%s", time.Now().Format("20060102-150405"))
	results.Timestamp = Timestamp()

	scenariosJSON, err := json.Marshal(results.Scenarios)
	if err != nil {
		return fmt.Errorf("marshal scenarios: %w", err)
	}

	_, err = p.db.Exec(`
		INSERT INTO eval_runs (id, timestamp, provider, model, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, results.Timestamp, results.Provider, results.Model,
		results.AvgBaselineScore, results.AvgCortexScore, results.AvgLift,
		results.TotalCortexWins, results.TotalBaselineWins, results.TotalTies,
		results.PassRate, results.Pass, string(scenariosJSON))

	if err != nil {
		return fmt.Errorf("insert run: %w", err)
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
