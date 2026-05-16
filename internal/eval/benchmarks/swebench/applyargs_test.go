package swebench

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// TestSWEBenchImplementsArgsApplier is the compile-time contract: if
// SWEBench stops satisfying ArgsApplier (rename / refactor), the CLI
// dispatcher will silently skip flag parsing. Catch that here.
func TestSWEBenchImplementsArgsApplier(t *testing.T) {
	var b benchmarks.Benchmark = NewSWEBench()
	if _, ok := b.(benchmarks.ArgsApplier); !ok {
		t.Fatalf("SWEBench does not implement benchmarks.ArgsApplier — CLI flag wiring is broken")
	}
}

func TestApplyArgs_AllSWEBenchFlags(t *testing.T) {
	b := NewSWEBench()
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	err := b.ApplyArgs([]string{
		"--docker-image-prefix", "myregistry/sweb.eval.x86_64.",
		"--git-cache-dir", "/tmp/cortex-git-cache",
		"--repo", "django/django",
		"--repo", "psf/requests",
	}, &opts)
	if err != nil {
		t.Fatalf("ApplyArgs: %v", err)
	}
	if opts.Filter["docker-image-prefix"] != "myregistry/sweb.eval.x86_64." {
		t.Errorf("prefix: %q", opts.Filter["docker-image-prefix"])
	}
	if opts.Filter["git-cache-dir"] != "/tmp/cortex-git-cache" {
		t.Errorf("cache: %q", opts.Filter["git-cache-dir"])
	}
	if opts.Filter["repo"] != "django/django,psf/requests" {
		t.Errorf("repo: %q (want comma-joined)", opts.Filter["repo"])
	}
}

func TestApplyArgs_TolerateSharedFlags(t *testing.T) {
	// --subset / --limit / --strategy / --model are owned by
	// parseBenchmarkArgs; ApplyArgs must silently skip them.
	b := NewSWEBench()
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	if err := b.ApplyArgs([]string{"--subset", "verified", "--limit", "5", "--model", "x", "--strategy", "baseline,cortex"}, &opts); err != nil {
		t.Fatalf("ApplyArgs should tolerate shared flags: %v", err)
	}
	if len(opts.Filter) != 0 {
		t.Errorf("shared flags should not land in Filter via ApplyArgs: %v", opts.Filter)
	}
}

func TestApplyArgs_MissingValueErrors(t *testing.T) {
	b := NewSWEBench()
	cases := [][]string{
		{"--repo"},
		{"--docker-image-prefix"},
		{"--git-cache-dir"},
	}
	for _, args := range cases {
		opts := benchmarks.LoadOpts{Filter: map[string]string{}}
		err := b.ApplyArgs(args, &opts)
		if err == nil || !strings.Contains(err.Error(), "requires a value") {
			t.Errorf("%v: want 'requires a value' error, got %v", args, err)
		}
	}
}
