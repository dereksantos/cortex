package dag

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestFileDeferredQueue_AppendReadRoundtrip — basic write/read cycle.
func TestFileDeferredQueue_AppendReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "deferred.jsonl"), time.Hour)

	ds := DeferredSpawn{
		TurnID:       "turn-1",
		ParentNodeID: "n5",
		Child:        NodeSpec{Function: FuncDecide, Op: "inject"},
		Reason:       "latency_ms",
	}
	if err := q.Append(ds); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("ReadAndConsume: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].TurnID != "turn-1" || got[0].ParentNodeID != "n5" {
		t.Errorf("roundtrip lost identity: %+v", got[0])
	}
	if got[0].Child.QualifiedName() != "decide.inject" {
		t.Errorf("roundtrip lost child qname: %s", got[0].Child.QualifiedName())
	}
	if got[0].DeferredAt.IsZero() {
		t.Errorf("DeferredAt should be auto-populated; got zero")
	}
}

// TestNodeSpec_SalienceJSONRoundtrip — a SalienceContract set by a
// parent at spawn time must survive the deferred-queue projection so a
// rolled-over spawn keeps its compression contract.
func TestNodeSpec_SalienceJSONRoundtrip(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "deferred.jsonl"), time.Hour)

	ds := DeferredSpawn{
		TurnID:       "turn-2",
		ParentNodeID: "n3",
		Child: NodeSpec{
			Function: FuncAct, Op: "read_file",
			Salience: &SalienceContract{MaxOutputTokens: 200, Intent: "find TODOs"},
		},
		Reason: "latency_ms",
	}
	if err := q.Append(ds); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("ReadAndConsume: %v", err)
	}
	if len(got) != 1 || got[0].Child.Salience == nil {
		t.Fatalf("expected Salience to survive roundtrip, got %+v", got)
	}
	if got[0].Child.Salience.MaxOutputTokens != 200 || got[0].Child.Salience.Intent != "find TODOs" {
		t.Errorf("Salience fields lost: %+v", got[0].Child.Salience)
	}
}

// TestFileDeferredQueue_StaleDropped — entries older than MaxAge
// are dropped on read.
func TestFileDeferredQueue_StaleDropped(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "deferred.jsonl"), 100*time.Millisecond)

	staleDS := DeferredSpawn{
		TurnID:       "stale-turn",
		ParentNodeID: "n1",
		Child:        NodeSpec{Function: FuncDecide, Op: "inject"},
		DeferredAt:   time.Now().Add(-1 * time.Hour),
	}
	freshDS := DeferredSpawn{
		TurnID:       "fresh-turn",
		ParentNodeID: "n2",
		Child:        NodeSpec{Function: FuncMaintain, Op: "capture"},
	}
	if err := q.Append(staleDS); err != nil {
		t.Fatalf("Append stale: %v", err)
	}
	if err := q.Append(freshDS); err != nil {
		t.Fatalf("Append fresh: %v", err)
	}

	got, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("ReadAndConsume: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1 (stale dropped)", len(got))
	}
	if got[0].TurnID != "fresh-turn" {
		t.Errorf("wrong record kept: %+v", got[0])
	}
}

// TestFileDeferredQueue_ReadConsumes — after ReadAndConsume, the
// queue is empty (records replay exactly once).
func TestFileDeferredQueue_ReadConsumes(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "deferred.jsonl"), time.Hour)

	for i := 0; i < 3; i++ {
		if err := q.Append(DeferredSpawn{
			TurnID:       "turn-x",
			ParentNodeID: "p",
			Child:        NodeSpec{Function: FuncDecide, Op: "inject"},
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	first, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("first ReadAndConsume: %v", err)
	}
	// All 3 entries dedupe to 1 by (turn, parent, qname).
	if len(first) != 1 {
		t.Fatalf("first read: got %d, want 1 (dedup)", len(first))
	}

	second, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("second ReadAndConsume: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("queue not consumed; second read returned %d records", len(second))
	}
}

// TestFileDeferredQueue_MissingFile — reading an absent file is a
// no-op, not an error.
func TestFileDeferredQueue_MissingFile(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "absent.jsonl"), time.Hour)
	got, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("ReadAndConsume on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result on missing file; got %d records", len(got))
	}
}

// TestFileDeferredQueue_ConcurrentAppends — in-process mutex +
// flock keep concurrent appends from corrupting lines.
func TestFileDeferredQueue_ConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deferred.jsonl")
	q := NewFileDeferredQueue(path, time.Hour)

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = q.Append(DeferredSpawn{
				TurnID:       "concurrent",
				ParentNodeID: "p",
				Child:        NodeSpec{Function: FuncDecide, Op: "inject", ID: string(rune('a' + i%26))},
			})
		}(i)
	}
	wg.Wait()

	// Count lines in the file directly — every Append should have
	// produced one line, none corrupted.
	bb, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	nl := 0
	for _, b := range bb {
		if b == '\n' {
			nl++
		}
	}
	if nl != N {
		t.Errorf("got %d newlines, want %d (interleaved or lost appends)", nl, N)
	}
}

// TestExecutor_RolloverAppendsBudgetRefusals — when scheduleChildren
// refuses for budget_exceeded AND a DeferredQueue is wired, the
// refusal is also appended to the queue.
func TestExecutor_RolloverAppendsBudgetRefusals(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "deferred.jsonl"), time.Hour)

	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide, Op: "inject",
		Cost:    Cost{LatencyMS: 1000, Tokens: 0},
		Handler: mockHandler(Cost{LatencyMS: 1000}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Handler: mockHandler(Cost{LatencyMS: 50}, []NodeSpec{
			{Function: FuncDecide, Op: "inject", ID: "n2"},
		}),
	})

	ex := NewExecutor(reg, nil)
	ex.SetDeferredQueue(q)
	// Budget = 100ms; cost of decide.inject = 1000ms; pre-spawn
	// CanAfford check refuses with budget_exceeded.
	_, err := ex.Run(context.Background(), "turn-A",
		[]NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}},
		Budget{LatencyMS: 100, Tokens: 500, Depth: 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	deferred, err := q.ReadAndConsume()
	if err != nil {
		t.Fatalf("ReadAndConsume: %v", err)
	}
	if len(deferred) != 1 {
		t.Fatalf("got %d deferred spawns, want 1", len(deferred))
	}
	if deferred[0].TurnID != "turn-A" {
		t.Errorf("TurnID: got %q, want turn-A", deferred[0].TurnID)
	}
	if deferred[0].ParentNodeID != "n1" {
		t.Errorf("ParentNodeID: got %q, want n1", deferred[0].ParentNodeID)
	}
	if deferred[0].Child.QualifiedName() != "decide.inject" {
		t.Errorf("Child qname: got %s, want decide.inject", deferred[0].Child.QualifiedName())
	}
}

// TestExecutor_RolloverReplaysOnNextRun — fresh deferred spawns are
// prepended to the next Run's seed, with ParentNodeID preserved on
// the trace for cross-turn lineage.
func TestExecutor_RolloverReplaysOnNextRun(t *testing.T) {
	dir := t.TempDir()
	q := NewFileDeferredQueue(filepath.Join(dir, "deferred.jsonl"), time.Hour)

	// Seed the queue with a deferred spawn that came from a prior turn.
	if err := q.Append(DeferredSpawn{
		TurnID:       "turn-prior",
		ParentNodeID: "prior-n5",
		Child:        NodeSpec{Function: FuncDecide, Op: "inject", ID: "replayed-1"},
	}); err != nil {
		t.Fatalf("seed Append: %v", err)
	}

	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide, Op: "inject",
		Handler: mockHandler(Cost{LatencyMS: 10, Tokens: 5}, nil),
	})
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Handler: mockHandler(Cost{LatencyMS: 10, Tokens: 5}, nil),
	})

	ex := NewExecutor(reg, nil)
	ex.SetDeferredQueue(q)
	trace, err := ex.Run(context.Background(), "turn-next",
		[]NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}},
		Budget{LatencyMS: 1000, Tokens: 500, Depth: 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expect 2 nodes: the deferred (replayed-1) + the user seed (n1).
	if trace.TotalExecuted != 2 {
		t.Errorf("TotalExecuted: got %d, want 2 (deferred + seed)", trace.TotalExecuted)
	}

	// Find the replayed entry and check parent lineage.
	var replayed *TraceEntry
	for i := range trace.Entries {
		if trace.Entries[i].NodeID == "replayed-1" {
			replayed = &trace.Entries[i]
		}
	}
	if replayed == nil {
		t.Fatalf("replayed deferred spawn not in trace; entries=%+v", trace.Entries)
	}
	if replayed.ParentID != "prior-n5" {
		t.Errorf("ParentID lost across rollover: got %q, want prior-n5", replayed.ParentID)
	}
}
