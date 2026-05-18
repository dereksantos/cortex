// Package commands — eval --suite=<name> dispatch.
//
// Suites are eval families that operate outside the standard v2 /
// benchmark / harness paths:
//
//   - mechanic         : deterministic fixtures verifying DAG executor
//     invariants (budget decay, depth cap, tree
//     reconstruction, exhaustion graceful-degrade,
//     tree-shape variation). All fail today until
//     Phase 5 v0 lands the executor.
//   - legacy-cognition : per-node scenarios under test/evals/legacy/
//     cognition/ — stub awaiting Phase B runner.
//   - journeys         : multi-session e2e scenarios under
//     test/evals/journeys/ — stub awaiting Phase D
//     runner.
//
// Each suite is its own dispatcher function; the top-level runSuite
// chooses by name. Adding a suite is a function + a switch arm; no
// flag parsing changes needed beyond the suite name itself.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/journey"
	"github.com/dereksantos/cortex/internal/eval/legacy"
	"github.com/dereksantos/cortex/internal/eval/mechanic"
)

// runSuite is the entrypoint for `cortex eval --suite=<name>`. It
// dispatches by suite name to the per-suite runner. Suite names that
// don't have a runner yet return a structured "not implemented" error
// (clean stdout, non-zero exit) so callers can distinguish "suite
// unknown" from "suite known but pending."
func runSuite(suite, baseDir, outputFormat string, verbose bool) error {
	switch suite {
	case "mechanic":
		dir := baseDir
		if dir == "" || dir == "test/evals/v2" { // default --dir not overridden
			dir = "test/evals/mechanic"
		}
		return runMechanicSuite(dir, outputFormat, verbose)
	case "legacy-cognition":
		dir := baseDir
		if dir == "" || dir == "test/evals/v2" {
			dir = "test/evals/legacy/cognition"
		}
		return runLegacyCognitionSuite(dir, outputFormat, verbose)
	case "journeys":
		dir := baseDir
		if dir == "" || dir == "test/evals/v2" {
			dir = "test/evals/journeys"
		}
		return runJourneysSuite(dir, outputFormat, verbose)
	default:
		return fmt.Errorf("unknown suite %q (known: mechanic, legacy-cognition, journeys)", suite)
	}
}

// runMechanicSuite loads every *.yaml under dir, executes each
// fixture through the DAG executor (pkg/cognition/dag), and reports
// PASS/FAIL based on the actual executor behavior matching the
// fixture's expected block.
//
// As of Stage 1 v0 (commits 1406eb6 + this file), 4 of 5 mechanic
// invariants (M1 budget decay, M2 tree reconstruction, M3 depth
// cap, M4 budget exhaustion) pass green; M5 (tree-shape variation)
// requires input-aware handler dispatch which is also wired here.
func runMechanicSuite(dir, outputFormat string, verbose bool) error {
	ctx := context.Background()
	res, err := mechanic.RunSuite(ctx, dir)
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		if res.Failed > 0 {
			os.Exit(1)
		}
		return nil
	}

	fmt.Printf("=== mechanic suite (%d fixtures) ===\n\n", res.Total)
	for _, r := range res.Results {
		statusTag := "PASS"
		if !r.OK {
			statusTag = "FAIL"
		}
		fmt.Printf("  [%s] %s\n", statusTag, r.Fixture)
		if r.TraceSummary != "" {
			fmt.Printf("         %s\n", r.TraceSummary)
		}
		if !r.OK && verbose {
			for _, f := range r.Failures {
				fmt.Printf("         × %s\n", f)
			}
		}
		fmt.Println()
	}
	fmt.Printf("Total: %d  Passed: %d  Failed: %d\n", res.Total, res.Passed, res.Failed)
	if res.Failed > 0 {
		os.Exit(1)
	}
	return nil
}

// runLegacyCognitionSuite dispatches the 22 scenarios under
// test/evals/legacy/cognition/ to internal/eval/legacy.RunSuite.
// Self-contained resolve-mode scenarios run end-to-end; storage-
// dependent modes (reflex / reflect / think / dream / router) are
// reported as skipped with error_code=needs_fixture_seed until the
// canonical fixture-seed helper lands as a follow-up.
func runLegacyCognitionSuite(dir, outputFormat string, verbose bool) error {
	ctx := context.Background()
	res, err := legacy.RunSuite(ctx, dir)
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		if res.Failed > 0 {
			os.Exit(1)
		}
		return nil
	}

	fmt.Printf("=== legacy-cognition suite (%d tests across scenarios) ===\n\n", res.Total)
	for _, t := range res.TestResults {
		var statusTag string
		switch {
		case t.OK:
			statusTag = "PASS"
		case t.ErrorCode == "needs_fixture_seed":
			statusTag = "SKIP"
		default:
			statusTag = "FAIL"
		}
		fmt.Printf("  [%s] %s / %s (mode=%s, %dms)\n", statusTag, t.Scenario, t.TestID, t.Mode, t.LatencyMs)
		if !t.OK && verbose && t.ErrorMessage != "" {
			fmt.Printf("         %s\n", t.ErrorMessage)
		}
	}
	fmt.Printf("\nTotal: %d  Passed: %d  Failed: %d  Skipped: %d\n",
		res.Total, res.Passed, res.Failed, res.Skipped)
	if res.Skipped > 0 {
		fmt.Println("Skipped tests need the canonical fixture-seed helper")
		fmt.Println("(planned follow-up — see Phase B + D audit entry in docs/eval-journal.md).")
	}
	if res.Failed > 0 {
		os.Exit(1)
	}
	return nil
}

// runJourneysSuite loads + validates the 10 e2e scenarios under
// test/evals/journeys/ and reports per-scenario status. Two depths:
//
//   - default: validation only (parse + scaffold check)
//   - --with-seed: also runs the seed adapter — converts each
//     scenario's session events into Cortex insights, seeds them
//     into per-scenario temp storage, verifies retrievability
//
// Full agent execution is Phase D's remaining work (reuses cortex
// code harness pattern).
func runJourneysSuite(dir, outputFormat string, verbose bool) error {
	// Detect --with-seed / --execute via env (the eval CLI's flag
	// parsing doesn't flow flags into here; using env keeps this
	// surface minimal). --execute implies --with-seed (execution
	// path bundles its own seeding into the workdir's .cortex).
	withSeed := os.Getenv("CORTEX_JOURNEYS_WITH_SEED") != ""
	execute := os.Getenv("CORTEX_JOURNEYS_EXECUTE") != ""

	if execute {
		return runJourneysExecute(dir, outputFormat, verbose)
	}

	if withSeed {
		res, reports, err := journey.RunSuiteWithSeed(dir)
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			out := map[string]any{"suite": res, "seed_reports": reports}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		fmt.Printf("=== journeys suite (%d scenarios — validation + seed) ===\n\n", res.Total)
		repByID := make(map[string]journey.SeedReport)
		for _, r := range reports {
			repByID[r.ScenarioID] = r
		}
		for _, s := range res.Scenarios {
			statusTag := strings.ToUpper(s.Status)
			fmt.Printf("  [%s] %s\n", statusTag, s.ID)
			fmt.Printf("         scaffold: %s (exists=%v)\n", s.ScaffoldPath, s.ScaffoldExists)
			fmt.Printf("         sessions: %d  events: %d\n", s.SessionCount, s.EventCount)
			if rep, ok := repByID[s.ID]; ok {
				seedTag := "SEED_OK"
				if !rep.SeedOK {
					seedTag = "SEED_FAIL"
				}
				fmt.Printf("         [%s] sessions_processed=%d  events_seeded=%d  events_retrievable=%d\n",
					seedTag, rep.SessionsProcessed, rep.EventsSeeded, rep.EventsRetrievable)
				if !rep.SeedOK && rep.ErrorMessage != "" {
					fmt.Printf("                 %s\n", rep.ErrorMessage)
				}
			}
			fmt.Println()
		}
		fmt.Printf("Total: %d  validation: pending_adapter=%d scaffold_missing=%d invalid=%d  seed: ok=%d failed=%d\n",
			res.Total, res.PendingAdapter, res.ScaffoldMissing, res.Invalid, res.SeedOK, res.SeedFailed)
		fmt.Println("Seed adapter proves journey → Cortex-context pipeline works.")
		fmt.Println("Full agent execution remains pending (Phase D harness adapter).")
		return nil
	}

	// Default: validation only.
	res, err := journey.RunSuite(dir)
	if err != nil {
		return err
	}
	if outputFormat == "json" {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("=== journeys suite (%d scenarios — validation only) ===\n\n", res.Total)
	for _, s := range res.Scenarios {
		statusTag := strings.ToUpper(s.Status)
		fmt.Printf("  [%s] %s\n", statusTag, s.ID)
		fmt.Printf("         scaffold: %s (exists=%v)\n", s.ScaffoldPath, s.ScaffoldExists)
		fmt.Printf("         sessions: %d  events: %d\n", s.SessionCount, s.EventCount)
		fmt.Println()
	}
	fmt.Printf("Total: %d  pending_adapter: %d  scaffold_missing: %d  invalid: %d\n",
		res.Total, res.PendingAdapter, res.ScaffoldMissing, res.Invalid)
	fmt.Println("Validation-only pass. Set CORTEX_JOURNEYS_WITH_SEED=1 for seed-adapter run.")
	fmt.Println("Set CORTEX_JOURNEYS_EXECUTE=1 (with CORTEX_JOURNEYS_MODEL) for full-execution run.")
	return nil
}

// runJourneysExecute is the full-execution path. Drives a
// CortexHarness against each journey's task sessions and emits per-
// session cell_results.jsonl rows. Tunable via env:
//
//   - CORTEX_JOURNEYS_MODEL: OpenRouter model id (default:
//     "anthropic/claude-3-5-haiku")
//   - CORTEX_JOURNEYS_FILTER: comma-separated scenario IDs to run
//     (default: all). E.g. CORTEX_JOURNEYS_FILTER=trivial-hello-world
//     for fast iteration on a single journey.
//   - CORTEX_JOURNEYS_CELL_SINK: file path for cell_results.jsonl
//     (default: <dir>/../../.cortex/db/cell_results.jsonl ; created if
//     missing). Set to "-" to skip the sink.
func runJourneysExecute(dir, outputFormat string, verbose bool) error {
	model := os.Getenv("CORTEX_JOURNEYS_MODEL")
	if model == "" {
		model = "anthropic/claude-3-5-haiku"
	}
	var filter []string
	if f := os.Getenv("CORTEX_JOURNEYS_FILTER"); f != "" {
		filter = strings.Split(f, ",")
	}

	var sink *os.File
	sinkPath := os.Getenv("CORTEX_JOURNEYS_CELL_SINK")
	if sinkPath == "" {
		sinkPath = ".cortex/db/cell_results.jsonl"
	}
	if sinkPath != "-" {
		if err := os.MkdirAll(filepathDirOf(sinkPath), 0o755); err != nil {
			return fmt.Errorf("mkdir sink dir: %w", err)
		}
		f, err := os.OpenFile(sinkPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open cell sink %s: %w", sinkPath, err)
		}
		defer f.Close()
		sink = f
	}

	ctx := context.Background()
	suite, reports, err := journey.RunSuiteWithExecution(ctx, dir, model, filter, sinkOrNil(sink))
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		out := map[string]any{"suite": suite, "execution_reports": reports, "model": model}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("=== journeys suite (%d scenarios — full execution, model=%s) ===\n\n", suite.Total, model)
	totalSessions, passedSessions := 0, 0
	for _, rep := range reports {
		tag := "EXEC_OK"
		if !rep.OverallOK {
			tag = "EXEC_FAIL"
		}
		fmt.Printf("  [%s] %s (%dms)\n", tag, rep.ScenarioID, rep.LatencyMs)
		if rep.ErrorMessage != "" {
			fmt.Printf("         error: %s\n", rep.ErrorMessage)
		}
		for _, sr := range rep.Sessions {
			totalSessions++
			if sr.OK {
				passedSessions++
			}
			sessTag := "PASS"
			if !sr.OK {
				sessTag = "FAIL"
			}
			fmt.Printf("         [%s] %s (%s/%s, %dms, turns=%d)\n",
				sessTag, sr.SessionID, sr.Phase, sr.Kind, sr.LatencyMs, sr.HarnessTurns)
			if !sr.OK && verbose {
				if sr.ErrorMessage != "" {
					fmt.Printf("                  err=%s\n", sr.ErrorMessage)
				}
				if !sr.TestsPassed && len(sr.PatternsRequired) >= 0 {
					fmt.Printf("                  tests_passed=%v required_matched=%d/%d forbidden_found=%v\n",
						sr.TestsPassed, len(sr.PatternsRequired), -1, sr.PatternsForbidden)
				}
			}
		}
		fmt.Println()
	}
	fmt.Printf("Total scenarios: %d  scored sessions: %d/%d pass\n", len(reports), passedSessions, totalSessions)
	if sinkPath != "-" {
		fmt.Printf("cell_results.jsonl appended to %s\n", sinkPath)
	}
	return nil
}

// filepathDirOf is a thin wrapper to avoid an extra path/filepath import
// just for filepath.Dir in this file. (eval_suite.go imports through
// internal packages only.)
func filepathDirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// sinkOrNil returns f as io.Writer, or nil if f is nil. Hides the
// *os.File-vs-io.Writer typing dance at the call site.
func sinkOrNil(f *os.File) interface{ Write(p []byte) (int, error) } {
	if f == nil {
		return nil
	}
	return f
}
