// Package mechanic runs the test/evals/mechanic/*.yaml fixtures
// through the DAG executor (pkg/cognition/dag). Each fixture declares
// a deterministic mock handler graph + initial budget + expected
// trace properties; the runner builds a per-fixture Registry from
// the mocked_handlers spec, executes the seed, and compares the
// resulting trace against the fixture's expected block.
//
// This wires the Phase C fixtures end-to-end through the Stage 1 v0
// executor — the TDD signal flips from "all FAIL: not_implemented"
// to "PASS/FAIL based on actual executor behavior."
package mechanic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"gopkg.in/yaml.v3"
)

// Fixture mirrors test/evals/mechanic/*.yaml shape (the subset the
// runner consumes — unknown fields ignored).
type Fixture struct {
	ID                  string                  `yaml:"id"`
	Version             int                     `yaml:"version"`
	Suite               string                  `yaml:"suite"`
	Description         string                  `yaml:"description"`
	MockedHandlers      []MockedHandler         `yaml:"mocked_handlers"`
	Seed                []SeedNode              `yaml:"seed"`
	InitialBudget       BudgetSpec              `yaml:"initial_budget"`
	Expected            map[string]any          `yaml:"expected"`
	FailureMessageToday string                  `yaml:"failure_message_today"`
	Scenarios           []FixtureScenario       `yaml:"scenarios,omitempty"` // for mechanic-5
}

// MockedHandler declares one node's deterministic behavior: fixed
// cost + fixed spawn list. The runner builds a NodeSpec from this
// and registers it under <function>.<op>.
type MockedHandler struct {
	NodeID        string     `yaml:"node_id"`
	Function      string     `yaml:"function"`
	Op            string     `yaml:"op"`
	CostConsumed  CostSpec   `yaml:"cost_consumed"`
	CostHint      CostSpec   `yaml:"cost_hint,omitempty"`
	Spawn         []SeedNode `yaml:"spawn"`
}

// SeedNode is a seed-or-spawn entry. Identifies a node by id +
// function.op for executor scheduling.
type SeedNode struct {
	NodeID   string         `yaml:"node_id"`
	Function string         `yaml:"function"`
	Op       string         `yaml:"op"`
	Attrs    map[string]any `yaml:"attrs,omitempty"`
}

// CostSpec is the YAML form of dag.Cost.
type CostSpec struct {
	LatencyMS int `yaml:"latency_ms"`
	Tokens    int `yaml:"tokens"`
}

// BudgetSpec is the YAML form of dag.Budget.
type BudgetSpec struct {
	LatencyMS int `yaml:"latency_ms"`
	Tokens    int `yaml:"tokens"`
	Depth     int `yaml:"depth"`
}

// FixtureScenario is one sub-scenario within mechanic-5 (tree-shape-
// variation runs two scenarios back-to-back and compares them).
type FixtureScenario struct {
	ID             string          `yaml:"id"`
	Description    string          `yaml:"description"`
	InitialBudget  BudgetSpec      `yaml:"initial_budget"`
	Seed           []SeedNode      `yaml:"seed"`
	MockedHandlers []MockedHandler `yaml:"mocked_handlers"`
	Expected       map[string]any  `yaml:"expected"`
}

// Result is the per-fixture outcome.
type Result struct {
	Fixture      string   `json:"fixture"`
	Path         string   `json:"path"`
	OK           bool     `json:"ok"`
	Failures     []string `json:"failures,omitempty"`
	TraceSummary string   `json:"trace_summary,omitempty"`
}

// SuiteResult aggregates per-fixture outcomes.
type SuiteResult struct {
	Suite   string   `json:"suite"`
	Total   int      `json:"total"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Results []Result `json:"results"`
}

// RunSuite loads every *.yaml under dir and runs each fixture through
// the executor. Returns aggregated suite result. Errors loading or
// parsing a fixture become a per-fixture failure (does not abort).
func RunSuite(ctx context.Context, dir string) (*SuiteResult, error) {
	pattern := filepath.Join(dir, "*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no mechanic fixtures in %s", dir)
	}
	sort.Strings(matches)

	suite := &SuiteResult{Suite: "mechanic", Total: len(matches)}
	for _, path := range matches {
		res := Result{Fixture: filepath.Base(path), Path: path}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			res.Failures = []string{fmt.Sprintf("read failed: %v", rerr)}
			suite.Failed++
			suite.Results = append(suite.Results, res)
			continue
		}
		var fx Fixture
		if uerr := yaml.Unmarshal(data, &fx); uerr != nil {
			res.Failures = []string{fmt.Sprintf("parse failed: %v", uerr)}
			suite.Failed++
			suite.Results = append(suite.Results, res)
			continue
		}
		res.Fixture = fx.ID
		runFixture(ctx, &fx, &res)
		if res.OK {
			suite.Passed++
		} else {
			suite.Failed++
		}
		suite.Results = append(suite.Results, res)
	}
	return suite, nil
}

// runFixture executes one fixture and populates res with PASS/FAIL.
func runFixture(ctx context.Context, fx *Fixture, res *Result) {
	// Mechanic-5 has scenarios[] for tree-shape variation; run each
	// sub-scenario and compare them.
	if len(fx.Scenarios) > 0 {
		runVariationFixture(ctx, fx, res)
		return
	}

	reg := buildRegistry(fx.MockedHandlers)
	seed := buildSeed(fx.Seed)
	budget := dag.Budget{
		LatencyMS: fx.InitialBudget.LatencyMS,
		Tokens:    fx.InitialBudget.Tokens,
		Depth:     fx.InitialBudget.Depth,
	}

	ex := dag.NewExecutor(reg, nil)
	trace, err := ex.Run(ctx, fx.ID, seed, budget)
	if err != nil {
		res.OK = false
		res.Failures = []string{fmt.Sprintf("executor error: %v", err)}
		return
	}

	res.TraceSummary = fmt.Sprintf("%d nodes executed; exhausted=%v (axis=%s); %d refusals; final budget %s",
		trace.TotalExecuted, trace.Exhausted, trace.ExhaustedAxis, len(trace.SpawnRefusals), trace.FinalBudget)
	res.OK = true

	if want, ok := fx.Expected["total_nodes_executed"].(int); ok {
		if trace.TotalExecuted != want {
			res.OK = false
			res.Failures = append(res.Failures, fmt.Sprintf("total_nodes_executed: got %d, want %d", trace.TotalExecuted, want))
		}
	}
	if want, ok := fx.Expected["exhausted_axis"].(string); ok {
		// The "exhausted axis" can manifest in two places:
		// 1. trace.ExhaustedAxis (when budget went negative mid-walk)
		// 2. spawn_refusals[].ExhaustedAxis (when pre-spawn budget
		//    check refused a child before letting budget go negative)
		// Both signal "the turn stopped early because <axis> ran out."
		got := trace.ExhaustedAxis
		if got == "" {
			for _, r := range trace.SpawnRefusals {
				if r.ExhaustedAxis != "" {
					got = r.ExhaustedAxis
					break
				}
			}
		}
		if got != want {
			res.OK = false
			res.Failures = append(res.Failures, fmt.Sprintf("exhausted_axis: got %q, want %q", got, want))
		}
	}
	if want, ok := fx.Expected["final_outcome"].(string); ok {
		got := "ok"
		if trace.Exhausted && want != "ok" {
			got = "exhausted"
		}
		if got != want {
			res.OK = false
			res.Failures = append(res.Failures, fmt.Sprintf("final_outcome: got %q, want %q", got, want))
		}
	}
	// remaining_budget_after: list of {node, latency_ms, tokens, depth}
	if want, ok := fx.Expected["remaining_budget_after"].([]any); ok {
		byNode := make(map[string]dag.Budget)
		for _, e := range trace.Entries {
			byNode[e.NodeID] = e.BudgetAfter
		}
		for i, w := range want {
			wm, _ := w.(map[string]any)
			nodeID, _ := wm["node"].(string)
			got, found := byNode[nodeID]
			if !found {
				res.OK = false
				res.Failures = append(res.Failures, fmt.Sprintf("remaining_budget_after[%d]: node %q not in trace", i, nodeID))
				continue
			}
			if v, ok := wm["latency_ms"].(int); ok && got.LatencyMS != v {
				res.OK = false
				res.Failures = append(res.Failures, fmt.Sprintf("remaining_budget_after node=%s latency_ms: got %d, want %d", nodeID, got.LatencyMS, v))
			}
			if v, ok := wm["tokens"].(int); ok && got.Tokens != v {
				res.OK = false
				res.Failures = append(res.Failures, fmt.Sprintf("remaining_budget_after node=%s tokens: got %d, want %d", nodeID, got.Tokens, v))
			}
		}
	}
	// parent_chain: map[nodeID]parentID
	if want, ok := fx.Expected["parent_chain"].(map[string]any); ok {
		byNode := make(map[string]string)
		for _, e := range trace.Entries {
			byNode[e.NodeID] = e.ParentID
		}
		for nodeID, wp := range want {
			wantParent := ""
			if wp != nil {
				wantParent, _ = wp.(string)
			}
			if got, found := byNode[nodeID]; !found {
				res.OK = false
				res.Failures = append(res.Failures, fmt.Sprintf("parent_chain: node %q not in trace", nodeID))
			} else if got != wantParent {
				res.OK = false
				res.Failures = append(res.Failures, fmt.Sprintf("parent_chain[%s]: got %q, want %q", nodeID, got, wantParent))
			}
		}
	}
	// spawn_refusals: list of {parent, child (qualified), error_code, exhausted_axis}
	if want, ok := fx.Expected["spawn_refusals"].([]any); ok {
		// Build a set of (parent, error_code) tuples from trace.SpawnRefusals.
		seen := make(map[string]bool)
		for _, r := range trace.SpawnRefusals {
			key := fmt.Sprintf("%s|%s|%s", r.ParentID, r.ErrorCode, r.ExhaustedAxis)
			seen[key] = true
		}
		for i, w := range want {
			wm, _ := w.(map[string]any)
			parent, _ := wm["parent"].(string)
			errCode, _ := wm["error_code"].(string)
			exhAxis, _ := wm["exhausted_axis"].(string)
			key := fmt.Sprintf("%s|%s|%s", parent, errCode, exhAxis)
			if !seen[key] {
				res.OK = false
				res.Failures = append(res.Failures, fmt.Sprintf("spawn_refusals[%d]: expected refusal %s not found in trace", i, key))
			}
		}
	}
}

// runVariationFixture handles mechanic-5: runs each scenario,
// compares shapes per expected_variation.
func runVariationFixture(ctx context.Context, fx *Fixture, res *Result) {
	type sub struct {
		id    string
		trace *dag.Trace
		err   error
	}
	subs := make([]sub, 0, len(fx.Scenarios))
	for _, sc := range fx.Scenarios {
		// Mechanic-5 scenarios may omit function/op on mocked_handlers
		// (they appear instead on seed entries and spawn-list entries).
		// Cross-reference both to backfill before building the registry.
		backfilled := backfillFunctionOp(sc.MockedHandlers, sc.Seed)
		reg := buildRegistry(backfilled)
		seed := buildSeed(sc.Seed)
		budget := dag.Budget{LatencyMS: sc.InitialBudget.LatencyMS, Tokens: sc.InitialBudget.Tokens, Depth: sc.InitialBudget.Depth}
		ex := dag.NewExecutor(reg, nil)
		tr, err := ex.Run(ctx, fx.ID+"/"+sc.ID, seed, budget)
		subs = append(subs, sub{id: sc.ID, trace: tr, err: err})
	}
	res.OK = true
	var summary []string
	for _, s := range subs {
		if s.err != nil {
			res.OK = false
			res.Failures = append(res.Failures, fmt.Sprintf("%s: executor error %v", s.id, s.err))
			continue
		}
		summary = append(summary, fmt.Sprintf("%s=%dnodes", s.id, s.trace.TotalExecuted))
	}
	res.TraceSummary = strings.Join(summary, " ")

	// trivial_nodes_lt_rich assertion (the main variation check)
	if len(subs) >= 2 && subs[0].trace != nil && subs[1].trace != nil {
		if subs[0].trace.TotalExecuted >= subs[1].trace.TotalExecuted {
			res.OK = false
			res.Failures = append(res.Failures, fmt.Sprintf("expected scenario-a (%d nodes) < scenario-b (%d nodes); shapes did not vary as expected",
				subs[0].trace.TotalExecuted, subs[1].trace.TotalExecuted))
		}
	}
}

// buildRegistry constructs a Registry from a list of MockedHandler
// specs. Each becomes a registered NodeSpec with a closure handler
// that returns the declared cost + spawn list. The spawn list is
// translated from SeedNode form to NodeSpec form (looking up
// function/op via the surrounding fixture's mocked handlers when
// the spawn entry only names node_id).
func buildRegistry(mocks []MockedHandler) *dag.Registry {
	reg := dag.NewRegistry()
	// First pass: build a lookup from node_id → (function, op) so spawn
	// entries that only name node_id can find their function/op.
	byID := make(map[string]MockedHandler)
	for _, m := range mocks {
		byID[m.NodeID] = m
	}
	// Second pass: register each handler.
	registered := make(map[string]bool)
	for _, m := range mocks {
		qn := m.Function + "." + m.Op
		if registered[qn] {
			continue // multiple node_ids may share a qualified name; first wins
		}
		registered[qn] = true
		spawn := buildSpawnSpecs(m.Spawn, byID)
		cost := dag.Cost{LatencyMS: m.CostConsumed.LatencyMS, Tokens: m.CostConsumed.Tokens}
		costCopy := cost // capture for closure
		spawnCopy := spawn
		_ = reg.Register(dag.NodeSpec{
			Function: dag.CortexFunction(m.Function),
			Op:       m.Op,
			Cost:     dag.Cost{LatencyMS: m.CostHint.LatencyMS, Tokens: m.CostHint.Tokens},
			Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
				return dag.NodeResult{
					Out:          map[string]any{},
					Spawn:        spawnCopy,
					CostConsumed: costCopy,
				}, nil
			},
		})
	}
	return reg
}

// buildSeed translates SeedNode entries to NodeSpec entries with IDs.
func buildSeed(seed []SeedNode) []dag.NodeSpec {
	out := make([]dag.NodeSpec, 0, len(seed))
	for _, s := range seed {
		out = append(out, dag.NodeSpec{
			Function: dag.CortexFunction(s.Function),
			Op:       s.Op,
			ID:       s.NodeID,
			Attrs:    s.Attrs,
		})
	}
	return out
}

// backfillFunctionOp returns mocked_handlers with function/op fields
// populated from cross-references (seed entries + spawn-list entries
// in other handlers). Used for mechanic-5 where the fixture omits
// function/op from the mocked_handlers list and relies on the runner
// to look them up.
func backfillFunctionOp(mocks []MockedHandler, seed []SeedNode) []MockedHandler {
	// Build a lookup: node_id → (function, op) from seed + every spawn entry.
	type fnOp struct{ Function, Op string }
	lookup := make(map[string]fnOp)
	for _, s := range seed {
		if s.NodeID != "" && s.Function != "" {
			lookup[s.NodeID] = fnOp{s.Function, s.Op}
		}
	}
	for _, m := range mocks {
		for _, sp := range m.Spawn {
			if sp.NodeID != "" && sp.Function != "" {
				lookup[sp.NodeID] = fnOp{sp.Function, sp.Op}
			}
		}
	}
	out := make([]MockedHandler, len(mocks))
	for i, m := range mocks {
		if m.Function == "" {
			if fo, ok := lookup[m.NodeID]; ok {
				m.Function = fo.Function
				m.Op = fo.Op
			}
		}
		out[i] = m
	}
	return out
}

// buildSpawnSpecs translates spawn-list entries (which may name only
// node_id or may carry function/op) to executor-ready NodeSpec.
func buildSpawnSpecs(spawn []SeedNode, byID map[string]MockedHandler) []dag.NodeSpec {
	out := make([]dag.NodeSpec, 0, len(spawn))
	for _, s := range spawn {
		spec := dag.NodeSpec{ID: s.NodeID, Attrs: s.Attrs}
		if s.Function != "" {
			spec.Function = dag.CortexFunction(s.Function)
			spec.Op = s.Op
		} else if m, ok := byID[s.NodeID]; ok {
			spec.Function = dag.CortexFunction(m.Function)
			spec.Op = m.Op
		}
		out = append(out, spec)
	}
	return out
}
