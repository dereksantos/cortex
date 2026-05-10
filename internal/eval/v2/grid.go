//go:build !windows

// Grid runner for the cross-harness × model × strategy eval grid.
//
// RunGrid expands the Cartesian product over (scenario × harness ×
// model × strategy), invokes one cell at a time, and persists each
// CellResult as it completes (per hard constraint #8: durability is
// per-cell, not per-grid). Concurrency is serial — the --parallel knob
// is deferred to the CLI step.
//
// Refinement of the loop spec's RunGrid signature: HarnessSpec /
// ModelSpec wrap the bare Harness / model-string types so the runner
// can record the harness/provider/model identifiers that go into each
// CellResult. Those names aren't recoverable from the Harness
// interface alone (it has no Name() method, deliberately — the
// existing Aider and ClaudeCLI harnesses predate this need).
package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// HarnessSpec pairs a Harness implementation with its identifier.
//
// Name should be one of HarnessAider / HarnessOpenCode / HarnessPiDev /
// HarnessClaudeCLI from cellresult.go — those constants are the
// canonical column values that downstream analysis joins on.
type HarnessSpec struct {
	Name    string
	Harness Harness
}

// ModelSpec pairs a model identifier with its provider. Provider must
// be one of the Provider* constants in cellresult.go; Model is whatever
// string the harness/provider expects verbatim (e.g.
// "openai/gpt-oss-20b:free", "ollama/qwen2.5-coder:1.5b").
type ModelSpec struct {
	Provider string
	Model    string
}

// ContextStrategy is an alias of string covering the v2 conditions:
// StrategyBaseline / StrategyCortex / StrategyFrontier from
// cellresult.go. Aliased rather than newly typed so the existing
// constants flow through without a breaking change to the contract.
type ContextStrategy = string

// RunGrid expands the Cartesian product of dimensions, invokes one
// cell at a time, and persists each CellResult as it lands.
//
// Returns the in-memory list of completed cells along with any error.
// Callers should treat SQLite + the JSONL log as authoritative — the
// in-memory return is a convenience, not durable. On per-cell failure
// (harness error, validation error, persistence error) RunGrid stops
// and returns the partial list plus the error so the caller can see
// what landed before the failure.
//
// Context cancellation is checked between cells; an in-flight cell
// runs to completion (the harness owns its own SIGTERM lifecycle).
func RunGrid(
	ctx context.Context,
	p *Persister,
	scenarios []*Scenario,
	harnesses []HarnessSpec,
	models []ModelSpec,
	strategies []ContextStrategy,
) ([]CellResult, error) {
	if p == nil {
		return nil, fmt.Errorf("RunGrid: persister is nil")
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("RunGrid: no scenarios")
	}
	if len(harnesses) == 0 {
		return nil, fmt.Errorf("RunGrid: no harnesses")
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("RunGrid: no models")
	}
	if len(strategies) == 0 {
		return nil, fmt.Errorf("RunGrid: no strategies")
	}

	commitSHA, branch := getGitInfo()

	expected := len(scenarios) * len(harnesses) * len(models) * len(strategies)
	results := make([]CellResult, 0, expected)

	for _, scn := range scenarios {
		for _, hs := range harnesses {
			for _, ms := range models {
				for _, strat := range strategies {
					select {
					case <-ctx.Done():
						return results, ctx.Err()
					default:
					}

					cell, err := runOneCell(ctx, scn, hs, ms, strat, commitSHA, branch)
					if err != nil {
						return results, fmt.Errorf("cell scenario=%s harness=%s model=%s strategy=%s: %w",
							scn.ID, hs.Name, ms.Model, strat, err)
					}
					if err := p.PersistCell(ctx, &cell); err != nil {
						return results, fmt.Errorf("persist cell scenario=%s harness=%s model=%s strategy=%s: %w",
							scn.ID, hs.Name, ms.Model, strat, err)
					}
					results = append(results, cell)
				}
			}
		}
	}
	return results, nil
}

// runOneCell drives a single grid cell — fresh workdir, build prompt,
// call harness (preferring ResultfulHarness), assemble CellResult.
//
// Does NOT persist; that's RunGrid's job so the persistence call site
// stays a single funnel.
func runOneCell(ctx context.Context, scn *Scenario, hs HarnessSpec, ms ModelSpec, strat ContextStrategy, commitSHA, branch string) (CellResult, error) {
	workdir, err := os.MkdirTemp("", "cortex-grid-cell-")
	if err != nil {
		return CellResult{}, fmt.Errorf("mkdir workdir: %w", err)
	}
	defer os.RemoveAll(workdir)

	prompt := scenarioToPrompt(scn)

	var (
		hres   HarnessResult
		runErr error
		start  = time.Now()
	)
	if rh, ok := hs.Harness.(ResultfulHarness); ok {
		hres, runErr = rh.RunSessionWithResult(ctx, prompt, workdir)
	} else {
		runErr = hs.Harness.RunSession(ctx, prompt, workdir)
	}
	if hres.LatencyMs == 0 {
		elapsed := time.Since(start)
		hres.LatencyMs = elapsed.Milliseconds()
		// Sub-millisecond ops (no-op fakes, instant returns) floor to 0
		// and would lose the "we did call the harness" signal. Round up.
		if hres.LatencyMs == 0 && elapsed > 0 {
			hres.LatencyMs = 1
		}
	}

	cr := CellResult{
		SchemaVersion:        CellResultSchemaVersion,
		RunID:                newRunID(),
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		GitCommitSHA:         commitSHA,
		GitBranch:            branch,
		ScenarioID:           scn.ID,
		Harness:              hs.Name,
		Provider:             ms.Provider,
		Model:                ms.Model,
		ContextStrategy:      strat,
		Temperature:          0.0,
		TokensIn:             hres.TokensIn,
		TokensOut:            hres.TokensOut,
		CostUSD:              hres.CostUSD,
		LatencyMs:            hres.LatencyMs,
		AgentTurnsTotal:      hres.AgentTurnsTotal,
		TestsPassed:          0, // scoring is wired in a later step
		TestsFailed:          0,
		TaskSuccess:          runErr == nil,
		TaskSuccessCriterion: CriterionTestsPassAll,
	}
	if strat == StrategyCortex {
		cr.CortexVersion = CortexVersion
	}
	if runErr != nil {
		cr.Notes = fmt.Sprintf("harness error: %v", runErr)
	}
	return cr, nil
}

// scenarioToPrompt produces a single string prompt from a Scenario.
//
// First-pass implementation: scenario name + concatenated test queries.
// Real eval scenarios will eventually drive richer prompts (per-session
// briefs, file context, supersession-aware framings, ...) — this is
// the minimum needed for TODO 6's fake-harness test and TODO 9's
// smoke run.
func scenarioToPrompt(scn *Scenario) string {
	var b strings.Builder
	if scn.Name != "" {
		b.WriteString(scn.Name)
		b.WriteString("\n\n")
	}
	for _, t := range scn.Tests {
		if t.Query != "" {
			b.WriteString(t.Query)
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return scn.ID
	}
	return strings.TrimSpace(b.String())
}

// newRunID returns a unique cell run identifier. Time-prefixed for
// natural sortability + 8 random bytes for collision resistance.
// Avoids an external ULID dependency for one identifier.
func newRunID() string {
	var rb [8]byte
	_, _ = rand.Read(rb[:])
	return fmt.Sprintf("cell-%d-%s", time.Now().UnixNano(), hex.EncodeToString(rb[:]))
}
