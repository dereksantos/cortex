package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/bootstrap"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func writeMiniFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	must("go.mod", "module example.com/m\n")
	must("main.go", "package main\n\nfunc main() {}\n")
	must("README.md", "# proj\n\nbody\n")
	return root
}

func TestScanBoundaries_Handler(t *testing.T) {
	root := writeMiniFixture(t)
	spec := ScanBoundariesSpec(ScanBoundariesConfig{})
	res, err := spec.Handler(context.Background(),
		map[string]any{"project_root": root, "window_lines": 400, "window_overlap": 40},
		dag.Budget{LatencyMS: 60000, Tokens: 1000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	bo, ok := res.Out["boundary_output"].(*bootstrap.BoundaryOutput)
	if !ok || bo == nil {
		t.Fatal("boundary_output missing")
	}
	if bo.TotalFiles == 0 {
		t.Error("TotalFiles=0; expected at least 1")
	}
	if res.Out["cached"].(bool) {
		t.Error("cached=true on first call (caching deferred)")
	}
	if res.Out["chunk_count"].(int) != len(bo.Chunks) {
		t.Errorf("chunk_count drift")
	}
	if res.Out["state_hash"].(string) != bo.StateHash {
		t.Errorf("state_hash drift")
	}
}

func TestScanBoundaries_MissingRootErrors(t *testing.T) {
	spec := ScanBoundariesSpec(ScanBoundariesConfig{})
	_, err := spec.Handler(context.Background(),
		map[string]any{},
		dag.Budget{LatencyMS: 1000})
	if err == nil {
		t.Error("expected error on missing project_root")
	}
}

func TestScanBoundaries_DeterministicAcrossCalls(t *testing.T) {
	root := writeMiniFixture(t)
	spec := ScanBoundariesSpec(ScanBoundariesConfig{})
	in := map[string]any{"project_root": root, "window_lines": 400, "window_overlap": 40, "salt": "abc"}
	r1, err := spec.Handler(context.Background(), in, dag.Budget{LatencyMS: 60000})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	r2, err := spec.Handler(context.Background(), in, dag.Budget{LatencyMS: 60000})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if r1.Out["state_hash"] != r2.Out["state_hash"] {
		t.Errorf("StateHash drift across calls")
	}
	if r1.Out["rng_seed"] != r2.Out["rng_seed"] {
		t.Errorf("RNGSeed drift across calls")
	}
	if r1.Out["chunk_count"] != r2.Out["chunk_count"] {
		t.Errorf("chunk_count drift across calls")
	}
}
