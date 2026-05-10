package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

// runGridReport prints the most recent N CellResult rows from the
// JSONL append log. The JSONL log is the canonical analysis source
// (hard constraint #8); SQLite is a parallel store for ad-hoc queries.
//
// Output is a fixed-width table on stdout — analysts can pipe to
// `column -t` or pull richer queries from sqlite3 directly.
func runGridReport(limit int) error {
	p, err := evalv2.NewPersister()
	if err != nil {
		return fmt.Errorf("init persister: %w", err)
	}
	defer p.Close()

	cells, err := p.RecentCellsFromJSONL(limit)
	if err != nil {
		return fmt.Errorf("read cell_results.jsonl: %w", err)
	}
	if len(cells) == 0 {
		fmt.Println("no cell_results yet — run `cortex eval grid --models <id>` first")
		return nil
	}

	fmt.Printf("%-30s %-20s %-10s %-35s %-10s %5s %5s %9s %7s %3s\n",
		"run_id", "scenario", "harness", "model", "strategy",
		"in", "out", "cost_usd", "lat_ms", "ok")
	fmt.Println(strings.Repeat("-", 140))
	for _, c := range cells {
		ok := "no"
		if c.TaskSuccess {
			ok = "yes"
		}
		fmt.Printf("%-30s %-20s %-10s %-35s %-10s %5d %5d $%.6f %7d %3s\n",
			truncate(c.RunID, 30),
			truncate(c.ScenarioID, 20),
			c.Harness,
			truncate(c.Model, 35),
			c.ContextStrategy,
			c.TokensIn, c.TokensOut,
			c.CostUSD, c.LatencyMs, ok)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// executeGrid handles `cortex eval grid <flags>`. Builds the grid
// dimensions, validates that every requested harness binary is present,
// and drives evalv2.RunGrid.
//
// Per the eval-harness loop's TODO 7: scope is limited to aider as the
// only supported harness. opencode + pi.dev are deferred to TODOs 10/11.
func executeGrid(args []string) error {
	scenarioDir := "test/evals/v2"
	harnessesCSV := evalv2.HarnessAider
	provider := evalv2.ProviderOpenRouter
	modelsCSV := ""
	strategiesCSV := evalv2.StrategyBaseline + "," + evalv2.StrategyCortex
	showHelp := false
	reportOnly := false
	reportLimit := 20

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			showHelp = true
		case "--report":
			reportOnly = true
		case "--report-limit":
			if i+1 < len(args) {
				if v, err := strconv.Atoi(args[i+1]); err == nil && v > 0 {
					reportLimit = v
				}
				i++
			}
		case "--scenarios":
			if i+1 < len(args) {
				scenarioDir = args[i+1]
				i++
			}
		case "--harnesses":
			if i+1 < len(args) {
				harnessesCSV = args[i+1]
				i++
			}
		case "--provider":
			if i+1 < len(args) {
				provider = args[i+1]
				i++
			}
		case "--models":
			if i+1 < len(args) {
				modelsCSV = args[i+1]
				i++
			}
		case "--strategies":
			if i+1 < len(args) {
				strategiesCSV = args[i+1]
				i++
			}
		default:
			return fmt.Errorf("eval grid: unknown flag %q (try --help)", args[i])
		}
	}

	if showHelp {
		printGridHelp()
		return nil
	}

	if reportOnly {
		return runGridReport(reportLimit)
	}

	if modelsCSV == "" {
		return fmt.Errorf("eval grid: --models is required (csv of model IDs, e.g. --models openai/gpt-oss-20b:free)")
	}

	// Resolve harnesses with binary checks up front — fail fast.
	harnesses, err := buildGridHarnesses(harnessesCSV)
	if err != nil {
		return err
	}

	models, err := buildGridModels(provider, modelsCSV)
	if err != nil {
		return err
	}

	strategies, err := buildGridStrategies(strategiesCSV)
	if err != nil {
		return err
	}

	// OpenRouter API key check (only when openrouter is the provider).
	if provider == evalv2.ProviderOpenRouter && os.Getenv("OPEN_ROUTER_API_KEY") == "" {
		return fmt.Errorf("eval grid: OPEN_ROUTER_API_KEY not set (required for provider=openrouter)")
	}

	scenarios, err := evalv2.LoadAll(scenarioDir)
	if err != nil {
		return fmt.Errorf("load scenarios from %q: %w", scenarioDir, err)
	}
	if len(scenarios) == 0 {
		return fmt.Errorf("no scenarios found in %q", scenarioDir)
	}

	persister, err := evalv2.NewPersister()
	if err != nil {
		return fmt.Errorf("init persister: %w", err)
	}
	defer persister.Close()

	totalCells := len(scenarios) * len(harnesses) * len(models) * len(strategies)
	fmt.Fprintf(os.Stderr, "running %d cells: %d scenarios × %d harnesses × %d models × %d strategies\n",
		totalCells, len(scenarios), len(harnesses), len(models), len(strategies))

	results, err := evalv2.RunGrid(context.Background(), persister, scenarios, harnesses, models, strategies)
	fmt.Fprintf(os.Stderr, "completed %d/%d cells\n", len(results), totalCells)
	if err != nil {
		return fmt.Errorf("RunGrid: %w", err)
	}

	// Summary to stdout (analysis-friendly).
	var totalIn, totalOut int
	var totalCost float64
	var passes int
	for _, r := range results {
		totalIn += r.TokensIn
		totalOut += r.TokensOut
		totalCost += r.CostUSD
		if r.TaskSuccess {
			passes++
		}
	}
	fmt.Printf("cells=%d  tokens_in=%d  tokens_out=%d  total_cost_usd=%.6f  passes=%d/%d\n",
		len(results), totalIn, totalOut, totalCost, passes, len(results))

	return nil
}

func buildGridHarnesses(csv string) ([]evalv2.HarnessSpec, error) {
	names := splitCSV(csv)
	if len(names) == 0 {
		return nil, fmt.Errorf("eval grid: --harnesses produced empty list")
	}

	out := make([]evalv2.HarnessSpec, 0, len(names))
	for _, name := range names {
		switch name {
		case evalv2.HarnessAider:
			// NewAiderHarness verifies the binary is on PATH
			// ($AIDER_BINARY env override is honored). The model is
			// re-pointed per cell via SetModel; passing "" here is fine.
			h, err := evalv2.NewAiderHarness("", "")
			if err != nil {
				return nil, fmt.Errorf("eval grid: aider harness unavailable: %w", err)
			}
			out = append(out, evalv2.HarnessSpec{Name: name, Harness: h})
		case evalv2.HarnessOpenCode, evalv2.HarnessPiDev:
			return nil, fmt.Errorf("eval grid: harness %q not yet wired in CLI (deferred to eval-harness-loop TODOs 10/11)", name)
		case evalv2.HarnessClaudeCLI:
			return nil, fmt.Errorf("eval grid: harness %q not exposed via grid yet — use the legacy `cortex eval` for claude-cli runs", name)
		default:
			return nil, fmt.Errorf("eval grid: unknown harness %q (valid: aider)", name)
		}
	}
	return out, nil
}

func buildGridModels(provider, csv string) ([]evalv2.ModelSpec, error) {
	switch provider {
	case evalv2.ProviderOpenRouter, evalv2.ProviderOllama, evalv2.ProviderAnthropic, evalv2.ProviderOpenAI, evalv2.ProviderLocal:
	default:
		return nil, fmt.Errorf("eval grid: unknown provider %q (valid: openrouter, ollama, anthropic, openai, local)", provider)
	}

	names := splitCSV(csv)
	if len(names) == 0 {
		return nil, fmt.Errorf("eval grid: --models produced empty list")
	}
	out := make([]evalv2.ModelSpec, 0, len(names))
	for _, m := range names {
		out = append(out, evalv2.ModelSpec{Provider: provider, Model: m})
	}
	return out, nil
}

func buildGridStrategies(csv string) ([]evalv2.ContextStrategy, error) {
	names := splitCSV(csv)
	if len(names) == 0 {
		return nil, fmt.Errorf("eval grid: --strategies produced empty list")
	}
	out := make([]evalv2.ContextStrategy, 0, len(names))
	for _, s := range names {
		switch s {
		case evalv2.StrategyBaseline, evalv2.StrategyCortex, evalv2.StrategyFrontier:
		default:
			return nil, fmt.Errorf("eval grid: unknown strategy %q (valid: baseline, cortex, frontier)", s)
		}
		out = append(out, evalv2.ContextStrategy(s))
	}
	return out, nil
}

func splitCSV(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printGridHelp() {
	fmt.Println(`Usage: cortex eval grid [options]

Cross-harness × model × strategy eval grid. Drives one CellResult per
cell into both .cortex/db/evals_v2.db and .cortex/db/cell_results.jsonl.

Options:
  --scenarios DIR          Scenario directory (default: test/evals/v2)
  --harnesses LIST         CSV of harness names (default: aider)
                           Currently supported: aider
                           Deferred: opencode, pi_dev (eval-harness loop
                           TODOs 10/11)
  --provider NAME          Provider for all models in this run
                           (default: openrouter)
                           Valid: openrouter, ollama, anthropic, openai, local
  --models LIST            CSV of model IDs (REQUIRED). Pass verbatim to
                           the harness/provider, e.g.
                             openai/gpt-oss-20b:free
                             qwen/qwen3-coder
                             anthropic/claude-haiku-4.5
  --strategies LIST        CSV of context strategies
                           (default: baseline,cortex)
                           Valid: baseline, cortex, frontier
  --report                 Print the last N CellResult rows from the
                           JSONL log; do not run any cells.
  --report-limit N         How many rows --report shows (default: 20)
  -h, --help               Show this help

Environment:
  OPEN_ROUTER_API_KEY      Required when --provider=openrouter (note the
                           underscore — Aider/litellm uses the canonical
                           OPENROUTER_API_KEY internally; the harness
                           re-exports automatically).

Examples:
  cortex eval grid --models openai/gpt-oss-20b:free
  cortex eval grid \
    --scenarios test/evals/v2 \
    --harnesses aider \
    --provider openrouter \
    --models openai/gpt-oss-20b:free,qwen/qwen3-coder \
    --strategies baseline,cortex`)
}
