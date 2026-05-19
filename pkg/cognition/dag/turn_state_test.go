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
