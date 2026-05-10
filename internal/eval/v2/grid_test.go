//go:build !windows

package eval

import (
	"context"
	"strings"
	"sync"
	"testing"
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

	// Last cell ran model-b (cells iterate models in order, so the last
	// SetModel call is model-b).
	if fake.lastModel != "model-b" {
		t.Errorf("lastModel=%q want %q (final cell should have re-pointed)", fake.lastModel, "model-b")
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
