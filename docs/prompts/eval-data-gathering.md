# Eval Data Gathering Session

## Goal
Run a comprehensive set of evals across different models and eval types to establish baseline performance data. Use parallel subagents where resources allow. Visualize results in Grafana.

## Prerequisites

### Start Grafana (if not running)
```bash
# Install if needed
brew install grafana

# Start service
brew services start grafana

# Access at http://localhost:3000 (admin/admin)
```

### Configure Grafana SQLite Datasource
1. Go to Connections > Data Sources > Add data source
2. Search for "SQLite" (install plugin if needed: `grafana-cli plugins install frser-sqlite-datasource`)
3. Configure:
   - Name: `cortex-evals`
   - Path: `~/.cortex/db/evals.db` (or `$PROJECT_ROOT/.cortex/db/evals.db` if using a per-project store)
4. Save & Test

## Models to Test

**Anthropic:**
- claude-3-5-haiku-20241022 (fast, cheap)

**Ollama (local):**
- qwen2.5-coder:1.5b (fast)
- mistral:7b (balanced)

## Evals to Run

### E2E Journeys (test real task completion)
Run each journey with each model:
- `test/evals/journeys/trivial-hello-world.yaml` (baseline, ~30s)
- `test/evals/journeys/small-refactor.yaml` (~1-2min)
- `test/evals/journeys/medium-logging.yaml` (~2-3min)

### Cognition Evals (test retrieval quality)
Run once per model (covers Reflex, Reflect, Think, Dream modes):
```bash
./cortex eval --cognition -p <provider> -m <model>
```

## Execution Plan

**Phase 1 - Run in parallel:**
- **Agent 1**: Run all E2E journeys with `claude-3-5-haiku-20241022` (Anthropic API)
- **Agent 2**: Run all E2E journeys with `qwen2.5-coder:1.5b` (Ollama local)

**Phase 2 - After Agent 2 completes:**
- **Agent 3**: Run all E2E journeys with `mistral:7b` (Ollama local)

**Phase 3 - After all E2E complete:**
- **Agent 4**: Run cognition evals with each model sequentially

This prevents CPU contention between Ollama models while still parallelizing Anthropic (API) with Ollama (local).

## Commands

E2E journey:
```bash
./cortex eval -t e2e --journey <path> -p <provider> -m <model> -v
```

Cognition:
```bash
./cortex eval --cognition -p <provider> -m <model> -v
```

## After Completion

### 1. Verify Data Collection
```bash
sqlite3 -header -column .cortex/db/evals.db "
SELECT model, run_type, COUNT(*) as runs,
       ROUND(AVG(pass_rate)*100,1) as pass_pct
FROM eval_runs
GROUP BY model, run_type
ORDER BY model, run_type;"
```

### 2. Create Grafana Dashboard

Create a new dashboard with these panels:

#### Panel 1: Pass Rate by Model (Bar Chart)
```sql
SELECT model,
       ROUND(AVG(pass_rate)*100, 1) as pass_rate_pct
FROM eval_runs
WHERE model IS NOT NULL AND model != ''
GROUP BY model
ORDER BY pass_rate_pct DESC
```

#### Panel 2: Pass Rate Over Time (Time Series)
```sql
SELECT timestamp as time,
       model,
       pass_rate * 100 as pass_rate_pct
FROM eval_runs
WHERE model IS NOT NULL
ORDER BY timestamp
```

#### Panel 3: Model Comparison by Eval Type (Grouped Bar)
```sql
SELECT model,
       run_type,
       ROUND(AVG(pass_rate)*100, 1) as pass_rate_pct,
       COUNT(*) as runs
FROM eval_runs
WHERE model IS NOT NULL AND model != ''
GROUP BY model, run_type
ORDER BY model, run_type
```

#### Panel 4: E2E Lift by Model (Bar Chart)
```sql
SELECT model,
       ROUND(AVG(overall_lift)*100, 1) as avg_lift_pct
FROM eval_runs
WHERE run_type = 'e2e' AND model IS NOT NULL
GROUP BY model
ORDER BY avg_lift_pct DESC
```

#### Panel 5: Journey Performance (Table)
```sql
SELECT journey_id,
       model,
       CASE WHEN pass_rate >= 0.5 THEN 'PASS' ELSE 'FAIL' END as status,
       ROUND(overall_lift*100, 1) as lift_pct,
       datetime(timestamp) as run_time
FROM eval_runs
WHERE run_type = 'e2e'
ORDER BY timestamp DESC
LIMIT 50
```

#### Panel 6: Statistical Significance (Table)
```sql
SELECT r.model,
       s.n as samples,
       ROUND(s.mean_delta, 3) as mean_delta,
       s.effect_size,
       ROUND(s.p_value, 4) as p_value,
       CASE WHEN s.significant THEN 'Yes' ELSE 'No' END as significant
FROM eval_runs r
JOIN eval_statistics s ON r.id = s.run_id
WHERE r.model IS NOT NULL
ORDER BY s.p_value ASC
```

### 3. Summary Report

Create a summary comparing models across:
- E2E pass rate (% of journeys passed)
- E2E average lift (Cortex improvement %)
- Cognition pass rate (% of mode tests passed)
- Statistical significance (how many runs show p < 0.05)
- Notable patterns or consistent failures

## Notes

- Results auto-persist to SQLite (`.cortex/db/evals.db`) and JSONL (`.cortex/evals/`)
- Grafana dashboard updates automatically as new eval runs complete
- Ollama agents run sequentially to avoid CPU contention
- Anthropic agents can run in parallel (uses API, not local resources)
- For quick data exploration, Datasette is also available:
  ```bash
  datasette serve .cortex/db/evals.db -m .cortex/datasette.yaml --open
  ```
