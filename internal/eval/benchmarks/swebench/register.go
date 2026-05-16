package swebench

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

func init() {
	benchmarks.Register("swebench", func() benchmarks.Benchmark { return NewSWEBench() })
}

// SWEBenchConfig holds the per-run configuration the dispatcher fills
// in from CLI flags. Defaults are conservative: scoring uses the
// canonical Docker image prefix, no git cache, and the canonical
// 30-minute per-instance timeout from the SWE-bench reference harness.
type SWEBenchConfig struct {
	Model             string
	Strategies        []string // ordered; "baseline", "cortex", or both
	DockerImagePrefix string
	GitCacheDir       string
	InstanceTimeout   time.Duration
}

// SWEBench implements benchmarks.Benchmark. Configured via the setters
// before Load is called; Load multiplies instances by len(Strategies)
// so each (instance, strategy) pair maps to one Run call.
type SWEBench struct {
	cfg SWEBenchConfig
}

// NewSWEBench returns a benchmark with sensible defaults: cortex
// strategy only, canonical docker prefix, 30-minute timeout, model
// unset (the dispatcher must call SetConfig before Load).
func NewSWEBench() *SWEBench {
	return &SWEBench{
		cfg: SWEBenchConfig{
			Strategies:        []string{"cortex"},
			DockerImagePrefix: "swebench/sweb.eval.x86_64.",
			InstanceTimeout:   30 * time.Minute,
		},
	}
}

// Name implements benchmarks.Benchmark.
func (b *SWEBench) Name() string { return "swebench" }

// SetConfig replaces the per-run config wholesale. Called by the CLI
// dispatcher after flag parsing.
func (b *SWEBench) SetConfig(cfg SWEBenchConfig) {
	if cfg.DockerImagePrefix == "" {
		cfg.DockerImagePrefix = b.cfg.DockerImagePrefix
	}
	if cfg.InstanceTimeout == 0 {
		cfg.InstanceTimeout = b.cfg.InstanceTimeout
	}
	if len(cfg.Strategies) == 0 {
		cfg.Strategies = b.cfg.Strategies
	}
	b.cfg = cfg
}

// Config returns the current configuration. Useful for tests.
func (b *SWEBench) Config() SWEBenchConfig { return b.cfg }

// Load implements benchmarks.Benchmark.
//
// Reads benchmark-specific flags out of opts.Filter (model, strategy,
// docker-image-prefix, git-cache-dir) and applies them onto b.cfg.
// Defaults --limit to 10 if unset — the full 500-row split is a
// nightly target, not an interactive one.
//
// The returned slice has one entry per (instance, strategy) pair so
// the dispatcher's outer loop persists one CellResult per row without
// the benchmark itself touching the Persister.
func (b *SWEBench) Load(ctx context.Context, opts benchmarks.LoadOpts) ([]benchmarks.Instance, error) {
	if opts.Subset == "" {
		opts.Subset = "verified"
	}
	if opts.Limit == 0 {
		opts.Limit = 10
	}
	b.applyFilterConfig(opts.Filter)

	raw, err := LoadInstances(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(b.cfg.Strategies) == 0 {
		return nil, fmt.Errorf("swebench: no strategies configured")
	}

	out := make([]benchmarks.Instance, 0, len(raw)*len(b.cfg.Strategies))
	for _, inst := range raw {
		for _, strat := range b.cfg.Strategies {
			out = append(out, benchmarks.Instance{
				ID:      inst.InstanceID + "/" + strat,
				Payload: runnerPayload{Inst: inst, Strategy: strat},
			})
		}
	}
	return out, nil
}

// applyFilterConfig merges per-benchmark flags from opts.Filter into
// b.cfg. Existing fields take precedence only when the Filter value
// is empty — explicit flags override prior SetConfig calls.
func (b *SWEBench) applyFilterConfig(f map[string]string) {
	if f == nil {
		return
	}
	if v := f["model"]; v != "" {
		b.cfg.Model = v
	}
	if v := f["strategy"]; v != "" {
		b.cfg.Strategies = splitCSV(v)
	}
	if v := f["docker-image-prefix"]; v != "" {
		b.cfg.DockerImagePrefix = v
	}
	if v := f["git-cache-dir"]; v != "" {
		b.cfg.GitCacheDir = v
	}
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Run implements benchmarks.Benchmark.
func (b *SWEBench) Run(ctx context.Context, inst benchmarks.Instance, env benchmarks.Env) (*evalv2.CellResult, error) {
	p, ok := inst.Payload.(runnerPayload)
	if !ok {
		return nil, fmt.Errorf("swebench: unexpected payload type %T", inst.Payload)
	}
	if strings.TrimSpace(b.cfg.Model) == "" {
		return nil, fmt.Errorf("swebench: --model required")
	}
	return runInstance(ctx, p, b.cfg, benchInfo{
		Workdir: env.Workdir,
		Verbose: env.Verbose,
	})
}
