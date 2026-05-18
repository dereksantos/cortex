package dag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCalibrate_UpdatesRegistryCost — read traces, compute p50, apply
// to registry, persist snapshot, verify Cost reflects the median.
func TestCalibrate_UpdatesRegistryCost(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "dag_traces.jsonl")
	snapPath := filepath.Join(dir, "op_cost_hints.json")

	// 5 rows for sense.prompt: latencies 100,200,300,400,500 — p50=300
	//                          tokens   10,20,30,40,50      — p50=30
	// 3 rows for attend.reflex: latencies 50,150,250         — p50=150
	//                           tokens   5,15,25             — p50=15
	// 1 failed row (ok=false) — must be excluded from calibration
	rows := []map[string]any{
		{"qualified_name": "sense.prompt", "ok": true, "cost_latency_ms": 100, "cost_tokens": 10, "wall_start_unix_ns": int64(1000)},
		{"qualified_name": "sense.prompt", "ok": true, "cost_latency_ms": 200, "cost_tokens": 20, "wall_start_unix_ns": int64(2000)},
		{"qualified_name": "sense.prompt", "ok": true, "cost_latency_ms": 300, "cost_tokens": 30, "wall_start_unix_ns": int64(3000)},
		{"qualified_name": "sense.prompt", "ok": true, "cost_latency_ms": 400, "cost_tokens": 40, "wall_start_unix_ns": int64(4000)},
		{"qualified_name": "sense.prompt", "ok": true, "cost_latency_ms": 500, "cost_tokens": 50, "wall_start_unix_ns": int64(5000)},
		{"qualified_name": "sense.prompt", "ok": false, "cost_latency_ms": 99999, "cost_tokens": 99999, "wall_start_unix_ns": int64(6000)},
		{"qualified_name": "attend.reflex", "ok": true, "cost_latency_ms": 50, "cost_tokens": 5, "wall_start_unix_ns": int64(7000)},
		{"qualified_name": "attend.reflex", "ok": true, "cost_latency_ms": 150, "cost_tokens": 15, "wall_start_unix_ns": int64(8000)},
		{"qualified_name": "attend.reflex", "ok": true, "cost_latency_ms": 250, "cost_tokens": 25, "wall_start_unix_ns": int64(9000)},
	}
	f, err := os.Create(tracePath)
	if err != nil {
		t.Fatalf("create trace: %v", err)
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	f.Close()

	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{Function: FuncSense, Op: "prompt", Handler: mockHandler(Cost{}, nil)})
	mustRegister(t, reg, NodeSpec{Function: FuncAttend, Op: "reflex", Handler: mockHandler(Cost{}, nil)})

	snap, err := Calibrate(reg, CalibrateOptions{
		TracePath:    tracePath,
		SnapshotPath: snapPath,
		WindowSize:   100,
	})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	sense := snap.Hints["sense.prompt"]
	if sense.LatencyMS != 300 {
		t.Errorf("sense.prompt p50 latency: got %d, want 300", sense.LatencyMS)
	}
	if sense.Tokens != 30 {
		t.Errorf("sense.prompt p50 tokens: got %d, want 30", sense.Tokens)
	}
	if sense.Samples != 5 {
		t.Errorf("sense.prompt samples: got %d, want 5 (failed row excluded)", sense.Samples)
	}

	attend := snap.Hints["attend.reflex"]
	if attend.LatencyMS != 150 {
		t.Errorf("attend.reflex p50 latency: got %d, want 150", attend.LatencyMS)
	}
	if attend.Tokens != 15 {
		t.Errorf("attend.reflex p50 tokens: got %d, want 15", attend.Tokens)
	}

	// Registry was updated in place.
	gotSense, _ := reg.Get("sense.prompt")
	if gotSense.Cost.LatencyMS != 300 || gotSense.Cost.Tokens != 30 {
		t.Errorf("sense.prompt registry Cost: got %+v, want {300, 30}", gotSense.Cost)
	}

	// Snapshot was persisted.
	bb, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var disk CalibrationSnapshot
	if err := json.Unmarshal(bb, &disk); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(disk.Hints) != 2 {
		t.Errorf("snapshot hints: got %d, want 2", len(disk.Hints))
	}
	if disk.SourcePath != tracePath {
		t.Errorf("snapshot source_path: got %q, want %q", disk.SourcePath, tracePath)
	}
}

// TestCalibrate_MissingTraceFile — missing trace file is not an
// error (cold start), and produces an empty snapshot.
func TestCalibrate_MissingTraceFile(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{Function: FuncSense, Op: "prompt", Handler: mockHandler(Cost{}, nil)})

	snap, err := Calibrate(reg, CalibrateOptions{
		TracePath:    filepath.Join(dir, "absent.jsonl"),
		SnapshotPath: filepath.Join(dir, "hints.json"),
	})
	if err != nil {
		t.Fatalf("Calibrate on missing trace: %v", err)
	}
	if len(snap.Hints) != 0 {
		t.Errorf("expected empty hints on missing trace; got %d", len(snap.Hints))
	}
}

// TestCalibrate_UnregisteredOpsSkipped — calibration must not pull
// in stale traces of ops the current registry no longer knows about.
func TestCalibrate_UnregisteredOpsSkipped(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "dag_traces.jsonl")
	f, err := os.Create(tracePath)
	if err != nil {
		t.Fatalf("create trace: %v", err)
	}
	enc := json.NewEncoder(f)
	for i := 0; i < 3; i++ {
		_ = enc.Encode(map[string]any{
			"qualified_name":     "since-removed.op",
			"ok":                 true,
			"cost_latency_ms":    100,
			"cost_tokens":        10,
			"wall_start_unix_ns": int64(i + 1),
		})
	}
	f.Close()

	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Cost:    Cost{LatencyMS: 50, Tokens: 5},
		Handler: mockHandler(Cost{LatencyMS: 50, Tokens: 5}, nil),
	})

	_, err = Calibrate(reg, CalibrateOptions{
		TracePath:    tracePath,
		SnapshotPath: filepath.Join(dir, "hints.json"),
	})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	// sense.prompt Cost must be untouched (no observations).
	got, _ := reg.Get("sense.prompt")
	if got.Cost.LatencyMS != 50 || got.Cost.Tokens != 5 {
		t.Errorf("sense.prompt Cost mutated despite no observations: %+v", got.Cost)
	}
}

// TestLoadCalibrationSnapshot — round-trip via Calibrate + Load.
func TestLoadCalibrationSnapshot(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "dag_traces.jsonl")
	snapPath := filepath.Join(dir, "hints.json")

	f, err := os.Create(tracePath)
	if err != nil {
		t.Fatalf("create trace: %v", err)
	}
	enc := json.NewEncoder(f)
	for _, lat := range []int{100, 200, 300} {
		_ = enc.Encode(map[string]any{
			"qualified_name":     "sense.prompt",
			"ok":                 true,
			"cost_latency_ms":    lat,
			"cost_tokens":        lat / 10,
			"wall_start_unix_ns": int64(lat),
		})
	}
	f.Close()

	regA := NewRegistry()
	mustRegister(t, regA, NodeSpec{Function: FuncSense, Op: "prompt", Handler: mockHandler(Cost{}, nil)})
	if _, err := Calibrate(regA, CalibrateOptions{TracePath: tracePath, SnapshotPath: snapPath}); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	// Fresh registry — Load from disk should apply the same hints.
	regB := NewRegistry()
	mustRegister(t, regB, NodeSpec{Function: FuncSense, Op: "prompt", Handler: mockHandler(Cost{}, nil)})
	snap, err := LoadCalibrationSnapshot(regB, snapPath)
	if err != nil {
		t.Fatalf("LoadCalibrationSnapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("snap is nil; want loaded snapshot")
	}

	gotA, _ := regA.Get("sense.prompt")
	gotB, _ := regB.Get("sense.prompt")
	if gotA.Cost != gotB.Cost {
		t.Errorf("post-Load Cost mismatch: regA=%+v regB=%+v", gotA.Cost, gotB.Cost)
	}
	if gotB.Cost.LatencyMS != 200 {
		t.Errorf("loaded Cost.LatencyMS: got %d, want 200 (p50)", gotB.Cost.LatencyMS)
	}
}

// TestCalibrate_DoesNotDisturbMechanicEvals — mocked-handler runs
// shouldn't see their Cost mutated by calibration, because mock
// handlers self-report their cost via NodeResult.CostConsumed
// (which the executor applies regardless of registered Cost).
// This is the M4 invariant restated: registered Cost gates spawn
// scheduling; runtime cost is what the handler returns.
func TestCalibrate_DoesNotDisturbMechanicEvals(t *testing.T) {
	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncSense, Op: "prompt",
		Cost: Cost{LatencyMS: 1000, Tokens: 100}, // calibrated value
		Handler: func(ctx context.Context, in map[string]any, b Budget) (NodeResult, error) {
			return NodeResult{CostConsumed: Cost{LatencyMS: 50, Tokens: 10}}, nil
		},
	})

	ex := NewExecutor(reg, nil)
	trace, err := ex.Run(context.Background(), "m1-like",
		[]NodeSpec{{Function: FuncSense, Op: "prompt", ID: "n1"}},
		Budget{LatencyMS: 500, Tokens: 200, Depth: 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Cost applied = handler's CostConsumed (50/10), not registered (1000/100).
	if trace.Entries[0].CostConsumed.LatencyMS != 50 {
		t.Errorf("runtime cost: got %d, want 50 (handler-reported, not registered)", trace.Entries[0].CostConsumed.LatencyMS)
	}
}
