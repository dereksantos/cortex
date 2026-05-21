package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/internal/bootstrap"
	"github.com/dereksantos/cortex/internal/projectscan"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// ScanBoundariesConfig wires the sense.scan_project_boundaries op to
// its analyzer + cache dir. If Analyzer is nil, the handler builds a
// fresh bootstrap.UniversalAnalyzer per call using the input knobs.
//
// CacheDir is reserved for a future memoization layer (step 11 of the
// plan) — the handler currently always re-runs the analyzer and
// reports cached=false.
type ScanBoundariesConfig struct {
	Analyzer bootstrap.BoundaryAnalyzer
	CacheDir string
}

// ScanBoundariesSpec returns the NodeSpec for
// sense.scan_project_boundaries. Mechanical (no LLM); deterministic
// given the project's file state + window knobs + salt.
func ScanBoundariesSpec(cfg ScanBoundariesConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "scan_project_boundaries",
		Description: "scan project filesystem; emit chunks + edges + module tree (Tier 1, language-agnostic)",
		Inputs: []dag.ParamSpec{
			{Name: "project_root", Type: "string", Required: true},
			{Name: "window_lines", Type: "int", Required: false},
			{Name: "window_overlap", Type: "int", Required: false},
			{Name: "salt", Type: "string", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "boundary_output", Type: "*bootstrap.BoundaryOutput"},
			{Name: "total_lines", Type: "int"},
			{Name: "eff_total_lines", Type: "int"},
			{Name: "chunk_count", Type: "int"},
			{Name: "module_count", Type: "int"},
			{Name: "state_hash", Type: "string"},
			{Name: "rng_seed", Type: "int64"},
			{Name: "cached", Type: "bool"},
		},
		Cost:    dag.Cost{LatencyMS: 2000, Tokens: 0},
		Handler: NewScanBoundariesHandler(cfg),
	}
}

// NewScanBoundariesHandler returns the handler for
// sense.scan_project_boundaries.
//
// Inputs:
//   - project_root (string)        — required; absolute path
//   - window_lines (int)           — default bootstrap.DefaultWindowLines
//   - window_overlap (int)         — default bootstrap.DefaultWindowOverlap
//   - salt (string)                — optional, mixed into RNG seed
//
// Outputs:
//   - boundary_output (*bootstrap.BoundaryOutput)
//   - total_lines (int)            — raw line count, diagnostic
//   - eff_total_lines (int)        — primary coverage denominator
//   - chunk_count (int)            — len(BoundaryOutput.Chunks)
//   - module_count (int)           — len(BoundaryOutput.Modules)
//   - state_hash (string)          — sha256 of (rel:size:mtime) tuples
//   - rng_seed (int64)             — fnv64(state_hash + salt)
//   - cached (bool)                — false in v1 (cache deferred)
func NewScanBoundariesHandler(cfg ScanBoundariesConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		root := readString(in, "project_root")
		if root == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("sense.scan_project_boundaries: 'project_root' (string) is required")
		}
		windowLines := readInt(in, "window_lines", bootstrap.DefaultWindowLines)
		overlap := readInt(in, "window_overlap", bootstrap.DefaultWindowOverlap)
		salt := readString(in, "salt")

		var analyzer bootstrap.BoundaryAnalyzer
		if cfg.Analyzer != nil {
			analyzer = cfg.Analyzer
		} else {
			analyzer = bootstrap.UniversalAnalyzer{
				WindowLines:   windowLines,
				WindowOverlap: overlap,
				Salt:          salt,
			}
		}

		ignore := projectscan.LoadIgnoreSet(root)
		out, err := analyzer.Analyze(ctx, root, ignore)
		if err != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("sense.scan_project_boundaries: %w", err)
		}

		return dag.NodeResult{
			Out: map[string]any{
				"boundary_output": out,
				"total_lines":     out.TotalLines,
				"eff_total_lines": out.EffTotalLines,
				"chunk_count":     len(out.Chunks),
				"module_count":    len(out.Modules),
				"state_hash":      out.StateHash,
				"rng_seed":        out.RNGSeed,
				"cached":          false,
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
		}, nil
	}
}
