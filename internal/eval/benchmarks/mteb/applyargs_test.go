package mteb

import (
	"os"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// TestApplyArgsDefaults — empty args is a no-op; loader defaults
// Subset to NFCorpus on its own.
func TestApplyArgsDefaults(t *testing.T) {
	os.Unsetenv("CORTEX_MTEB_RUNOPTS")
	b := &Benchmark{}
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	if err := b.ApplyArgs(nil, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Subset != "" {
		t.Errorf("Subset=%q, want empty (loader supplies NFCorpus default)", opts.Subset)
	}
	if v := os.Getenv("CORTEX_MTEB_RUNOPTS"); v != "" {
		t.Errorf("env CORTEX_MTEB_RUNOPTS=%q leaked", v)
	}
}

// TestApplyArgsTasks — --tasks sets opts.Subset, which the loader
// validates against the supported set.
func TestApplyArgsTasks(t *testing.T) {
	b := &Benchmark{}
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	if err := b.ApplyArgs([]string{"--tasks", "NFCorpus"}, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Subset != "NFCorpus" {
		t.Errorf("Subset=%q want NFCorpus", opts.Subset)
	}
}

// TestApplyArgsRerank — --rerank sets the sidechannel env var the
// runner reads at Run() time.
func TestApplyArgsRerank(t *testing.T) {
	os.Unsetenv("CORTEX_MTEB_RUNOPTS")
	t.Cleanup(func() { os.Unsetenv("CORTEX_MTEB_RUNOPTS") })

	b := &Benchmark{}
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	if err := b.ApplyArgs([]string{"--rerank"}, &opts); err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("CORTEX_MTEB_RUNOPTS"); v != "rerank" {
		t.Errorf("env=%q want rerank", v)
	}
}

// TestApplyArgsEmbedder — --embedder is captured in Filter, kept for
// the day internal/storage exposes a real per-instance switch.
func TestApplyArgsEmbedder(t *testing.T) {
	b := &Benchmark{}
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	if err := b.ApplyArgs([]string{"--embedder", "my-model"}, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Filter["embedder"] != "my-model" {
		t.Errorf("Filter[embedder]=%q want my-model", opts.Filter["embedder"])
	}
}

// TestApplyArgsRejectsModel — --model on the mteb benchmark is almost
// always operator error (they meant --embedder). Surface it loudly.
func TestApplyArgsRejectsModel(t *testing.T) {
	b := &Benchmark{}
	opts := benchmarks.LoadOpts{Filter: map[string]string{}}
	err := b.ApplyArgs([]string{"--model", "claude"}, &opts)
	if err == nil || !strings.Contains(err.Error(), "embedder") {
		t.Errorf("err=%v want guidance mentioning embedder", err)
	}
}

// TestApplyArgsMissingValueErrors — --tasks / --embedder without an
// argument should error, not silently swallow the next flag.
func TestApplyArgsMissingValueErrors(t *testing.T) {
	for _, flag := range []string{"--tasks", "--embedder"} {
		t.Run(flag, func(t *testing.T) {
			b := &Benchmark{}
			opts := benchmarks.LoadOpts{Filter: map[string]string{}}
			if err := b.ApplyArgs([]string{flag}, &opts); err == nil {
				t.Errorf("want error for %s with no value", flag)
			}
		})
	}
}
