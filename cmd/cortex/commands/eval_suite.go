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
	"path/filepath"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/legacy"
	"gopkg.in/yaml.v3"
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
		return suiteNotYetImplemented(suite,
			"Phase D runner — see docs/prompts/loop-phase-d-journeys.md")
	default:
		return fmt.Errorf("unknown suite %q (known: mechanic, legacy-cognition, journeys)", suite)
	}
}

// mechanicFixture is the minimal shape needed to emit a structured
// "not implemented" status for each fixture. The full schema (mocked
// handlers, seed, initial budget, expected) lives in the YAML files
// and is consumed by the DAG executor once it exists; the suite stub
// only needs enough to report which fixtures exist and what they
// each verify.
type mechanicFixture struct {
	ID                  string `yaml:"id"`
	Version             int    `yaml:"version"`
	Suite               string `yaml:"suite"`
	Description         string `yaml:"description"`
	FailureMessageToday string `yaml:"failure_message_today"`
}

// runMechanicSuite loads every *.yaml under dir, parses the
// fixture identity fields, and emits structured failure rows for each
// (since the DAG executor isn't implemented yet). Exit code is
// non-zero — these fixtures are *supposed* to fail until Phase 5 v0
// lands; the CLI surface treats that as expected, not as crashed.
func runMechanicSuite(dir, outputFormat string, verbose bool) error {
	pattern := filepath.Join(dir, "*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no mechanic fixtures found in %s", dir)
	}
	sort.Strings(matches)

	type result struct {
		Fixture      string `json:"fixture"`
		Version      int    `json:"version"`
		Path         string `json:"path"`
		OK           bool   `json:"ok"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}

	results := make([]result, 0, len(matches))
	for _, path := range matches {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		var fx mechanicFixture
		if uerr := yaml.Unmarshal(data, &fx); uerr != nil {
			return fmt.Errorf("parse %s: %w", path, uerr)
		}
		results = append(results, result{
			Fixture:      fx.ID,
			Version:      fx.Version,
			Path:         path,
			OK:           false,
			ErrorCode:    "not_implemented",
			ErrorMessage: strings.TrimSpace(fx.FailureMessageToday),
		})
	}

	if outputFormat == "json" {
		out := map[string]any{
			"suite":    "mechanic",
			"fixtures": results,
			"summary": map[string]any{
				"total":          len(results),
				"passed":         0,
				"failed":         len(results),
				"expected_state": "all fail until Stage 1 v0 lands the executor",
				"see":            "docs/dag-build-plan.md Stage 1, docs/eval-prep-epic.md Phase C",
			},
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("=== mechanic suite (%d fixtures) ===\n\n", len(results))
		for _, r := range results {
			fmt.Printf("  %s [v%d]\n", r.Fixture, r.Version)
			fmt.Printf("    status: FAIL (error_code=%s)\n", r.ErrorCode)
			if verbose && r.ErrorMessage != "" {
				fmt.Printf("    reason:\n")
				for _, line := range strings.Split(r.ErrorMessage, "\n") {
					fmt.Printf("      %s\n", line)
				}
			}
			fmt.Println()
		}
		fmt.Printf("Total: %d  Passed: 0  Failed: %d\n", len(results), len(results))
		fmt.Println("All fixtures fail as expected until DAG executor lands.")
		fmt.Println("See docs/dag-build-plan.md Stage 1 for the implementation gate.")
	}

	// Exit non-zero so CI / scripts can detect the "still pre-executor"
	// state without parsing output. Once the executor lands and all 5
	// fixtures pass, this should flip to exit 0.
	os.Exit(2)
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
