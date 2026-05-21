package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/internal/bootstrap"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// synthOutput builds a small BoundaryOutput for handler tests.
func synthOutput() *bootstrap.BoundaryOutput {
	out := &bootstrap.BoundaryOutput{
		ProjectRoot: "/x",
		RNGSeed:     1234,
		StateHash:   "synth",
	}
	for m := 0; m < 3; m++ {
		mod := bootstrap.Module{ID: "m" + itoaOps(m)}
		for c := 0; c < 5; c++ {
			ch := bootstrap.Chunk{
				ID:        mod.ID + "-c" + itoaOps(c),
				RelPath:   mod.ID + "/f" + itoaOps(c) + ".go",
				LineStart: 1,
				LineEnd:   10,
				EffLines:  10,
				ModuleID:  mod.ID,
			}
			out.Chunks = append(out.Chunks, ch)
			mod.ChunkIDs = append(mod.ChunkIDs, ch.ID)
			mod.EffLines += 10
			mod.Lines += 10
			mod.Files++
		}
		out.Modules = append(out.Modules, mod)
	}
	out.TotalFiles = 15
	out.TotalLines = 150
	out.EffTotalLines = 150
	return out
}

func itoaOps(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestFractalSample_Handler(t *testing.T) {
	spec := FractalSampleSpec(FractalSampleConfig{})
	out := synthOutput()
	res, err := spec.Handler(context.Background(),
		map[string]any{"boundary_output": out, "k": 3},
		dag.Budget{LatencyMS: 1000})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ids, ok := res.Out["chunk_ids"].([]string)
	if !ok {
		t.Fatal("chunk_ids missing")
	}
	if len(ids) != 3 {
		t.Errorf("chunk_ids len = %d, want 3", len(ids))
	}
	chunks, ok := res.Out["chunks"].([]bootstrap.Chunk)
	if !ok {
		t.Fatal("chunks missing")
	}
	if len(chunks) != len(ids) {
		t.Errorf("chunks len = %d, want %d", len(chunks), len(ids))
	}
	// Verify order: chunks[i].ID == ids[i].
	for i, id := range ids {
		if chunks[i].ID != id {
			t.Errorf("order drift at %d: chunk.ID=%q id=%q", i, chunks[i].ID, id)
		}
	}
	if name := res.Out["sampler"].(string); name != "hierarchical-v1" {
		t.Errorf("sampler = %q, want hierarchical-v1", name)
	}
}

func TestFractalSample_RespectsCovered(t *testing.T) {
	spec := FractalSampleSpec(FractalSampleConfig{})
	out := synthOutput()
	covered := map[string]bool{}
	for _, c := range out.Chunks {
		if c.ModuleID == "m0" {
			covered[c.ID] = true
		}
	}
	res, err := spec.Handler(context.Background(),
		map[string]any{"boundary_output": out, "k": 5, "covered": covered, "rng_seed": int64(7)},
		dag.Budget{LatencyMS: 1000})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ids := res.Out["chunk_ids"].([]string)
	for _, id := range ids {
		if covered[id] {
			t.Errorf("covered chunk drawn: %s", id)
		}
	}
}

func TestFractalSample_DefaultSeedFromBoundary(t *testing.T) {
	spec := FractalSampleSpec(FractalSampleConfig{})
	out := synthOutput()
	res1, err := spec.Handler(context.Background(),
		map[string]any{"boundary_output": out, "k": 3},
		dag.Budget{LatencyMS: 1000})
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	res2, err := spec.Handler(context.Background(),
		map[string]any{"boundary_output": out, "k": 3, "rng_seed": out.RNGSeed},
		dag.Budget{LatencyMS: 1000})
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	ids1 := res1.Out["chunk_ids"].([]string)
	ids2 := res2.Out["chunk_ids"].([]string)
	if len(ids1) != len(ids2) {
		t.Fatalf("len mismatch: %d vs %d", len(ids1), len(ids2))
	}
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Errorf("default seed didn't fall back to BoundaryOutput.RNGSeed at %d", i)
		}
	}
}

func TestFractalSample_MissingBoundaryErrors(t *testing.T) {
	spec := FractalSampleSpec(FractalSampleConfig{})
	_, err := spec.Handler(context.Background(),
		map[string]any{"k": 3},
		dag.Budget{LatencyMS: 1000})
	if err == nil {
		t.Error("expected error on missing boundary_output")
	}
}
