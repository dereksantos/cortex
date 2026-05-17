// Package commands — eval --suite=<name> dispatch.
//
// Suites are eval families that operate outside the standard v2 /
// benchmark / harness paths:
//
//   - mechanic         : deterministic fixtures verifying DAG executor
//                        invariants (budget decay, depth cap, tree
//                        reconstruction, exhaustion graceful-degrade,
//                        tree-shape variation). All fail today until
//                        Phase 5 v0 lands the executor.
//   - legacy-cognition : per-node scenarios under test/evals/legacy/
//                        cognition/ — stub awaiting Phase B runner.
//   - journeys         : multi-session e2e scenarios under
//                        test/evals/journeys/ — stub awaiting Phase D
//                        runner.
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

// suiteNotYetImplemented is the placeholder dispatcher for suites
// whose runner hasn't landed yet. Returns a structured error pointing
// at the loop prompt that will land the runner.
func suiteNotYetImplemented(suite, pointer string) error {
	return fmt.Errorf("suite %q: runner not yet implemented (%s)", suite, pointer)
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
		statusTag := "PASS"
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
// test/evals/journeys/ and reports per-scenario runnability status.
// Does not execute agent runs — the harness adapter is the bulk of
// Phase D's deferred work; the loader confirms the YAML + scaffold
// substrate is intact and surfaces which scenarios are ready to run
// once the adapter lands.
func runJourneysSuite(dir, outputFormat string, verbose bool) error {
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
		if s.Name != "" {
			fmt.Printf("         name:     %s\n", s.Name)
		}
		fmt.Printf("         scaffold: %s (exists=%v)\n", s.ScaffoldPath, s.ScaffoldExists)
		fmt.Printf("         sessions: %d  events: %d\n", s.SessionCount, s.EventCount)
		if verbose && s.Message != "" {
			fmt.Printf("         note:     %s\n", s.Message)
		}
		fmt.Println()
	}
	fmt.Printf("Total: %d  pending_adapter: %d  scaffold_missing: %d  invalid: %d\n",
		res.Total, res.PendingAdapter, res.ScaffoldMissing, res.Invalid)
	fmt.Println("Validation-only pass — agent execution awaits harness adapter")
	fmt.Println("(planned Phase D follow-up, will reuse v2 coding harness pattern).")
	return nil
}
