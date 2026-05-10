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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

	// Free-tier preference: rewrite each ModelSpec to its `:free` variant
	// when one exists in the curated pair table. Disabled via
	// CORTEX_EVAL_NO_FREE_PREFERENCE=1 for users who want the paid form
	// (e.g. for benchmarking).
	if os.Getenv(EnvNoFreePreference) == "" {
		preferred := make([]ModelSpec, len(models))
		for i, m := range models {
			preferred[i] = ModelSpec{Provider: m.Provider, Model: PreferFreeVariant(m.Model)}
		}
		models = preferred
	}

	// Spend safety. Ceilings + tracker pull from env (defaults from
	// docs/eval-harness-loop.md TODO 8). Frontier guard is a separate
	// boolean gate so a routine grid run can't accidentally fire Sonnet.
	ceilings := CeilingsFromEnv()
	tracker := NewSpendTracker(p, ceilings)
	allowFrontier := os.Getenv(EnvAllowFrontier) == "1"

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

					// Frontier guard fires before any cost work — a
					// frontier model without the env gate is a hard
					// stop, never an estimate-and-skip.
					if FrontierGuardRequired(ms.Model) && !allowFrontier {
						partial := emitPartialCSV(p, results, fmt.Sprintf(
							"frontier guard: model %q requires %s=1", ms.Model, EnvAllowFrontier))
						return results, fmt.Errorf("frontier model %q blocked (set %s=1 to enable); partial CSV: %s",
							ms.Model, EnvAllowFrontier, partial)
					}

					// Ceiling check uses an estimate that's the max of
					// the last observed cost for this (provider, model)
					// pair and 1.5× the tier floor. Trips before any
					// network call.
					estimate := tracker.EstimateCost(ms.Provider, ms.Model)
					tripped, daily, lifetime, cerr := tracker.CheckBeforeCall(estimate)
					if cerr != nil {
						return results, fmt.Errorf("ceiling check: %w", cerr)
					}
					if tripped != "" {
						partial := emitPartialCSV(p, results, fmt.Sprintf(
							"%s ceiling: estimate $%.4f would exceed limit; run=$%.4f daily=$%.4f lifetime=$%.4f; ceilings run=$%.2f daily=$%.2f lifetime=$%.2f",
							tripped, estimate,
							tracker.RunSpend(), daily, lifetime,
							ceilings.Run, ceilings.Daily, ceilings.Lifetime))
						return results, fmt.Errorf("aborted: %s ceiling would be exceeded by next cell (estimate $%.4f); partial CSV: %s",
							tripped, estimate, partial)
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
					if err := tracker.RecordCell(ms.Provider, ms.Model, cell.CostUSD); err != nil {
						return results, fmt.Errorf("track spend: %w", err)
					}
					results = append(results, cell)
				}
			}
		}
	}
	return results, nil
}

// emitPartialCSV writes the completed cells (so far) to <dbDir>/<id>.partial.csv
// + a sidecar <id>.partial.meta.json carrying the abort reason. Returns
// the CSV path for callers to surface in error messages. CSV is the
// analysis-friendly format mandated by hard constraint #8; the sidecar
// keeps the abort metadata structured without breaking CSV strictness.
func emitPartialCSV(p *Persister, completed []CellResult, reason string) string {
	dir := p.dbDir
	if dir == "" {
		dir = filepath.Join(".cortex", "db")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("<mkdir failed: %v>", err)
	}

	id := newRunID()
	csvPath := filepath.Join(dir, id+".partial.csv")
	metaPath := filepath.Join(dir, id+".partial.meta.json")

	f, err := os.Create(csvPath)
	if err != nil {
		return fmt.Sprintf("<create failed: %v>", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	header := []string{
		"run_id", "scenario_id", "session_id", "harness", "provider", "model",
		"context_strategy", "tokens_in", "tokens_out", "injected_context_tokens",
		"cost_usd", "latency_ms", "agent_turns_total", "tests_passed", "tests_failed",
		"task_success", "timestamp",
	}
	if err := w.Write(header); err != nil {
		return fmt.Sprintf("<header write failed: %v>", err)
	}
	for _, c := range completed {
		row := []string{
			c.RunID, c.ScenarioID, c.SessionID, c.Harness, c.Provider, c.Model,
			c.ContextStrategy,
			strconv.Itoa(c.TokensIn), strconv.Itoa(c.TokensOut), strconv.Itoa(c.InjectedContextTokens),
			strconv.FormatFloat(c.CostUSD, 'f', -1, 64),
			strconv.FormatInt(c.LatencyMs, 10),
			strconv.Itoa(c.AgentTurnsTotal),
			strconv.Itoa(c.TestsPassed), strconv.Itoa(c.TestsFailed),
			strconv.FormatBool(c.TaskSuccess),
			c.Timestamp,
		}
		if err := w.Write(row); err != nil {
			return fmt.Sprintf("<row write failed: %v>", err)
		}
	}
	w.Flush()

	meta, _ := json.MarshalIndent(map[string]any{
		"abort_reason":    reason,
		"completed_cells": len(completed),
		"timestamp":       time.Now().UTC().Format(time.RFC3339),
		"csv_path":        csvPath,
	}, "", "  ")
	if metaErr := os.WriteFile(metaPath, append(meta, '\n'), 0o644); metaErr != nil {
		// Sidecar failure is non-fatal — the CSV is the durable record.
		return csvPath
	}
	return csvPath
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

	// Optional seed_dir → workdir copy. Lets coding scenarios ship a
	// minimal Go project (go.mod + stubs + tests) for the agent to
	// edit, rather than starting from an empty dir.
	if scn.SeedDir != "" {
		if err := copyDirIntoWorkdir(ctx, scn.SeedDir, workdir); err != nil {
			return CellResult{}, fmt.Errorf("seed workdir from %q: %w", scn.SeedDir, err)
		}
	}

	prompt := scenarioToPrompt(scn)

	// Harnesses that bake the model at construction time (Aider) can
	// implement SetModel(string) so the grid can re-point one instance
	// across cells. Done via inline interface assertion to avoid widening
	// the Harness contract.
	//
	// Aider/litellm expects the provider as a prefix
	// ("openrouter/openai/gpt-oss-20b:free", "ollama/qwen2.5-coder:1.5b").
	// CellResult.Model stays canonical (no prefix) for clean grid
	// aggregation; only the harness call gets the prefixed form.
	if setter, ok := hs.Harness.(interface{ SetModel(string) }); ok {
		harnessModel := ms.Model
		if ms.Provider != "" && !strings.HasPrefix(harnessModel, ms.Provider+"/") {
			harnessModel = ms.Provider + "/" + ms.Model
		}
		setter.SetModel(harnessModel)
	}

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
		TaskSuccess:          runErr == nil,
		TaskSuccessCriterion: CriterionTestsPassAll,
	}
	if strat == StrategyCortex {
		cr.CortexVersion = CortexVersion
	}
	if runErr != nil {
		cr.Notes = fmt.Sprintf("harness error: %v", runErr)
		// Don't run the verifier if the harness errored — there's
		// nothing useful to verify. Pre-existing TaskSuccess=false stays.
		return cr, nil
	}

	// Optional post-harness verifier. Only runs when the scenario
	// declares one — empty Verify keeps the legacy "harness exit code
	// IS the success" behavior so older retrieval scenarios stay
	// unaffected.
	if scn.Verify != "" {
		passed, failed, vErr := runVerifier(ctx, scn.Verify, workdir)
		cr.TestsPassed = passed
		cr.TestsFailed = failed
		cr.TaskSuccess = vErr == nil
		if vErr != nil {
			if cr.Notes != "" {
				cr.Notes += "; "
			}
			cr.Notes += fmt.Sprintf("verify failed: %v", vErr)
		}
	}
	return cr, nil
}

// copyDirIntoWorkdir recursively copies the contents of src (not src
// itself) into dst. Uses `cp -R src/. dst/` for portable POSIX
// semantics on macOS + Linux. Fails fast with a wrapped error if src
// doesn't exist or cp fails.
func copyDirIntoWorkdir(ctx context.Context, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat seed_dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("seed_dir %q is not a directory", src)
	}

	// `cp -R src/. dst` copies contents of src into dst, not src itself.
	// Trailing `/.` is the POSIX convention for "every entry inside,
	// including dotfiles".
	cmd := exec.CommandContext(ctx, "cp", "-R", filepath.Clean(src)+"/.", dst)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp -R: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// goTestPassRE / goTestFailRE match the per-test summary lines emitted
// by `go test -v` (and `go test` with verbose subtests). Subtests
// (`PASS: TestX/sub`) are counted alongside their parents — the count
// reflects total assertions decided, not unique test functions.
var (
	goTestPassRE = regexp.MustCompile(`(?m)^\s*--- PASS:`)
	goTestFailRE = regexp.MustCompile(`(?m)^\s*--- FAIL:`)
)

// runVerifier execs `bash -c "<cmd>"` in workdir and returns the
// derived pass/fail counts plus any execution error. Exit-0 → no
// error; any other exit (including signals) → error wrapping the
// captured stderr. The 5-minute timeout is a safety net — long
// verifiers (full integration suites) should be designed to fit.
func runVerifier(ctx context.Context, command, workdir string) (passed, failed int, err error) {
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(verifyCtx, "bash", "-c", command)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	out := stdout.String()
	passed = len(goTestPassRE.FindAllString(out, -1))
	failed = len(goTestFailRE.FindAllString(out, -1))

	if runErr != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = strings.TrimSpace(out)
		}
		// Truncate to keep CellResult.Notes reasonable.
		if len(errMsg) > 200 {
			errMsg = errMsg[:200] + "…"
		}
		return passed, failed, fmt.Errorf("%w (stderr: %s)", runErr, errMsg)
	}
	return passed, failed, nil
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
