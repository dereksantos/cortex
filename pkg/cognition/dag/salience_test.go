package dag

import (
	"context"
	"strings"
	"testing"
)

// fakeCompressOp registers an attend.compress handler the test can
// drive deterministically — truncates `raw` to max_tokens × 4 chars and
// returns it as the `compressed` field with a marker. Equivalent to
// the real stub but independent of the ops package so this test stays
// in pkg/cognition/dag.
func fakeCompressOp() NodeSpec {
	return NodeSpec{
		Function: FuncAttend, Op: "compress",
		Cost: Cost{LatencyMS: 5, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			raw, _ := in["raw"].(string)
			maxTok, _ := in["max_tokens"].(int)
			if maxTok == 0 {
				return NodeResult{Out: map[string]any{"compressed": raw}}, nil
			}
			maxBytes := maxTok * 4
			if len(raw) <= maxBytes {
				return NodeResult{Out: map[string]any{"compressed": raw}}, nil
			}
			compressed := raw[:maxBytes] + " […]"
			return NodeResult{
				Out:          map[string]any{"compressed": compressed},
				CostConsumed: Cost{LatencyMS: 5, OutputTokens: maxTok},
			}, nil
		},
	}
}

// TestExecutor_SalienceCompressionShrinksOversizedDeposit pins the
// Phase-2 contract: a node returning a string larger than its
// SalienceContract.MaxOutputTokens gets that field replaced by the
// attend.compress output, a synthetic child trace row is emitted with
// the parent's ID as ParentID, and the parent's SpawnedChildren list
// includes the synthetic child.
func TestExecutor_SalienceCompressionShrinksOversizedDeposit(t *testing.T) {
	reg := NewRegistry()
	mustRegister(t, reg, fakeCompressOp())

	bigPayload := strings.Repeat("abcd", 500) // 2000 chars ~ 500 tokens
	mustRegister(t, reg, NodeSpec{
		Function: FuncAct, Op: "read_file",
		AxisContract: &AxisContract{Mutator: false},
		Cost:         Cost{LatencyMS: 10, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			return NodeResult{Out: map[string]any{"output": bigPayload}}, nil
		},
	})

	var emitted []TraceEntry
	ex := NewExecutor(reg, func(e TraceEntry) { emitted = append(emitted, e) })
	ex.SetSequential(true)

	seed := []NodeSpec{{
		Function: FuncAct, Op: "read_file", ID: "n1",
		Salience: &SalienceContract{MaxOutputTokens: 40, Intent: "find TODOs"},
	}}
	if _, err := ex.Run(context.Background(), "t-1", seed, Budget{LatencyMS: 60000, Tokens: 4000, Depth: 5, OutputTokens: 5000}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(emitted) != 2 {
		t.Fatalf("expected 2 trace entries (parent + compress child), got %d: %+v", len(emitted), emitted)
	}
	parent, child := emitted[0], emitted[1]
	if parent.QualifiedName != "act.read_file" || parent.NodeID != "n1" {
		t.Fatalf("parent row shape wrong: %+v", parent)
	}
	if child.QualifiedName != "attend.compress" || child.ParentID != "n1" {
		t.Fatalf("compression child row shape wrong: %+v", child)
	}
	if !child.OK {
		t.Errorf("compression child should be OK; got %+v", child)
	}
	// Parent's Out["output"] must be the compressed string, not the
	// raw 2000-char payload.
	got, _ := parent.Out["output"].(string)
	if len(got) >= len(bigPayload) {
		t.Errorf("parent Out not shrunk; got %d chars", len(got))
	}
	if !strings.Contains(got, "[…]") {
		t.Errorf("parent Out missing compression marker: %q", got)
	}
	// Parent's spawned_children must surface the synthetic compress ID.
	if len(parent.SpawnedChildren) == 0 || parent.SpawnedChildren[len(parent.SpawnedChildren)-1] != child.NodeID {
		t.Errorf("parent.SpawnedChildren should include compress child %q, got %v",
			child.NodeID, parent.SpawnedChildren)
	}
}

// TestExecutor_SalienceCompressionSkippedWhenUnderBudget — when the
// deposit already fits the SalienceContract, no synthetic compress row
// is emitted and the parent's Out passes through untouched. Guards
// against the hook firing spuriously and burning unnecessary tokens.
func TestExecutor_SalienceCompressionSkippedWhenUnderBudget(t *testing.T) {
	reg := NewRegistry()
	mustRegister(t, reg, fakeCompressOp())

	small := "hello world"
	mustRegister(t, reg, NodeSpec{
		Function: FuncAct, Op: "read_file",
		AxisContract: &AxisContract{Mutator: false},
		Cost:         Cost{LatencyMS: 10, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			return NodeResult{Out: map[string]any{"output": small}}, nil
		},
	})

	var emitted []TraceEntry
	ex := NewExecutor(reg, func(e TraceEntry) { emitted = append(emitted, e) })
	ex.SetSequential(true)
	seed := []NodeSpec{{
		Function: FuncAct, Op: "read_file", ID: "n1",
		Salience: &SalienceContract{MaxOutputTokens: 100, Intent: "anything"},
	}}
	if _, err := ex.Run(context.Background(), "t-2", seed, Budget{LatencyMS: 60000, Tokens: 4000, Depth: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(emitted) != 1 {
		t.Fatalf("expected 1 trace entry (no compression), got %d", len(emitted))
	}
	if got, _ := emitted[0].Out["output"].(string); got != small {
		t.Errorf("under-budget output should pass through, got %q", got)
	}
}

// TestExecutor_SalienceCompressionSkippedWhenNoContract — a node with
// no Salience set must not trigger compression even when its output is
// large. Pre-salience-budgets behavior preserved.
func TestExecutor_SalienceCompressionSkippedWhenNoContract(t *testing.T) {
	reg := NewRegistry()
	mustRegister(t, reg, fakeCompressOp())

	big := strings.Repeat("x", 4000)
	mustRegister(t, reg, NodeSpec{
		Function: FuncAct, Op: "read_file",
		AxisContract: &AxisContract{Mutator: false},
		Cost:         Cost{LatencyMS: 10, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			return NodeResult{Out: map[string]any{"output": big}}, nil
		},
	})

	var emitted []TraceEntry
	ex := NewExecutor(reg, func(e TraceEntry) { emitted = append(emitted, e) })
	ex.SetSequential(true)
	seed := []NodeSpec{{Function: FuncAct, Op: "read_file", ID: "n1"}}
	if _, err := ex.Run(context.Background(), "t-3", seed, Budget{LatencyMS: 60000, Tokens: 4000, Depth: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(emitted) != 1 {
		t.Errorf("expected 1 trace entry (no contract → no compression), got %d", len(emitted))
	}
}
