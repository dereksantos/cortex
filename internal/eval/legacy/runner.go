// Package legacy runs the 22 per-node scenarios under test/evals/legacy/
// cognition/ against the current cognitive mode implementations in
// internal/cognition.
//
// Two scenario patterns:
//
//  1. Self-contained (resolve_inject, resolve_queue, resolve_wait):
//     inline all input data (query + results); runner can dispatch
//     directly to the mode and compare against the expected block.
//
//  2. Storage-dependent (everything else): reference fixture IDs
//     like "auth_module" that aren't defined inline. These need a
//     seeded storage layer before the mode can resolve them.
//
// This file implements pattern (1) — Phase B's minimum-viable
// runner. Pattern (2) requires the canonical fixture set
// (auth_module / jwt_handler / db_schema / etc) loaded into a real
// storage instance, deferred to a follow-up.
//
// See docs/eval-prep-epic.md Phase B and the Phase B + D audit
// entry in docs/eval-journal.md for the path-decision rationale.
package legacy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	cog "github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"gopkg.in/yaml.v3"
)

// Scenario mirrors the test/evals/legacy/cognition/*.yaml shape (the
// subset this runner consumes — we ignore unknown fields).
type Scenario struct {
	ID          string     `yaml:"id"`
	Type        string     `yaml:"type"` // expect "mode"
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Mode        string     `yaml:"mode"` // reflex | reflect | resolve | think | dream | router
	ModeTests   []ModeTest `yaml:"mode_tests"`
}

// ModeTest is one assertion within a scenario.
type ModeTest struct {
	ID       string         `yaml:"id"`
	Input    map[string]any `yaml:"input"`
	Expected map[string]any `yaml:"expected"`
}

// TestResult is the per-test outcome the runner emits.
type TestResult struct {
	Scenario     string `json:"scenario"`
	Mode         string `json:"mode"`
	TestID       string `json:"test_id"`
	OK           bool   `json:"ok"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	LatencyMs    int64  `json:"latency_ms"`
}

// SuiteResult aggregates per-test outcomes for a full suite run.
type SuiteResult struct {
	Suite      string       `json:"suite"`
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	Skipped    int          `json:"skipped"`
	TotalMs    int64        `json:"total_ms"`
	TestResults []TestResult `json:"tests"`
}

// RunSuite loads every *.yaml under dir, runs each scenario, and
// returns the aggregated result. Caller decides how to render
// (human / json). Errors loading or parsing a scenario are surfaced
// as Skipped TestResult entries; the runner does not abort the suite.
func RunSuite(ctx context.Context, dir string) (*SuiteResult, error) {
	pattern := filepath.Join(dir, "*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no legacy-cognition scenarios in %s", dir)
	}
	sort.Strings(matches)

	suite := &SuiteResult{Suite: "legacy-cognition"}

	for _, path := range matches {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			suite.TestResults = append(suite.TestResults, TestResult{
				Scenario:     filepath.Base(path),
				TestID:       "<load>",
				ErrorCode:    "read_failed",
				ErrorMessage: rerr.Error(),
			})
			suite.Skipped++
			continue
		}
		var s Scenario
		if uerr := yaml.Unmarshal(data, &s); uerr != nil {
			suite.TestResults = append(suite.TestResults, TestResult{
				Scenario:     filepath.Base(path),
				TestID:       "<parse>",
				ErrorCode:    "parse_failed",
				ErrorMessage: uerr.Error(),
			})
			suite.Skipped++
			continue
		}
		runScenario(ctx, &s, suite)
	}

	suite.Total = len(suite.TestResults)
	return suite, nil
}

// runScenario dispatches a scenario to the right mode handler and
// appends per-test results to the suite.
func runScenario(ctx context.Context, s *Scenario, suite *SuiteResult) {
	for _, t := range s.ModeTests {
		start := time.Now()
		var (
			ok        bool
			errCode   string
			errMsg    string
		)
		switch s.Mode {
		case "resolve":
			ok, errCode, errMsg = runResolveTest(ctx, &t)
		case "reflex":
			ok, errCode, errMsg = runReflexTest(ctx, &t)
		case "reflect", "think", "dream", "router":
			// Other storage-dependent modes need additional per-mode
			// dispatchers (similar shape to runReflexTest). Deferred
			// to a follow-up. Reflex is wired today as the smallest
			// end-to-end demonstration of the seed-and-dispatch
			// pattern; same pattern extends to reflect/think/dream/router.
			ok = false
			errCode = "needs_per_mode_dispatcher"
			errMsg = fmt.Sprintf("mode=%s dispatcher not yet wired; fixtures + storage seeding work (see runReflexTest pattern). Phase B follow-up.", s.Mode)
		default:
			ok = false
			errCode = "unknown_mode"
			errMsg = fmt.Sprintf("scenario mode=%q not handled by runner", s.Mode)
		}
		tr := TestResult{
			Scenario:  s.ID,
			Mode:      s.Mode,
			TestID:    t.ID,
			OK:        ok,
			LatencyMs: time.Since(start).Milliseconds(),
		}
		if !ok {
			tr.ErrorCode = errCode
			tr.ErrorMessage = errMsg
			if errCode == "needs_fixture_seed" {
				suite.Skipped++
			} else {
				suite.Failed++
			}
		} else {
			suite.Passed++
		}
		suite.TestResults = append(suite.TestResults, tr)
	}
}

// runResolveTest dispatches one resolve-mode test. Self-contained
// resolve scenarios inline both the query and the candidate results;
// runner converts to cognition.Query + []cognition.Result and calls
// Resolve. Asserts the returned Decision matches expected.decision
// and (when present) min_confidence is satisfied.
func runResolveTest(ctx context.Context, t *ModeTest) (ok bool, errCode, errMsg string) {
	q, results, ierr := buildResolveInput(t.Input)
	if ierr != nil {
		return false, "input_invalid", ierr.Error()
	}
	r := cognition.NewResolve()
	got, rerr := r.Resolve(ctx, q, results)
	if rerr != nil {
		return false, "resolve_error", rerr.Error()
	}

	expectedDecision, _ := t.Expected["decision"].(string)
	if expectedDecision == "" {
		return false, "expected_missing", "expected.decision not set"
	}
	if got.Decision.String() != expectedDecision {
		return false, "decision_mismatch", fmt.Sprintf("expected %q, got %q (confidence=%.3f, reason=%s)", expectedDecision, got.Decision.String(), got.Confidence, got.Reason)
	}

	if minConf, ok := t.Expected["min_confidence"].(float64); ok {
		if got.Confidence < minConf {
			return false, "confidence_too_low", fmt.Sprintf("expected min %.2f, got %.3f", minConf, got.Confidence)
		}
	}

	return true, "", ""
}

// runReflexTest dispatches one reflex-mode test against a seeded
// storage instance. Constructs a temp context dir, seeds canonical
// fixtures via SeedFixtures (JSONL-write path that honors the public
// storage API), opens storage, runs Reflex with nil embedder
// (text-based scoring — sufficient for the canonical fixture set
// which scores by EventID + text match, not semantic similarity),
// and compares returned Result IDs against expected.result_ids.
func runReflexTest(ctx context.Context, t *ModeTest) (ok bool, errCode, errMsg string) {
	// Build query from input.
	q, qerr := buildReflexQuery(t.Input)
	if qerr != nil {
		return false, "input_invalid", qerr.Error()
	}

	// Per-scenario temp dir so seeded fixtures don't bleed between tests.
	tempDir, err := os.MkdirTemp("", "cortex-legacy-reflex-*")
	if err != nil {
		return false, "tempdir_failed", err.Error()
	}
	defer os.RemoveAll(tempDir)

	if err := SeedFixtures(tempDir); err != nil {
		return false, "seed_failed", err.Error()
	}

	store, err := storage.New(&config.Config{ContextDir: tempDir})
	if err != nil {
		return false, "storage_open_failed", err.Error()
	}
	defer store.Close()

	// nil embedder: Reflex falls back to text-based scoring per its
	// NewReflex doc. Canonical fixtures' Summary text contains the
	// keywords scenarios search for (auth, jwt, db, etc).
	r := cognition.NewReflex(store, nil)
	results, err := r.Reflex(ctx, q)
	if err != nil {
		return false, "reflex_error", err.Error()
	}

	// Assertions per the scenario's expected block.
	gotIDs := make([]string, 0, len(results))
	for _, res := range results {
		gotIDs = append(gotIDs, res.ID)
	}

	expRaw, hasExp := t.Expected["result_ids"].([]any)
	if !hasExp {
		// Without result_ids, just check we got >= min_results.
		if minR, ok := t.Expected["min_results"].(int); ok {
			if len(results) < minR {
				return false, "too_few_results", fmt.Sprintf("got %d results, want >= %d", len(results), minR)
			}
		}
		return true, "", ""
	}

	// Check that EVERY expected ID appears in results (order not enforced
	// at this level; ranking checks would be a separate assertion).
	gotSet := make(map[string]bool, len(gotIDs))
	for _, id := range gotIDs {
		gotSet[id] = true
	}
	missing := []string{}
	for _, e := range expRaw {
		eid, _ := e.(string)
		if eid == "" {
			continue
		}
		if !gotSet[eid] {
			missing = append(missing, eid)
		}
	}
	if len(missing) > 0 {
		return false, "missing_expected_ids", fmt.Sprintf("expected ids missing from results: %v (got: %v)", missing, gotIDs)
	}

	// Optional min_results check.
	if minR, ok := t.Expected["min_results"].(int); ok {
		if len(results) < minR {
			return false, "too_few_results", fmt.Sprintf("got %d results, want >= %d", len(results), minR)
		}
	}

	return true, "", ""
}

// buildReflexQuery converts the YAML input.query map into a typed
// cognition.Query. Reflex inputs are simpler than Resolve inputs —
// just query text + optional limit/tags/categories.
func buildReflexQuery(input map[string]any) (cog.Query, error) {
	var q cog.Query
	qm, ok := input["query"].(map[string]any)
	if !ok {
		return q, fmt.Errorf("input.query missing or not a map")
	}
	if t, ok := qm["text"].(string); ok {
		q.Text = t
	}
	if v, ok := qm["limit"].(int); ok {
		q.Limit = v
	}
	if tags, ok := qm["tags"].([]any); ok {
		for _, tg := range tags {
			if s, ok := tg.(string); ok {
				q.Tags = append(q.Tags, s)
			}
		}
	}
	if cats, ok := qm["categories"].([]any); ok {
		for _, c := range cats {
			if s, ok := c.(string); ok {
				q.Categories = append(q.Categories, s)
			}
		}
	}
	if strings.TrimSpace(q.Text) == "" {
		return q, fmt.Errorf("input.query.text is empty")
	}
	return q, nil
}

// buildResolveInput converts the YAML input map into a typed Query +
// []Result for the cognition.Resolve call.
func buildResolveInput(input map[string]any) (cog.Query, []cog.Result, error) {
	var q cog.Query
	if qm, ok := input["query"].(map[string]any); ok {
		if t, ok := qm["text"].(string); ok {
			q.Text = t
		}
	}
	resultsRaw, _ := input["results"].([]any)
	results := make([]cog.Result, 0, len(resultsRaw))
	for i, r := range resultsRaw {
		rm, ok := r.(map[string]any)
		if !ok {
			return q, nil, fmt.Errorf("input.results[%d] not a map", i)
		}
		var res cog.Result
		if v, ok := rm["id"].(string); ok {
			res.ID = v
		}
		if v, ok := rm["content"].(string); ok {
			res.Content = v
		}
		if v, ok := rm["category"].(string); ok {
			res.Category = v
		}
		// YAML floats land as float64; ints as int.
		switch v := rm["score"].(type) {
		case float64:
			res.Score = v
		case int:
			res.Score = float64(v)
		}
		results = append(results, res)
	}
	if strings.TrimSpace(q.Text) == "" {
		return q, nil, fmt.Errorf("input.query.text is empty")
	}
	if len(results) == 0 {
		return q, nil, fmt.Errorf("input.results is empty")
	}
	return q, results, nil
}
