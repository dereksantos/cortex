package study

import (
	"context"
	"testing"
)

// scriptedCurator returns a fixed sequence of decisions, then DONE.
type scriptedCurator struct {
	decisions []Decision
	i         int
}

func (s *scriptedCurator) Decide(StudyResponse, string) Decision {
	if s.i < len(s.decisions) {
		d := s.decisions[s.i]
		s.i++
		return d
	}
	return Decision{Kind: DecisionDone}
}

func passDigest(_ context.Context, in InferInput) (InferOutput, error) {
	// Echo the first sampled range so each pass has a distinct digest.
	d := "pass digest"
	if len(in.Sampled) > 0 {
		d = in.Sampled[0].RelPath
	}
	return InferOutput{Digest: d}, nil
}

func sampledKey(s SampledChunk) int64 { return s.ByteOffset }

func TestStudyLoop_DeepensUntilDone(t *testing.T) {
	path := writeBytesFile(t, 120000)
	cur := &scriptedCurator{decisions: []Decision{
		{Kind: DecisionDensify, Density: "normal"},
		{Kind: DecisionDensify, Density: "dense"},
		{Kind: DecisionDone},
	}}
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "sparse", Session: "s", Infer: passDigest,
	}, cur, 6)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if res.Stopped != "done" {
		t.Errorf("Stopped = %q, want done", res.Stopped)
	}
	if len(res.Passes) != 3 {
		t.Fatalf("want 3 passes (densify, densify, done), got %d", len(res.Passes))
	}
	// Each pass must sample NEW regions — coverage accumulates, doesn't repeat.
	seen := map[int64]bool{}
	for pi, p := range res.Passes {
		newThisPass := 0
		for _, s := range p.Response.Sampled {
			if !seen[sampledKey(s)] {
				newThisPass++
				seen[sampledKey(s)] = true
			}
		}
		if newThisPass == 0 {
			t.Errorf("pass %d sampled no new regions (deepening repeated instead of refining)", pi)
		}
	}
	// Cumulative coverage strictly increased across the deepening passes.
	if res.CoveragePct <= res.Passes[0].Response.Coverage.Pct {
		t.Errorf("cumulative coverage %.3f did not exceed first pass %.3f", res.CoveragePct, res.Passes[0].Response.Coverage.Pct)
	}
}

func TestStudyLoop_TargetThenDone(t *testing.T) {
	path := writeBytesFile(t, 200000)
	cur := &scriptedCurator{decisions: []Decision{
		{Kind: DecisionTarget, Focus: &Focus{Lines: [2]int{2000, 2100}}, Density: "normal"},
		{Kind: DecisionDone},
	}}
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "sparse", Session: "t", Infer: passDigest,
	}, cur, 4)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if res.Stopped != "done" || len(res.Passes) != 2 {
		t.Fatalf("want done in 2 passes, got stopped=%q passes=%d", res.Stopped, len(res.Passes))
	}
	// The TARGET pass should concentrate near the focus lines.
	inFocus := 0
	for _, s := range res.Passes[1].Response.Sampled {
		if s.LineStart <= 2100 && s.LineEnd >= 2000 {
			inFocus++
		}
	}
	if inFocus == 0 {
		t.Errorf("TARGET pass did not sample near the focus range")
	}
}

func TestStudyLoop_ReadModeStopsImmediately(t *testing.T) {
	path := writeBytesFile(t, 1000) // under threshold
	res, err := StudyLoop(context.Background(), StudyRequest{Path: path, Window: 8192}, nil, 4)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if res.Stopped != "read" || len(res.Passes) != 1 {
		t.Errorf("read-mode file should stop in 1 pass, got stopped=%q passes=%d", res.Stopped, len(res.Passes))
	}
}

func TestStudyLoop_DefaultCuratorStopsOnExhausted(t *testing.T) {
	// Small-but-over-threshold file → few chunks → the dense default draw
	// exhausts; the heuristic curator DONEs an exhausted study.
	path := writeBytesFile(t, 18000)
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "dense", Infer: passDigest,
	}, nil, 5)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(res.Passes) == 0 {
		t.Fatal("expected at least one pass")
	}
	if res.Stopped != "done" && res.Stopped != "exhausted" {
		t.Errorf("Stopped = %q, want done or exhausted", res.Stopped)
	}
}

func TestStudyLoop_RespectsMaxPasses(t *testing.T) {
	path := writeBytesFile(t, 400000) // big enough that sparse never exhausts quickly
	// A curator that always densifies but never DONEs → bounded by maxPasses.
	cur := &scriptedCurator{decisions: []Decision{
		{Kind: DecisionDensify, Density: "sparse"},
		{Kind: DecisionDensify, Density: "sparse"},
		{Kind: DecisionDensify, Density: "sparse"},
		{Kind: DecisionDensify, Density: "sparse"},
		{Kind: DecisionDensify, Density: "sparse"},
	}}
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "sparse", Infer: passDigest,
	}, cur, 2)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(res.Passes) != 2 {
		t.Errorf("maxPasses=2 should cap passes at 2, got %d", len(res.Passes))
	}
	if res.Stopped != "budget" {
		t.Errorf("Stopped = %q, want budget", res.Stopped)
	}
}
