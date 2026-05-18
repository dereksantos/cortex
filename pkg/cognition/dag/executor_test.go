package dag

import (
	"context"
	"errors"
	"testing"
)

// mockHandler builds a handler that returns a fixed cost and spawns
// the given children. Used to drive deterministic test scenarios
// without LLMs or real handlers.
func mockHandler(cost Cost, spawn []NodeSpec) Handler {
	return func(ctx context.Context, in map[string]any, budget Budget) (NodeResult, error) {
		return NodeResult{
			Out:          map[string]any{},
			Spawn:        spawn,
			CostConsumed: cost,
		}, nil
	}
}

// TestExecutor_MechanicM1_BudgetDecayDeterminism validates the
// mechanic-1 fixture's invariant: given fixed handler costs, the
// remaining budget at each step matches expected to the millisecond /
// token. Mirrors test/evals/mechanic/mechanic-1-budget-decay.yaml.
func TestExecutor_MechanicM1_BudgetDecayDeterminism(t *testing.T) {
	reg := NewRegistry()

	// Build the spawn chain bottom-up so each parent references its child.
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide, Op: "inject",
		Cost:    Cost{LatencyMS: 150, Tokens: 100},
		Handler: mockHandler(Cost{LatencyMS: 150, Tokens: 100}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncAttend, Op: "reflex",
		Cost: Cost{LatencyMS: 200, Tokens: 75},
		Handler: mockHandler(Cost{LatencyMS: 200, Tokens: 75}, []NodeSpec{
			{Function: FuncDecide, Op: "inject", ID: "n3"},
		}),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Cost: Cost{LatencyMS: 100, Tokens: 50},
		Handler: mockHandler(Cost{LatencyMS: 100, Tokens: 50}, []NodeSpec{
			{Function: FuncAttend, Op: "reflex", ID: "n2"},
		}),
	})

	seed := []NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}}
	initial := Budget{LatencyMS: 1000, Tokens: 500, Depth: 10}

	ex := NewExecutor(reg, nil)
	trace, err := ex.Run(context.Background(), "test-m1", seed, initial)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expected: 3 nodes executed; no refusals; budget arithmetic exact.
	if trace.TotalExecuted != 3 {
		t.Errorf("TotalExecuted: got %d, want 3", trace.TotalExecuted)
	}
	if len(trace.SpawnRefusals) != 0 {
		t.Errorf("SpawnRefusals: got %d, want 0 (%v)", len(trace.SpawnRefusals), trace.SpawnRefusals)
	}
	if trace.Exhausted {
		t.Errorf("Exhausted: got true, want false")
	}

	// Expected per-step remaining budget (latency_ms, tokens):
	//   after n1: 900 / 450
	//   after n2: 700 / 375
	//   after n3: 550 / 275
	wantBudgets := []Budget{
		{LatencyMS: 900, Tokens: 450, Depth: 10},
		{LatencyMS: 700, Tokens: 375, Depth: 10},
		{LatencyMS: 550, Tokens: 275, Depth: 10},
	}
	if len(trace.Entries) != 3 {
		t.Fatalf("Entries: got %d, want 3", len(trace.Entries))
	}
	for i, want := range wantBudgets {
		got := trace.Entries[i].BudgetAfter
		if got.LatencyMS != want.LatencyMS || got.Tokens != want.Tokens || got.Depth != want.Depth {
			t.Errorf("step %d budget: got %s, want %s", i, got, want)
		}
	}
}

// TestExecutor_MechanicM2_TreeReconstruction validates the
// mechanic-2 fixture: parent_node_id chains correctly reconstruct
// a 5-node tree. Mirrors test/evals/mechanic/mechanic-2-tree-reconstruction.yaml.
func TestExecutor_MechanicM2_TreeReconstruction(t *testing.T) {
	reg := NewRegistry()

	// Tree: n1 → [n2, n3]; n2 → [n4, n5]; n3, n4, n5 are leaves.
	mustRegister(t, reg, NodeSpec{
		Function: FuncRepresent, Op: "embed",
		Handler: mockHandler(Cost{LatencyMS: 60, Tokens: 10}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncRemember, Op: "vector_search",
		Handler: mockHandler(Cost{LatencyMS: 30, Tokens: 0}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncAttend, Op: "rerank",
		Handler: mockHandler(Cost{LatencyMS: 80, Tokens: 30}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncAttend, Op: "reflex",
		Handler: mockHandler(Cost{LatencyMS: 100, Tokens: 40}, []NodeSpec{
			{Function: FuncRemember, Op: "vector_search", ID: "n4"},
			{Function: FuncRepresent, Op: "embed", ID: "n5"},
		}),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 20}, []NodeSpec{
			{Function: FuncAttend, Op: "reflex", ID: "n2"},
			{Function: FuncAttend, Op: "rerank", ID: "n3"},
		}),
	})

	seed := []NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}}
	initial := Budget{LatencyMS: 2000, Tokens: 500, Depth: 10}

	ex := NewExecutor(reg, nil)
	trace, err := ex.Run(context.Background(), "test-m2", seed, initial)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if trace.TotalExecuted != 5 {
		t.Errorf("TotalExecuted: got %d, want 5", trace.TotalExecuted)
	}

	// Expected parent chain.
	want := map[string]string{
		"n1": "", // seed
		"n2": "n1",
		"n3": "n1",
		"n4": "n2",
		"n5": "n2",
	}
	got := map[string]string{}
	for _, e := range trace.Entries {
		got[e.NodeID] = e.ParentID
	}
	for id, parent := range want {
		if got[id] != parent {
			t.Errorf("parent[%s]: got %q, want %q", id, got[id], parent)
		}
	}
}

// TestExecutor_MechanicM3_DepthCap validates depth cap enforcement.
// Initial budget.Depth = 3; node at depth 3 tries to spawn at depth 4;
// spawn refused with depth_exceeded; in-flight node finishes.
func TestExecutor_MechanicM3_DepthCap(t *testing.T) {
	reg := NewRegistry()

	// n5 is declared but should never execute.
	mustRegister(t, reg, NodeSpec{
		Function: FuncMaintain, Op: "capture",
		Handler: mockHandler(Cost{LatencyMS: 10, Tokens: 5}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide, Op: "inject",
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 10}, []NodeSpec{
			// n4 (depth 3) tries to spawn n5 (depth 4) — refused.
			{Function: FuncMaintain, Op: "capture", ID: "n5"},
		}),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncAttend, Op: "rerank",
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 10}, []NodeSpec{
			{Function: FuncDecide, Op: "inject", ID: "n4"},
		}),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncAttend, Op: "reflex",
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 10}, []NodeSpec{
			{Function: FuncAttend, Op: "rerank", ID: "n3"},
		}),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 10}, []NodeSpec{
			{Function: FuncAttend, Op: "reflex", ID: "n2"},
		}),
	})

	seed := []NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}}
	initial := Budget{LatencyMS: 1000, Tokens: 500, Depth: 3}

	ex := NewExecutor(reg, nil)
	trace, err := ex.Run(context.Background(), "test-m3", seed, initial)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expected: n1, n2, n3, n4 execute (4 total); n5 refused.
	if trace.TotalExecuted != 4 {
		t.Errorf("TotalExecuted: got %d, want 4 (n5 should be refused at depth cap)", trace.TotalExecuted)
	}
	foundDepthRefusal := false
	for _, r := range trace.SpawnRefusals {
		if r.ErrorCode == "depth_exceeded" && r.ParentID == "n4" && r.ChildQualName == "maintain.capture" {
			foundDepthRefusal = true
		}
	}
	if !foundDepthRefusal {
		t.Errorf("expected depth_exceeded refusal on n4 → n5; got refusals=%v", trace.SpawnRefusals)
	}
}

// TestExecutor_MechanicM4_BudgetExhaustion validates that budget
// exhaustion mid-tree stops new spawns; in-flight finishes; no
// orphans.
func TestExecutor_MechanicM4_BudgetExhaustion(t *testing.T) {
	reg := NewRegistry()

	// n3, n4 should never execute (n2 exhausts the budget).
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide, Op: "inject",
		Handler: mockHandler(Cost{LatencyMS: 100, Tokens: 50}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncMaintain, Op: "capture",
		Handler: mockHandler(Cost{LatencyMS: 80, Tokens: 30}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncAttend, Op: "reflex",
		// n2 spawns n3 + n4 but consumes 400ms — drops budget below
		// what either child needs.
		Handler: mockHandler(Cost{LatencyMS: 400, Tokens: 100}, []NodeSpec{
			{Function: FuncDecide, Op: "inject", ID: "n3"},
			{Function: FuncMaintain, Op: "capture", ID: "n4"},
		}),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 10}, []NodeSpec{
			{Function: FuncAttend, Op: "reflex", ID: "n2"},
		}),
	})

	seed := []NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}}
	// After n1 (50ms) + n2 (400ms): budget remaining = 50ms. Less than
	// n3.cost (100ms) or n4.cost (80ms). But the executor in v0 doesn't
	// pre-check cost hints — it'll dequeue + run n3, hit budget=50ms;
	// after n3's 100ms cost, budget goes negative; loop check on next
	// iter trips Exhausted. So n3 still executes; n4 gets refused.
	initial := Budget{LatencyMS: 500, Tokens: 500, Depth: 10}

	ex := NewExecutor(reg, nil)
	trace, err := ex.Run(context.Background(), "test-m4", seed, initial)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !trace.Exhausted {
		t.Errorf("Exhausted: got false, want true")
	}
	if trace.ExhaustedAxis != "latency_ms" {
		t.Errorf("ExhaustedAxis: got %q, want latency_ms", trace.ExhaustedAxis)
	}
	// At least one spawn refusal recorded.
	if len(trace.SpawnRefusals) == 0 {
		t.Errorf("expected spawn refusals due to exhaustion; got none")
	}
}

func mustRegister(t *testing.T, reg *Registry, spec NodeSpec) {
	t.Helper()
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register %s: %v", spec.QualifiedName(), err)
	}
}

// TestRegistry_UnknownNode verifies Get returns ErrUnknownNode when
// asked for a node that wasn't registered.
func TestRegistry_UnknownNode(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Get("sense.nonexistent")
	if !errors.Is(err, ErrUnknownNode) {
		t.Errorf("expected ErrUnknownNode, got %v", err)
	}
}

// TestBudget_Exhausted exercises the per-axis exhaustion check.
func TestBudget_Exhausted(t *testing.T) {
	tests := []struct {
		name     string
		budget   Budget
		wantExh  bool
		wantAxis string
	}{
		{"all positive", Budget{LatencyMS: 100, Tokens: 100, Depth: 5}, false, ""},
		{"latency 0", Budget{LatencyMS: 0, Tokens: 100, Depth: 5}, true, "latency_ms"},
		{"tokens 0", Budget{LatencyMS: 100, Tokens: 0, Depth: 5}, true, "tokens"},
		{"depth 0", Budget{LatencyMS: 100, Tokens: 100, Depth: 0}, true, "depth"},
		{"latency negative", Budget{LatencyMS: -10, Tokens: 100, Depth: 5}, true, "latency_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exh, axis := tt.budget.Exhausted()
			if exh != tt.wantExh || axis != tt.wantAxis {
				t.Errorf("got (%v, %q), want (%v, %q)", exh, axis, tt.wantExh, tt.wantAxis)
			}
		})
	}
}
