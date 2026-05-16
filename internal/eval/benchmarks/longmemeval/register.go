package longmemeval

import "github.com/dereksantos/cortex/internal/eval/benchmarks"

// init registers the benchmark. Import this package for side-effects
// (`_ "…/longmemeval"`) from cmd/cortex/commands to make `--benchmark
// longmemeval` available.
func init() {
	benchmarks.Register("longmemeval", func() benchmarks.Benchmark { return New() })
}
