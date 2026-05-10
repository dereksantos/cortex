package eval

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestPersister(t *testing.T) *Persister {
	t.Helper()
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, ".cortex", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dbDir, "evals_v2.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	p := &Persister{db: db, dbDir: dbDir}
	if err := p.init(); err != nil {
		db.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() { p.Close() })
	return p
}

func TestGetABRTrend(t *testing.T) {
	p := newTestPersister(t)

	// Store multiple runs with known ABR values
	runs := []struct {
		id        string
		timestamp string
		abr       float64
		commitSHA string
	}{
		{"run-1", "2026-01-01T00:00:00Z", 0.65, "abc1234"},
		{"run-2", "2026-01-02T00:00:00Z", 0.72, "def5678"},
		{"run-3", "2026-01-03T00:00:00Z", 0.80, "ghi9012"},
		{"run-4", "2026-01-04T00:00:00Z", 0.88, "jkl3456"},
	}

	for _, run := range runs {
		// Insert eval_runs
		_, err := p.db.Exec(`
			INSERT INTO eval_runs (id, timestamp, provider, model, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json, git_commit_sha)
			VALUES (?, ?, 'mock', 'test', 0.5, 0.7, 0.2, 1, 0, 0, 1.0, 1, '[]', ?)
		`, run.id, run.timestamp, run.commitSHA)
		if err != nil {
			t.Fatalf("insert run %s: %v", run.id, err)
		}

		// Insert scenario result with ABR
		_, err = p.db.Exec(`
			INSERT INTO eval_scenario_results (run_id, scenario_id, scenario_name, avg_baseline_score, avg_cortex_score, avg_lift, cortex_wins, baseline_wins, ties, has_ranking, avg_ndcg, avg_fast_ndcg, avg_full_ndcg, avg_abr, pass)
			VALUES (?, 'test-scenario', 'Test', 0.5, 0.7, 0.2, 1, 0, 0, 1, 0.8, 0.7, 1.0, ?, 1)
		`, run.id, run.abr)
		if err != nil {
			t.Fatalf("insert scenario result %s: %v", run.id, err)
		}
	}

	t.Run("returns correct ordering and values", func(t *testing.T) {
		points, err := p.GetABRTrend(10)
		if err != nil {
			t.Fatalf("GetABRTrend: %v", err)
		}

		if len(points) != 4 {
			t.Fatalf("expected 4 points, got %d", len(points))
		}

		// Should be chronological (oldest first)
		if points[0].AvgABR != 0.65 {
			t.Errorf("first point ABR: expected 0.65, got %.2f", points[0].AvgABR)
		}
		if points[3].AvgABR != 0.88 {
			t.Errorf("last point ABR: expected 0.88, got %.2f", points[3].AvgABR)
		}

		// Check commit SHA
		if points[0].GitCommitSHA != "abc1234" {
			t.Errorf("first point commit: expected abc1234, got %s", points[0].GitCommitSHA)
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		points, err := p.GetABRTrend(2)
		if err != nil {
			t.Fatalf("GetABRTrend: %v", err)
		}

		if len(points) != 2 {
			t.Fatalf("expected 2 points, got %d", len(points))
		}

		// Should be the 2 most recent in chronological order
		if points[0].AvgABR != 0.80 {
			t.Errorf("first point ABR: expected 0.80, got %.2f", points[0].AvgABR)
		}
		if points[1].AvgABR != 0.88 {
			t.Errorf("second point ABR: expected 0.88, got %.2f", points[1].AvgABR)
		}
	})

	t.Run("empty result for no data", func(t *testing.T) {
		// Create a fresh persister with no data
		p2 := newTestPersister(t)
		points, err := p2.GetABRTrend(10)
		if err != nil {
			t.Fatalf("GetABRTrend: %v", err)
		}
		if len(points) != 0 {
			t.Errorf("expected 0 points, got %d", len(points))
		}
	})
}

func TestPersistWithTokens(t *testing.T) {
	p := newTestPersister(t)

	results := &Results{
		Provider: "mock",
		Model:    "test",
		Scenarios: []ScenarioResult{
			{
				ScenarioID:        "token-test",
				Name:              "Token Test",
				Tests:             []TestResult{{TestID: "t1", BaselineTokens: 100, CortexTokens: 80}},
				AvgBaselineScore:  0.5,
				AvgCortexScore:    0.7,
				AvgLift:           0.4,
				AvgBaselineTokens: 100,
				AvgCortexTokens:   80,
				TokenReduction:    0.2,
				Pass:              true,
			},
		},
		AvgBaselineScore:    0.5,
		AvgCortexScore:      0.7,
		AvgLift:             0.4,
		TotalBaselineTokens: 100,
		TotalCortexTokens:   80,
		AvgTokenReduction:   0.2,
		AvgABR:              0.85,
		PassRate:            1.0,
		Pass:                true,
	}

	err := p.Persist(results, 1000)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// Verify token data was stored
	var totalBaseline, totalCortex int
	var avgTokenReduction, avgABR float64
	err = p.db.QueryRow(`
		SELECT total_baseline_tokens, total_cortex_tokens, avg_token_reduction, avg_abr
		FROM eval_runs ORDER BY timestamp DESC LIMIT 1
	`).Scan(&totalBaseline, &totalCortex, &avgTokenReduction, &avgABR)
	if err != nil {
		t.Fatalf("query run: %v", err)
	}

	if totalBaseline != 100 {
		t.Errorf("expected baseline tokens 100, got %d", totalBaseline)
	}
	if totalCortex != 80 {
		t.Errorf("expected cortex tokens 80, got %d", totalCortex)
	}
	if avgTokenReduction != 0.2 {
		t.Errorf("expected token reduction 0.2, got %.2f", avgTokenReduction)
	}
	if avgABR != 0.85 {
		t.Errorf("expected avg ABR 0.85, got %.2f", avgABR)
	}

	// Verify scenario token data
	var scenarioBaseline, scenarioCortex int
	var scenarioReduction float64
	err = p.db.QueryRow(`
		SELECT avg_baseline_tokens, avg_cortex_tokens, token_reduction
		FROM eval_scenario_results ORDER BY id DESC LIMIT 1
	`).Scan(&scenarioBaseline, &scenarioCortex, &scenarioReduction)
	if err != nil {
		t.Fatalf("query scenario: %v", err)
	}

	if scenarioBaseline != 100 {
		t.Errorf("expected scenario baseline tokens 100, got %d", scenarioBaseline)
	}
	if scenarioCortex != 80 {
		t.Errorf("expected scenario cortex tokens 80, got %d", scenarioCortex)
	}
}

func TestPersistWithComparison(t *testing.T) {
	p := newTestPersister(t)

	results := &Results{
		Provider: "ollama",
		Model:    "qwen2:0.5b",
		Scenarios: []ScenarioResult{
			{
				ScenarioID:        "mpr-test",
				Name:              "MPR Test",
				Tests:             []TestResult{{TestID: "t1", HasCompare: true, CompareScore: 0.85, CompareTokens: 200, MPR: 0.94}},
				AvgBaselineScore:  0.5,
				AvgCortexScore:    0.8,
				AvgLift:           0.6,
				HasCompare:        true,
				AvgCompareScore:   0.85,
				AvgMPR:            0.94,
				AvgBaselineTokens: 100,
				AvgCortexTokens:   120,
				TokenReduction:    -0.2,
				Pass:              true,
			},
		},
		AvgBaselineScore:    0.5,
		AvgCortexScore:      0.8,
		AvgLift:             0.6,
		CompareProvider:     "anthropic",
		CompareModel:        "claude-haiku-4-5-20251001",
		AvgCompareScore:     0.85,
		AvgMPR:              0.94,
		TotalCompareTokens:  200,
		TotalBaselineTokens: 100,
		TotalCortexTokens:   120,
		PassRate:            1.0,
		Pass:                true,
	}

	err := p.Persist(results, 1500)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// Verify comparison data in eval_runs
	var compareProvider, compareModel string
	var avgCompareScore, avgMPR float64
	var totalCompareTokens int
	err = p.db.QueryRow(`
		SELECT COALESCE(compare_provider, ''), COALESCE(compare_model, ''), COALESCE(avg_compare_score, 0), COALESCE(avg_mpr, 0), COALESCE(total_compare_tokens, 0)
		FROM eval_runs ORDER BY timestamp DESC LIMIT 1
	`).Scan(&compareProvider, &compareModel, &avgCompareScore, &avgMPR, &totalCompareTokens)
	if err != nil {
		t.Fatalf("query run: %v", err)
	}

	if compareProvider != "anthropic" {
		t.Errorf("expected compare provider 'anthropic', got %q", compareProvider)
	}
	if compareModel != "claude-haiku-4-5-20251001" {
		t.Errorf("expected compare model 'claude-haiku-4-5-20251001', got %q", compareModel)
	}
	if avgCompareScore != 0.85 {
		t.Errorf("expected avg compare score 0.85, got %.2f", avgCompareScore)
	}
	if avgMPR != 0.94 {
		t.Errorf("expected avg MPR 0.94, got %.2f", avgMPR)
	}
	if totalCompareTokens != 200 {
		t.Errorf("expected total compare tokens 200, got %d", totalCompareTokens)
	}

	// Verify scenario comparison data
	var scenarioCompareScore, scenarioMPR float64
	err = p.db.QueryRow(`
		SELECT COALESCE(avg_compare_score, 0), COALESCE(avg_mpr, 0)
		FROM eval_scenario_results ORDER BY id DESC LIMIT 1
	`).Scan(&scenarioCompareScore, &scenarioMPR)
	if err != nil {
		t.Fatalf("query scenario: %v", err)
	}

	if scenarioCompareScore != 0.85 {
		t.Errorf("expected scenario compare score 0.85, got %.2f", scenarioCompareScore)
	}
	if scenarioMPR != 0.94 {
		t.Errorf("expected scenario MPR 0.94, got %.2f", scenarioMPR)
	}
}

func TestPersistAndGetLatest(t *testing.T) {
	p := newTestPersister(t)

	// Use unique timestamp to avoid collision
	ts := time.Now().Format("20060102-150405")
	_ = ts

	results := &Results{
		Provider:          "mock",
		Model:             "test",
		Scenarios:         []ScenarioResult{},
		AvgBaselineScore:  0.5,
		AvgCortexScore:    0.7,
		AvgLift:           0.4,
		TotalCortexWins:   3,
		TotalBaselineWins: 1,
		TotalTies:         1,
		PassRate:          1.0,
		Pass:              true,
	}

	err := p.Persist(results, 500)
	if err != nil {
		// If there's a duplicate key, just use a different approach
		// Since Persist generates its own ID from time.Now(), this should be unique
		t.Fatalf("Persist: %v", err)
	}

	latest, err := p.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil latest result")
	}
	if latest.Provider != "mock" {
		t.Errorf("expected provider 'mock', got %q", latest.Provider)
	}
	if latest.AvgLift != 0.4 {
		t.Errorf("expected lift 0.4, got %.2f", latest.AvgLift)
	}
}

func TestGetTrend(t *testing.T) {
	p := newTestPersister(t)

	// Insert runs with different lifts
	for i := 0; i < 5; i++ {
		lift := float64(i) * 0.1
		id := fmt.Sprintf("trend-%d", i)
		ts := fmt.Sprintf("2026-01-%02dT00:00:00Z", i+1)
		_, err := p.db.Exec(`
			INSERT INTO eval_runs (id, timestamp, provider, model, avg_lift, cortex_wins, baseline_wins, ties, pass_rate, pass, scenarios_json)
			VALUES (?, ?, 'mock', 'test', ?, 0, 0, 0, 1.0, 1, '[]')
		`, id, ts, lift)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	lifts, err := p.GetTrend(3)
	if err != nil {
		t.Fatalf("GetTrend: %v", err)
	}

	if len(lifts) != 3 {
		t.Fatalf("expected 3 lifts, got %d", len(lifts))
	}

	// Should be chronological: 0.2, 0.3, 0.4
	if lifts[0] != 0.2 {
		t.Errorf("expected first lift 0.2, got %.2f", lifts[0])
	}
	if lifts[2] != 0.4 {
		t.Errorf("expected last lift 0.4, got %.2f", lifts[2])
	}
}
