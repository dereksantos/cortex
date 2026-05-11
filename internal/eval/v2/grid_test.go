//go:build !windows

package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gridFakeHarness is a deterministic, threadsafe fake that records each
// invocation and returns a canned HarnessResult. Implements Harness,
// ResultfulHarness, and the SetModel assertion the runner uses.
type gridFakeHarness struct {
	mu        sync.Mutex
	calls     []gridFakeCall
	res       HarnessResult
	lastModel string
}

type gridFakeCall struct {
	prompt  string
	workdir string
}

func (f *gridFakeHarness) RunSession(_ context.Context, prompt, workdir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, gridFakeCall{prompt: prompt, workdir: workdir})
	return nil
}

func (f *gridFakeHarness) RunSessionWithResult(_ context.Context, prompt, workdir string) (HarnessResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, gridFakeCall{prompt: prompt, workdir: workdir})
	return f.res, nil
}

// SetModel is recorded by gridFakeHarness so tests can verify the
// per-cell model-setter assertion fires before each call.
func (f *gridFakeHarness) SetModel(m string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastModel = m
}

// Compile-time guards: gridFakeHarness satisfies both interfaces.
var (
	_ Harness          = (*gridFakeHarness)(nil)
	_ ResultfulHarness = (*gridFakeHarness)(nil)
)

func TestRunGrid_8Cells(t *testing.T) {
	p := newTestPersister(t)

	scenarios := []*Scenario{
		{ID: "s-one", Name: "Scenario One", Tests: []Test{{Query: "do thing 1"}}},
		{ID: "s-two", Name: "Scenario Two", Tests: []Test{{Query: "do thing 2"}}},
	}
	fake := &gridFakeHarness{
		res: HarnessResult{
			TokensIn:        100,
			TokensOut:       50,
			CostUSD:         0.001,
			AgentTurnsTotal: 3,
			LatencyMs:       1234,
			ProviderEcho:    ProviderOpenRouter,
			ModelEcho:       "openai/gpt-oss-20b:free",
		},
	}
	harnesses := []HarnessSpec{
		{Name: HarnessAider, Harness: fake},
	}
	models := []ModelSpec{
		{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"},
		{Provider: ProviderOpenRouter, Model: "qwen/qwen3-coder"},
	}
	strategies := []ContextStrategy{StrategyBaseline, StrategyCortex}

	results, err := RunGrid(context.Background(), p, scenarios, harnesses, models, strategies)
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}

	// 2 scenarios × 1 harness × 2 models × 2 strategies = 8 cells
	const want = 8
	if len(results) != want {
		t.Errorf("len(results)=%d, want %d", len(results), want)
	}
	if len(fake.calls) != want {
		t.Errorf("fake harness called %d times, want %d", len(fake.calls), want)
	}

	// SQLite row count
	var sqliteCount int
	if err := p.db.QueryRow("SELECT COUNT(*) FROM cell_results").Scan(&sqliteCount); err != nil {
		t.Fatalf("sqlite count: %v", err)
	}
	if sqliteCount != want {
		t.Errorf("sqlite count=%d, want %d", sqliteCount, want)
	}

	// JSONL line count
	lines := readJSONL(t, p.cellResultsJSONLPath())
	if len(lines) != want {
		t.Errorf("jsonl lines=%d, want %d", len(lines), want)
	}

	// Run IDs unique
	seen := make(map[string]bool, want)
	for _, c := range results {
		if seen[c.RunID] {
			t.Errorf("duplicate RunID: %q", c.RunID)
		}
		seen[c.RunID] = true
	}

	// Every (scenario, harness, provider, model, strategy) tuple shows up
	// exactly once.
	type cellKey struct {
		scenario, harness, provider, model, strategy string
	}
	keys := make(map[cellKey]int)
	for _, c := range results {
		k := cellKey{c.ScenarioID, c.Harness, c.Provider, c.Model, c.ContextStrategy}
		keys[k]++
	}
	if len(keys) != want {
		t.Errorf("unique cell tuples=%d, want %d", len(keys), want)
	}
	for k, n := range keys {
		if n != 1 {
			t.Errorf("cell %+v appeared %d times, want 1", k, n)
		}
	}

	// HarnessResult fields propagate into CellResult.
	for _, c := range results {
		if c.TokensIn != 100 || c.TokensOut != 50 {
			t.Errorf("tokens not propagated: %+v", c)
		}
		if c.CostUSD != 0.001 {
			t.Errorf("CostUSD=%v want 0.001", c.CostUSD)
		}
		if c.AgentTurnsTotal != 3 {
			t.Errorf("AgentTurnsTotal=%d want 3", c.AgentTurnsTotal)
		}
		if c.LatencyMs != 1234 {
			t.Errorf("LatencyMs=%d want 1234", c.LatencyMs)
		}
	}

	// Cortex cells must carry CortexVersion; baselines must not.
	for _, c := range results {
		switch c.ContextStrategy {
		case StrategyCortex:
			if c.CortexVersion == "" {
				t.Errorf("cortex cell missing CortexVersion: run_id=%s", c.RunID)
			}
		case StrategyBaseline:
			if c.CortexVersion != "" {
				t.Errorf("baseline cell has CortexVersion=%q; want empty", c.CortexVersion)
			}
		}
	}

	// Prompt forwarded to the harness comes from scenarioToPrompt — it
	// should at least include each scenario's name in the calls.
	prompts := make(map[string]int)
	for _, call := range fake.calls {
		prompts[call.prompt]++
	}
	if len(prompts) != 2 {
		t.Errorf("got %d distinct prompts, want 2 (one per scenario)", len(prompts))
	}
}

// TestRunGrid_SetModelFiresPerCell verifies the runner re-points the
// harness's model before each cell. AiderHarness needs this; the fake
// implements SetModel for verification.
func TestRunGrid_SetModelFiresPerCell(t *testing.T) {
	p := newTestPersister(t)
	fake := &gridFakeHarness{}

	models := []ModelSpec{
		{Provider: ProviderOpenRouter, Model: "model-a"},
		{Provider: ProviderOpenRouter, Model: "model-b"},
	}

	_, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		models,
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}

	// Last cell ran model-b. The grid prepends the provider prefix
	// (litellm convention) before passing to SetModel, so the harness
	// sees "openrouter/model-b". CellResult.Model keeps the canonical
	// form for grid aggregation.
	if fake.lastModel != "openrouter/model-b" {
		t.Errorf("lastModel=%q want %q (final cell should have re-pointed)", fake.lastModel, "openrouter/model-b")
	}
}

func TestRunGrid_EmptyDimensions(t *testing.T) {
	p := newTestPersister(t)
	fake := &gridFakeHarness{}
	scn := []*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}}
	hs := []HarnessSpec{{Name: HarnessAider, Harness: fake}}
	mods := []ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}}
	strat := []ContextStrategy{StrategyBaseline}

	cases := []struct {
		name      string
		scenarios []*Scenario
		harnesses []HarnessSpec
		models    []ModelSpec
		strats    []ContextStrategy
		wantErr   string
	}{
		{"no scenarios", nil, hs, mods, strat, "no scenarios"},
		{"no harnesses", scn, nil, mods, strat, "no harnesses"},
		{"no models", scn, hs, nil, strat, "no models"},
		{"no strategies", scn, hs, mods, nil, "no strategies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RunGrid(context.Background(), p, tc.scenarios, tc.harnesses, tc.models, tc.strats)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err=%v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunGrid_NilPersister(t *testing.T) {
	_, err := RunGrid(context.Background(), nil,
		[]*Scenario{{ID: "x"}},
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "x"}},
		[]ContextStrategy{StrategyBaseline})
	if err == nil {
		t.Fatal("want error for nil persister, got nil")
	}
	if !strings.Contains(err.Error(), "persister is nil") {
		t.Errorf("err=%v, want 'persister is nil'", err)
	}
}

// TestRunGrid_FallsBackToBareHarness verifies the type-assertion
// fallback path: a Harness that does NOT implement ResultfulHarness
// still produces a CellResult with at least LatencyMs synthesized.
func TestRunGrid_FallsBackToBareHarness(t *testing.T) {
	p := newTestPersister(t)

	// bareHarness comes from harness_test.go — same package.
	bare := bareHarness{}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "scn", Name: "scn", Tests: []Test{{Query: "q"}}}},
		[]HarnessSpec{{Name: HarnessClaudeCLI, Harness: bare}},
		[]ModelSpec{{Provider: ProviderAnthropic, Model: "claude-haiku-4.5"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results)=%d want 1", len(results))
	}
	c := results[0]
	if c.LatencyMs <= 0 {
		t.Errorf("bare harness path should synthesize LatencyMs, got %d", c.LatencyMs)
	}
	if c.TokensIn != 0 || c.TokensOut != 0 {
		t.Errorf("bare harness path should leave tokens=0, got in=%d out=%d", c.TokensIn, c.TokensOut)
	}
	if !c.TaskSuccess {
		t.Errorf("bare harness returned nil err but TaskSuccess=false")
	}
}

func TestNewRunID_UniqueAndPrefixed(t *testing.T) {
	const n = 100
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := newRunID()
		if !strings.HasPrefix(id, "cell-") {
			t.Errorf("id %q missing cell- prefix", id)
		}
		if seen[id] {
			t.Errorf("duplicate id %q across %d generations", id, n)
		}
		seen[id] = true
	}
}

// gridFixedCostHarness is a fake that returns a configurable per-call
// cost. Used to drive deterministic ceiling-trip tests.
type gridFixedCostHarness struct {
	cost float64
}

func (h *gridFixedCostHarness) RunSession(_ context.Context, _ string, _ string) error {
	return nil
}

func (h *gridFixedCostHarness) RunSessionWithResult(_ context.Context, _ string, _ string) (HarnessResult, error) {
	return HarnessResult{
		TokensIn:        100,
		TokensOut:       50,
		CostUSD:         h.cost,
		AgentTurnsTotal: 1,
		LatencyMs:       1,
	}, nil
}

// TestRunGrid_RunCeilingTripsAfterNCells exercises requirement (a) of
// TODO 8's done criterion: a run-ceiling trip with a fake provider
// returning fixed cost_usd, abort with partial CSV.
func TestRunGrid_RunCeilingTripsAfterNCells(t *testing.T) {
	p := newTestPersister(t)

	// $1.00 ceiling. Each cell costs $0.30; estimator picks up the
	// observed cost on subsequent cells. After 3 cells run total is
	// $0.90; the 4th cell's estimate of $0.30 → $1.20 > $1.00 trips.
	// Numbers chosen so float drift can't push the boundary either way.
	t.Setenv(EnvRunUSDCeiling, "1.00")
	t.Setenv(EnvDailyUSDCeiling, "100")
	t.Setenv(EnvLifetimeUSDCeiling, "100")
	t.Setenv(EnvNoFreePreference, "1") // don't auto-swap to :free for this test

	fake := &gridFixedCostHarness{cost: 0.30}
	harnesses := []HarnessSpec{{Name: HarnessAider, Harness: fake}}
	models := []ModelSpec{{Provider: ProviderOpenRouter, Model: "qwen/qwen3-coder"}}
	strategies := []ContextStrategy{StrategyBaseline}

	scenarios := make([]*Scenario, 0, 6)
	for i := 0; i < 6; i++ {
		scenarios = append(scenarios, &Scenario{ID: fmt.Sprintf("scn-%d", i), Tests: []Test{{Query: "q"}}})
	}

	results, err := RunGrid(context.Background(), p, scenarios, harnesses, models, strategies)
	if err == nil {
		t.Fatalf("expected ceiling-trip error, got nil; results=%d", len(results))
	}
	if !strings.Contains(err.Error(), "run ceiling") {
		t.Errorf("err=%v, want 'run ceiling'", err)
	}

	// 3 cells should have completed (3 × $0.30 = $0.90; 4th cell's
	// estimate of $0.30 would push to $1.20 > $1.00 → abort).
	if len(results) != 3 {
		t.Errorf("len(results)=%d, want 3 (ceiling should trip on 4th cell)", len(results))
	}

	// Partial CSV file should exist in dbDir.
	matches, _ := filepath.Glob(filepath.Join(p.dbDir, "*.partial.csv"))
	if len(matches) == 0 {
		t.Errorf("no .partial.csv emitted in %s", p.dbDir)
	}
}

// TestRunGrid_LifetimeCeilingPersistsAcrossRuns exercises requirement
// (b) of TODO 8: lifetime spend accumulates across two RunGrid() calls
// via the daily_spend SQLite table.
func TestRunGrid_LifetimeCeilingPersistsAcrossRuns(t *testing.T) {
	p := newTestPersister(t)

	// Lifetime ceiling = $0.45. Each cell costs $0.20.
	// First RunGrid: 2 cells = $0.40 spent, third cell estimate ($0.20)
	//   would push to $0.60 > $0.45 → abort.
	// Each scenario × strategy + 1 model + 1 harness = N cells.
	t.Setenv(EnvRunUSDCeiling, "100")
	t.Setenv(EnvDailyUSDCeiling, "100")
	t.Setenv(EnvLifetimeUSDCeiling, "0.45")
	t.Setenv(EnvNoFreePreference, "1")

	fake := &gridFixedCostHarness{cost: 0.20}
	harnesses := []HarnessSpec{{Name: HarnessAider, Harness: fake}}
	models := []ModelSpec{{Provider: ProviderOpenRouter, Model: "qwen/qwen3-coder"}}
	strategies := []ContextStrategy{StrategyBaseline}

	// Run 1: 3 scenarios → 3 cells but ceiling stops it at 2.
	scenarios := []*Scenario{
		{ID: "a", Tests: []Test{{Query: "q"}}},
		{ID: "b", Tests: []Test{{Query: "q"}}},
		{ID: "c", Tests: []Test{{Query: "q"}}},
	}
	r1, err := RunGrid(context.Background(), p, scenarios, harnesses, models, strategies)
	if err == nil {
		t.Fatalf("run 1: expected lifetime ceiling trip, got nil")
	}
	if !strings.Contains(err.Error(), "lifetime ceiling") {
		t.Errorf("run 1 err=%v, want 'lifetime ceiling'", err)
	}
	if len(r1) != 2 {
		t.Errorf("run 1 cells=%d, want 2", len(r1))
	}

	// Run 2: empty start of run-spend, but lifetime is already $0.40.
	// Even one more $0.20 cell pushes lifetime to $0.60 > $0.45 → abort
	// immediately (zero cells).
	scenarios = []*Scenario{{ID: "d", Tests: []Test{{Query: "q"}}}}
	r2, err := RunGrid(context.Background(), p, scenarios, harnesses, models, strategies)
	if err == nil {
		t.Fatalf("run 2: expected lifetime ceiling trip, got nil")
	}
	if !strings.Contains(err.Error(), "lifetime ceiling") {
		t.Errorf("run 2 err=%v, want 'lifetime ceiling'", err)
	}
	if len(r2) != 0 {
		t.Errorf("run 2 cells=%d, want 0 (lifetime already exhausted)", len(r2))
	}
}

// TestRunGrid_FreeTierPreferenceRoutes exercises requirement (c) of
// TODO 8: a paid model with a known `:free` variant gets auto-rewritten
// when the user passes the paid form, unless explicitly opted out.
func TestRunGrid_FreeTierPreferenceRoutes(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvRunUSDCeiling, "100")
	t.Setenv(EnvDailyUSDCeiling, "100")
	t.Setenv(EnvLifetimeUSDCeiling, "100")

	t.Run("auto-swaps paid → :free", func(t *testing.T) {
		t.Setenv(EnvNoFreePreference, "")
		fake := &gridFakeHarness{}
		_, err := RunGrid(context.Background(), p,
			[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}},
			[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
			[]ModelSpec{{Provider: ProviderOpenRouter, Model: "qwen/qwen3-coder"}}, // paid input
			[]ContextStrategy{StrategyBaseline})
		if err != nil {
			t.Fatalf("RunGrid: %v", err)
		}
		// SetModel should have been called with the :free variant
		// plus the provider prefix that litellm expects.
		if fake.lastModel != "openrouter/qwen/qwen3-coder:free" {
			t.Errorf("lastModel=%q, want auto-swap to %q", fake.lastModel, "openrouter/qwen/qwen3-coder:free")
		}
	})

	t.Run("opt-out keeps paid form", func(t *testing.T) {
		t.Setenv(EnvNoFreePreference, "1")
		fake := &gridFakeHarness{}
		_, err := RunGrid(context.Background(), p,
			[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}},
			[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
			[]ModelSpec{{Provider: ProviderOpenRouter, Model: "qwen/qwen3-coder"}},
			[]ContextStrategy{StrategyBaseline})
		if err != nil {
			t.Fatalf("RunGrid: %v", err)
		}
		if fake.lastModel != "openrouter/qwen/qwen3-coder" {
			t.Errorf("lastModel=%q, want %q (opt-out should leave paid form, with provider prefix)", fake.lastModel, "openrouter/qwen/qwen3-coder")
		}
	})
}

// TestRunGrid_FrontierGuardBlocksWithoutEnv exercises requirement (d)
// of TODO 8: a frontier model (Sonnet) is blocked unless
// CORTEX_EVAL_ALLOW_FRONTIER=1.
func TestRunGrid_FrontierGuardBlocksWithoutEnv(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvRunUSDCeiling, "100")
	t.Setenv(EnvDailyUSDCeiling, "100")
	t.Setenv(EnvLifetimeUSDCeiling, "100")
	t.Setenv(EnvNoFreePreference, "1")

	fake := &gridFakeHarness{}
	scenarios := []*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}}
	harnesses := []HarnessSpec{{Name: HarnessAider, Harness: fake}}
	models := []ModelSpec{{Provider: ProviderOpenRouter, Model: "anthropic/claude-sonnet-4.6"}}
	strategies := []ContextStrategy{StrategyBaseline}

	t.Run("blocked without env", func(t *testing.T) {
		t.Setenv(EnvAllowFrontier, "")
		results, err := RunGrid(context.Background(), p, scenarios, harnesses, models, strategies)
		if err == nil {
			t.Fatal("frontier model should have been blocked, got nil err")
		}
		if !strings.Contains(err.Error(), "frontier model") {
			t.Errorf("err=%v, want 'frontier model'", err)
		}
		if len(results) != 0 {
			t.Errorf("results=%d, want 0 (no cells should run)", len(results))
		}
	})

	t.Run("allowed with env", func(t *testing.T) {
		t.Setenv(EnvAllowFrontier, "1")
		results, err := RunGrid(context.Background(), p, scenarios, harnesses, models, strategies)
		if err != nil {
			t.Fatalf("frontier should pass with env set: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("results=%d, want 1", len(results))
		}
	})
}

// TestRunGrid_CortexInjection_AddsPrefixOnCortexStrategy: with
// strategy=cortex AND a non-empty CortexContext list, the harness sees
// a prompt that starts with the natural-language "Hints:" preamble +
// inlined bullets + the original prompt, and InjectedContextTokens is
// non-zero (capped by reported TokensIn).
//
// The "Hints:" form replaced an earlier "RELEVANT CONTEXT:\n- …\nTASK:\n"
// shape because the structured heading destabilized gpt-oss-20b's
// output channel on pi.dev — see
// docs/phase7-cortex-regression-diagnostic.md.
func TestRunGrid_CortexInjection_AddsPrefixOnCortexStrategy(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	// Fake reports 1000 tokens_in so the InjectedContextTokens cap
	// against TokensIn doesn't zero out our estimate.
	fake := &gridFakeHarness{res: HarnessResult{TokensIn: 1000, TokensOut: 50, AgentTurnsTotal: 1, LatencyMs: 1}}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{
			ID:    "x",
			Tests: []Test{{Query: "implement the function"}},
			CortexContext: []string{
				"Match existing test patterns",
				"Use t.Helper() in helpers",
			},
		}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyCortex})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls=%d want 1", len(fake.calls))
	}
	prompt := fake.calls[0].prompt
	if !strings.HasPrefix(prompt, "Hints: ") {
		t.Errorf("prompt did not start with 'Hints: ' preamble; got: %q", prompt)
	}
	if !strings.Contains(prompt, "Match existing test patterns") {
		t.Errorf("prompt missing first bullet")
	}
	if !strings.Contains(prompt, "Use t.Helper() in helpers") {
		t.Errorf("prompt missing second bullet")
	}
	if !strings.Contains(prompt, "\n\nimplement the function") {
		t.Errorf("prompt missing blank-line separator + original query, got: %q", prompt)
	}
	if results[0].InjectedContextTokens == 0 {
		t.Errorf("InjectedContextTokens=0 on cortex strategy with bullets, want > 0")
	}
}

// TestRunGrid_CortexInjection_BaselineSkipsPrefix: baseline strategy
// with the same CortexContext leaves the prompt as-is and reports
// InjectedContextTokens=0 — strategy is the only thing varying.
func TestRunGrid_CortexInjection_BaselineSkipsPrefix(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	fake := &gridFakeHarness{res: HarnessResult{TokensIn: 1000, TokensOut: 50, AgentTurnsTotal: 1, LatencyMs: 1}}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{
			ID:            "x",
			Tests:         []Test{{Query: "implement the function"}},
			CortexContext: []string{"Match existing patterns"},
		}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	prompt := fake.calls[0].prompt
	if strings.HasPrefix(prompt, "Hints: ") {
		t.Errorf("baseline prompt should NOT have cortex prefix; got: %q", prompt)
	}
	if results[0].InjectedContextTokens != 0 {
		t.Errorf("InjectedContextTokens=%d on baseline, want 0", results[0].InjectedContextTokens)
	}
}

// TestRunGrid_CortexInjection_EmptyContextNoOp: cortex strategy with
// no bullets behaves exactly like baseline — no prefix, no injected
// tokens. (Catches the regression where empty bullets still produce a
// bare "Hints:" header.)
func TestRunGrid_CortexInjection_EmptyContextNoOp(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	fake := &gridFakeHarness{res: HarnessResult{TokensIn: 1000, TokensOut: 50, AgentTurnsTotal: 1, LatencyMs: 1}}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{
			ID:    "x",
			Tests: []Test{{Query: "implement the function"}},
			// no CortexContext
		}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyCortex})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if strings.HasPrefix(fake.calls[0].prompt, "Hints: ") {
		t.Errorf("empty-context cortex prompt should be bare; got %q", fake.calls[0].prompt)
	}
	if results[0].InjectedContextTokens != 0 {
		t.Errorf("InjectedContextTokens=%d, want 0 with no bullets", results[0].InjectedContextTokens)
	}
}

// TestRunGrid_SeedDirCopiedIntoWorkdir: scenario.SeedDir contents land
// in the cell's workdir before the harness runs. We assert both
// directions: a Verify that greps the seeded file passes when seed_dir
// is set, and the marker file is observable from a fake harness's call.
func TestRunGrid_SeedDirCopiedIntoWorkdir(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	seedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(seedRoot, "MARKER.txt"), []byte("seed-ok"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(seedRoot, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedRoot, "sub", "child.txt"), []byte("child"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{
			ID:      "seeded",
			Tests:   []Test{{Query: "q"}},
			SeedDir: seedRoot,
			// Verify checks both the top-level marker and the subdir.
			Verify: "test -f MARKER.txt && grep -q seed-ok MARKER.txt && test -f sub/child.txt",
		}},
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if !results[0].TaskSuccess {
		t.Errorf("TaskSuccess=false; seed_dir copy failed or files missing. Notes: %s", results[0].Notes)
	}
}

// TestRunGrid_SeedDirMissing: a scenario pointing at a non-existent
// seed_dir fails the cell up-front (before the harness runs) with a
// clear error wrapped in the cell's err return path.
func TestRunGrid_SeedDirMissing(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	_, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}, SeedDir: "/nonexistent/seed/dir"}},
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err == nil {
		t.Fatal("want error for missing seed_dir, got nil")
	}
	if !strings.Contains(err.Error(), "seed workdir") {
		t.Errorf("err=%v, want 'seed workdir'", err)
	}
}

// flakyHarness fails its first N calls with a 429-style error then
// succeeds. Used to lock the retry-on-transient path.
type flakyHarness struct {
	failuresBefore int
	calls          int
	res            HarnessResult
	transientErr   error
}

func (h *flakyHarness) RunSession(_ context.Context, _ string, _ string) error {
	h.calls++
	if h.calls <= h.failuresBefore {
		return h.transientErr
	}
	return nil
}

func (h *flakyHarness) RunSessionWithResult(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	if err := h.RunSession(ctx, prompt, workdir); err != nil {
		return HarnessResult{}, err
	}
	return h.res, nil
}

// withInstantRetry zeros the per-attempt backoff durations for tests
// so the retry loop runs without sleeping. Returns a teardown the
// caller must defer to restore production durations.
func withInstantRetry(t *testing.T) {
	t.Helper()
	saved := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	t.Cleanup(func() { retryBackoff = saved })
}

// TestRunGrid_RetryRecoversFromTransient429: a 429 on the first
// attempt should retry; if the next attempt succeeds, the cell
// reports TaskSuccess=true and Notes records retry_count.
func TestRunGrid_RetryRecoversFromTransient429(t *testing.T) {
	withInstantRetry(t)
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	fake := &flakyHarness{
		failuresBefore: 1,
		transientErr:   fmt.Errorf("aider exited: openrouter (429): qwen3-coder:free is temporarily rate-limited upstream"),
		res:            HarnessResult{TokensIn: 100, TokensOut: 50, AgentTurnsTotal: 1, LatencyMs: 1},
	}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("calls=%d want 2 (1 fail + 1 retry)", fake.calls)
	}
	if !results[0].TaskSuccess {
		t.Errorf("TaskSuccess=false; retry should have recovered. Notes: %s", results[0].Notes)
	}
	if !strings.Contains(results[0].Notes, "retry_count=1") {
		t.Errorf("Notes=%q, want 'retry_count=1'", results[0].Notes)
	}
}

// TestRunGrid_RetryGivesUpAfterMaxAttempts: with 4 transient failures
// (more than retryBackoff length), the cell ends in error after the
// configured retry budget — does not loop forever.
func TestRunGrid_RetryGivesUpAfterMaxAttempts(t *testing.T) {
	withInstantRetry(t)
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	fake := &flakyHarness{
		failuresBefore: 999, // never succeeds
		transientErr:   fmt.Errorf("temporarily rate-limited"),
	}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	// 1 initial + len(retryBackoff)=3 retries = 4 calls total.
	if fake.calls != 4 {
		t.Errorf("calls=%d want 4 (1 initial + 3 retries)", fake.calls)
	}
	if results[0].TaskSuccess {
		t.Errorf("TaskSuccess=true; want false (all attempts failed)")
	}
	if !strings.Contains(results[0].Notes, "retry_count=3") {
		t.Errorf("Notes=%q, want 'retry_count=3'", results[0].Notes)
	}
}

// TestRunGrid_NoRetryOnHardErrors: a non-transient error (auth failure,
// model not found) should NOT consume the retry budget — fail fast.
func TestRunGrid_NoRetryOnHardErrors(t *testing.T) {
	withInstantRetry(t)
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	fake := &flakyHarness{
		failuresBefore: 999,
		transientErr:   fmt.Errorf("aider exited: openrouter (401): No auth credentials found"),
	}

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}},
		[]HarnessSpec{{Name: HarnessAider, Harness: fake}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("calls=%d want 1 (no retry on hard error)", fake.calls)
	}
	if results[0].TaskSuccess {
		t.Errorf("TaskSuccess=true; want false (auth error)")
	}
	if strings.Contains(results[0].Notes, "retry_count") {
		t.Errorf("Notes=%q, should not mention retry on non-transient error", results[0].Notes)
	}
}

// TestIsTransient429 locks the substring patterns the retry path uses.
func TestIsTransient429(t *testing.T) {
	tests := []struct {
		err  string
		want bool
	}{
		{"openrouter (429): qwen/qwen3-coder:free is temporarily rate-limited upstream", true},
		{"rate-limited upstream", true},
		{"retry_after_seconds: 22", true},
		{"please retry shortly", true},
		{"http 429", true},
		{"401 unauthorized", false},
		{"model not found", false},
		{"connection refused", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.err, func(t *testing.T) {
			got := isTransient429(fmt.Errorf("%s", tc.err))
			if got != tc.want {
				t.Errorf("isTransient429(%q)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
	if got := isTransient429(nil); got {
		t.Error("isTransient429(nil) = true, want false")
	}
}

// TestRunGrid_VerifyExits0_TaskSuccess: a scenario whose Verify
// command exits 0 produces TaskSuccess=true even when the underlying
// (no-op) harness wouldn't have proven anything by itself.
func TestRunGrid_VerifyExits0_TaskSuccess(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}, Verify: "true"}},
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d want 1", len(results))
	}
	if !results[0].TaskSuccess {
		t.Errorf("TaskSuccess=false, want true (verify=true should pass)")
	}
}

// TestRunGrid_VerifyExitsNonzero_TaskFails: a Verify that exits
// non-zero overrides the harness's "no error" optimism. Confirms the
// verifier is the source of truth when present.
func TestRunGrid_VerifyExitsNonzero_TaskFails(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}, Verify: "false"}},
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d want 1", len(results))
	}
	if results[0].TaskSuccess {
		t.Errorf("TaskSuccess=true, want false (verify=false should fail)")
	}
	if !strings.Contains(results[0].Notes, "verify failed") {
		t.Errorf("Notes=%q, want 'verify failed'", results[0].Notes)
	}
}

// TestRunGrid_VerifyParsesGoTestCounts: when the Verify command emits
// go-test-style PASS/FAIL lines, the runner counts them into
// TestsPassed/TestsFailed.
func TestRunGrid_VerifyParsesGoTestCounts(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	verify := `printf -- '--- PASS: TestA (0.00s)\n--- PASS: TestB (0.00s)\n--- FAIL: TestC (0.00s)\n'; exit 1`
	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}, Verify: verify}},
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d want 1", len(results))
	}
	c := results[0]
	if c.TestsPassed != 2 {
		t.Errorf("TestsPassed=%d want 2", c.TestsPassed)
	}
	if c.TestsFailed != 1 {
		t.Errorf("TestsFailed=%d want 1", c.TestsFailed)
	}
	if c.TaskSuccess {
		t.Errorf("TaskSuccess=true (verify exited 1, should be false)")
	}
}

// TestRunGrid_NoVerifyKeepsLegacyBehavior: a scenario without Verify
// keeps the legacy "harness exit code is success" semantics so the
// pre-existing retrieval scenarios stay valid.
func TestRunGrid_NoVerifyKeepsLegacyBehavior(t *testing.T) {
	p := newTestPersister(t)
	t.Setenv(EnvNoFreePreference, "1")

	results, err := RunGrid(context.Background(), p,
		[]*Scenario{{ID: "x", Tests: []Test{{Query: "q"}}}}, // no Verify
		[]HarnessSpec{{Name: HarnessAider, Harness: &gridFakeHarness{}}},
		[]ModelSpec{{Provider: ProviderOpenRouter, Model: "openai/gpt-oss-20b:free"}},
		[]ContextStrategy{StrategyBaseline})
	if err != nil {
		t.Fatalf("RunGrid: %v", err)
	}
	if !results[0].TaskSuccess {
		t.Errorf("TaskSuccess=false; legacy no-verify should mirror harness exit 0")
	}
	if results[0].TestsPassed != 0 || results[0].TestsFailed != 0 {
		t.Errorf("test counts=%d/%d, want 0/0 without verify", results[0].TestsPassed, results[0].TestsFailed)
	}
}

func TestScenarioToPrompt(t *testing.T) {
	tests := []struct {
		name string
		scn  *Scenario
		want string
	}{
		{"name + query", &Scenario{ID: "x", Name: "Build foo", Tests: []Test{{Query: "implement bar"}}},
			"Build foo\n\nimplement bar"},
		{"name only", &Scenario{ID: "x", Name: "Just a name"}, "Just a name"},
		{"id fallback", &Scenario{ID: "id-only"}, "id-only"},
		{"multiple queries", &Scenario{ID: "x", Name: "n", Tests: []Test{{Query: "q1"}, {Query: "q2"}}},
			"n\n\nq1\nq2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scenarioToPrompt(tc.scn)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
