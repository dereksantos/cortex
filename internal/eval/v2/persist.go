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
		abr REAL,
		pass_rate REAL,
		pass BOOLEAN,
		scenarios_json TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_eval_runs_timestamp ON eval_runs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_eval_runs_abr ON eval_runs(abr);
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
		INSERT INTO eval_runs (id, timestamp, provider, model, abr, pass_rate, pass, scenarios_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, results.Timestamp, results.Provider, results.Model, results.ABR, results.PassRate, results.Pass, string(scenariosJSON))

	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}

	return nil
}

// GetLatest returns the most recent eval run.
func (p *Persister) GetLatest() (*Results, error) {
	row := p.db.QueryRow(`
		SELECT timestamp, provider, model, abr, pass_rate, pass, scenarios_json
		FROM eval_runs
		ORDER BY timestamp DESC
		LIMIT 1
	`)

	var r Results
	var scenariosJSON string
	err := row.Scan(&r.Timestamp, &r.Provider, &r.Model, &r.ABR, &r.PassRate, &r.Pass, &scenariosJSON)
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

// GetTrend returns ABR values over the last N runs.
func (p *Persister) GetTrend(n int) ([]float64, error) {
	rows, err := p.db.Query(`
		SELECT abr FROM eval_runs
		ORDER BY timestamp DESC
		LIMIT ?
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var abrs []float64
	for rows.Next() {
		var abr float64
		if err := rows.Scan(&abr); err != nil {
			return nil, err
		}
		abrs = append(abrs, abr)
	}

	// Reverse to get chronological order
	for i, j := 0, len(abrs)-1; i < j; i, j = i+1, j-1 {
		abrs[i], abrs[j] = abrs[j], abrs[i]
	}

	return abrs, nil
}

// Close closes the database connection.
func (p *Persister) Close() error {
	return p.db.Close()
}
