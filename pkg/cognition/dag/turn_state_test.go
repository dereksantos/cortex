package dag

import (
	"context"
	"testing"
)

// TestTurnState_PriorOut covers the round-trip: deposit a node's Out
// into state, then read it back via PriorOut(ctx, nodeID).
func TestTurnState_PriorOut(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)

	s.deposit("n1", "act.read_file", map[string]any{"output": "hello"})
	s.deposit("n2", "decide.coding_turn", map[string]any{"response": "world"})

	if out := PriorOut(ctx, "n1"); out["output"] != "hello" {
		t.Errorf("PriorOut(n1).output: got %v, want hello", out["output"])
	}
	if out := PriorOut(ctx, "n2"); out["response"] != "world" {
		t.Errorf("PriorOut(n2).response: got %v, want world", out["response"])
	}
	if out := PriorOut(ctx, "missing"); out != nil {
		t.Errorf("PriorOut(missing): got %v, want nil", out)
	}
}

// TestTurnState_PriorOutByName_LastWins — when multiple nodes share a
// qualified name (e.g., three act.read_file invocations in one turn),
// PriorOutByName returns the most recent Out.
func TestTurnState_PriorOutByName_LastWins(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)

	s.deposit("n1", "act.read_file", map[string]any{"output": "first"})
	s.deposit("n2", "act.read_file", map[string]any{"output": "second"})
	s.deposit("n3", "act.read_file", map[string]any{"output": "third"})

	got := PriorOutByName(ctx, "act.read_file")
	if got["output"] != "third" {
		t.Errorf("PriorOutByName: got %v, want third (most recent)", got["output"])
	}
}

// TestTurnState_PriorOutsByName returns every recorded Out in order.
func TestTurnState_PriorOutsByName(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)

	s.deposit("n1", "act.read_file", map[string]any{"output": "a"})
	s.deposit("n2", "act.read_file", map[string]any{"output": "b"})

	got := PriorOutsByName(ctx, "act.read_file")
	if len(got) != 2 {
		t.Fatalf("PriorOutsByName: len = %d, want 2", len(got))
	}
	if got[0]["output"] != "a" || got[1]["output"] != "b" {
		t.Errorf("PriorOutsByName order: got %v, want [a, b]", got)
	}
}

// TestTurnState_NilSafe — when the handler runs outside the executor
// (no turn state attached), the helpers return nil rather than panic.
func TestTurnState_NilSafe(t *testing.T) {
	ctx := context.Background()
	if out := PriorOut(ctx, "anything"); out != nil {
		t.Errorf("PriorOut without turn state: got %v, want nil", out)
	}
	if out := PriorOutByName(ctx, "act.read_file"); out != nil {
		t.Errorf("PriorOutByName without turn state: got %v, want nil", out)
	}
	if out := PriorOutsByName(ctx, "act.read_file"); out != nil {
		t.Errorf("PriorOutsByName without turn state: got %v, want nil", out)
	}
}

// TestTurnState_ExecutorIntegration — wire two handlers through the
// executor. Handler #2 reads handler #1's Out via PriorOut. Confirms
// the executor actually deposits the Out and the ctx is threaded.
func TestTurnState_ExecutorIntegration(t *testing.T) {
	reg := NewRegistry()

	produced := map[string]any{"value": 42}
	if err := reg.Register(NodeSpec{
		Function: FuncSense,
		Op:       "producer",
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			return NodeResult{
				Out: produced,
				Spawn: []NodeSpec{{
					Function: FuncAct,
					Op:       "consumer",
					ID:       "consumer-node",
					Attrs:    map[string]any{"upstream_id": NodeIDFromContext(ctx)},
				}},
				CostConsumed: Cost{LatencyMS: 1},
			}, nil
		},
	}); err != nil {
		t.Fatalf("register producer: %v", err)
	}

	var consumerSawOut map[string]any
	if err := reg.Register(NodeSpec{
		Function:     FuncAct,
		Op:           "consumer",
		AxisContract: &AxisContract{Mutator: false},
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			upstreamID, _ := in["upstream_id"].(string)
			consumerSawOut = PriorOut(ctx, upstreamID)
			return NodeResult{CostConsumed: Cost{LatencyMS: 1}}, nil
		},
	}); err != nil {
		t.Fatalf("register consumer: %v", err)
	}

	ex := NewExecutor(reg, nil)
	ex.SetSequential(true)
	seed := []NodeSpec{{Function: FuncSense, Op: "producer", ID: "producer-node"}}
	if _, err := ex.Run(context.Background(), "test-turn", seed, DefaultTurnBudget()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if consumerSawOut == nil {
		t.Fatalf("consumer didn't see producer's Out (PriorOut returned nil)")
	}
	if v, _ := consumerSawOut["value"].(int); v != 42 {
		t.Errorf("consumer saw Out[value]=%v, want 42", consumerSawOut["value"])
	}
}

// TestSequentialExecutor_DFSOrdering — when a parent emits multiple
// siblings and each sibling has children, the executor must run each
// sibling's children before the next sibling. This is what makes the
// REPL's [tool_call, tool_call, ..., synthesize] pattern work — the
// synthesizer needs to see prior nodes' Outs, which means the act.*
// children of earlier tool_calls must have completed first.
//
// Prior BFS-append ordering ran the synthesizer between siblings'
// emissions and their children's execution, defeating the
// prior-output injection. This test pins the DFS-prepend fix.
func TestSequentialExecutor_DFSOrdering(t *testing.T) {
	reg := NewRegistry()
	var execOrder []string

	// parent spawns three sibling "step" nodes; each step spawns one
	// "leaf". Expected DFS order: step1, leaf1, step2, leaf2, step3,
	// leaf3, synthesize. NOT BFS (which would give step1, step2,
	// step3, synthesize, leaf1, leaf2, leaf3).
	mkRecorder := func(id string) Handler {
		return func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			execOrder = append(execOrder, id)
			return NodeResult{CostConsumed: Cost{LatencyMS: 1}}, nil
		}
	}
	mkStep := func(id, leafID string) Handler {
		return func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			execOrder = append(execOrder, id)
			return NodeResult{
				Spawn: []NodeSpec{{
					Function: FuncAct, Op: "leaf-" + leafID,
					ID: leafID,
				}},
				CostConsumed: Cost{LatencyMS: 1},
			}, nil
		}
	}

	for _, qn := range []struct {
		fn, op string
		h      Handler
	}{
		{"decide", "parent", func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			execOrder = append(execOrder, "parent")
			return NodeResult{
				Spawn: []NodeSpec{
					{Function: FuncDecide, Op: "step-1", ID: "s1"},
					{Function: FuncDecide, Op: "step-2", ID: "s2"},
					{Function: FuncDecide, Op: "step-3", ID: "s3"},
					{Function: FuncDecide, Op: "synthesize", ID: "syn"},
				},
				CostConsumed: Cost{LatencyMS: 1},
			}, nil
		}},
		{"decide", "step-1", mkStep("s1", "l1")},
		{"decide", "step-2", mkStep("s2", "l2")},
		{"decide", "step-3", mkStep("s3", "l3")},
		{"decide", "synthesize", mkRecorder("syn")},
	} {
		err := reg.Register(NodeSpec{
			Function: CortexFunction(qn.fn),
			Op:       qn.op,
			Handler:  qn.h,
		})
		if err != nil {
			t.Fatalf("register %s.%s: %v", qn.fn, qn.op, err)
		}
	}
	for _, leafID := range []string{"l1", "l2", "l3"} {
		err := reg.Register(NodeSpec{
			Function:     FuncAct,
			Op:           "leaf-" + leafID,
			AxisContract: &AxisContract{Mutator: false},
			Handler:      mkRecorder(leafID),
		})
		if err != nil {
			t.Fatalf("register act.leaf-%s: %v", leafID, err)
		}
	}

	ex := NewExecutor(reg, nil)
	ex.SetSequential(true)
	seed := []NodeSpec{{Function: FuncDecide, Op: "parent", ID: "p"}}
	if _, err := ex.Run(context.Background(), "dfs-test", seed, DefaultTurnBudget()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"parent", "s1", "l1", "s2", "l2", "s3", "l3", "syn"}
	if len(execOrder) != len(want) {
		t.Fatalf("exec order length: got %d (%v), want %d (%v)", len(execOrder), execOrder, len(want), want)
	}
	for i, w := range want {
		if execOrder[i] != w {
			t.Errorf("exec order[%d]: got %q, want %q (full=%v)", i, execOrder[i], w, execOrder)
		}
	}
}

func TestLatestAccumulatorSnapshot_ReturnsLatestSnapshot(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)

	s.deposit("n1", "attend.accumulate", map[string]any{"snapshot": "snap-1"})
	s.deposit("n2", "act.read_file", map[string]any{"output": "ignored"})
	s.deposit("n3", "attend.accumulate", map[string]any{"snapshot": "snap-2"})

	if got := LatestAccumulatorSnapshot(ctx); got != "snap-2" {
		t.Errorf("LatestAccumulatorSnapshot: got %q, want %q", got, "snap-2")
	}
}

func TestLatestAccumulatorSnapshot_EmptyWhenNoneRun(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)
	if got := LatestAccumulatorSnapshot(ctx); got != "" {
		t.Errorf("LatestAccumulatorSnapshot: got %q, want empty", got)
	}
}

func TestLatestAccumulatorSnapshot_EmptyWithoutTurnState(t *testing.T) {
	if got := LatestAccumulatorSnapshot(context.Background()); got != "" {
		t.Errorf("LatestAccumulatorSnapshot without turn state: got %q, want empty", got)
	}
}

func TestDepositAccumulatorSnapshot_VisibleViaLatest(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)
	id := DepositAccumulatorSnapshot(ctx, "fact A", 12, false)
	if id == "" {
		t.Fatal("DepositAccumulatorSnapshot should return a non-empty ID when turn state is attached")
	}
	if got := LatestAccumulatorSnapshot(ctx); got != "fact A" {
		t.Errorf("LatestAccumulatorSnapshot after deposit: got %q, want %q", got, "fact A")
	}
}

func TestDepositAccumulatorSnapshot_LatestWinsAcrossMultipleDeposits(t *testing.T) {
	s := newTurnState()
	ctx := withTurnState(context.Background(), s)
	DepositAccumulatorSnapshot(ctx, "first", 10, false)
	DepositAccumulatorSnapshot(ctx, "second", 12, false)
	DepositAccumulatorSnapshot(ctx, "third", 14, true)
	if got := LatestAccumulatorSnapshot(ctx); got != "third" {
		t.Errorf("LatestAccumulatorSnapshot: got %q, want %q (most recent deposit)", got, "third")
	}
}

func TestDepositAccumulatorSnapshot_NoOpWithoutTurnState(t *testing.T) {
	id := DepositAccumulatorSnapshot(context.Background(), "anything", 1, false)
	if id != "" {
		t.Errorf("DepositAccumulatorSnapshot without turn state: got %q, want empty (no-op)", id)
	}
}
