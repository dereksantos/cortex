package swebench

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

func TestSWEBench_LoadStrategyMultiplication(t *testing.T) {
	defer withFreshCache(t)()

	pages := map[int][]byte{
		0: syntheticPage(t, []string{"foo__a-1", "foo__a-2"}, "foo/a"),
	}
	rt := &recordingTransport{pages: pages}
	benchmarks.SetHTTPClient(&http.Client{Transport: rt})
	defer benchmarks.SetHTTPClient(nil)

	b := NewSWEBench()
	b.SetConfig(SWEBenchConfig{
		Model:      "test-model",
		Strategies: []string{"baseline", "cortex"},
	})

	out, err := b.Load(context.Background(), benchmarks.LoadOpts{Subset: "verified", Limit: 2})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 cells (2 instances × 2 strategies), got %d", len(out))
	}
	// IDs should suffix with the strategy.
	seen := map[string]bool{}
	for _, inst := range out {
		seen[inst.ID] = true
		if !strings.HasSuffix(inst.ID, "/baseline") && !strings.HasSuffix(inst.ID, "/cortex") {
			t.Errorf("id should suffix with strategy: %s", inst.ID)
		}
		p, ok := inst.Payload.(runnerPayload)
		if !ok {
			t.Errorf("payload type: %T", inst.Payload)
		}
		if p.Strategy != "baseline" && p.Strategy != "cortex" {
			t.Errorf("unexpected strategy: %s", p.Strategy)
		}
	}
	if !seen["foo__a-1/baseline"] || !seen["foo__a-1/cortex"] {
		t.Errorf("missing per-strategy variants: %v", seen)
	}
}

func TestSWEBench_RunRejectsMissingModel(t *testing.T) {
	b := NewSWEBench()
	// No model set.
	_, err := b.Run(context.Background(), benchmarks.Instance{
		ID:      "x/cortex",
		Payload: runnerPayload{Inst: Instance{InstanceID: "x"}, Strategy: "cortex"},
	}, benchmarks.Env{})
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("want model-required error, got %v", err)
	}
}

func TestSWEBench_RunRejectsBadPayload(t *testing.T) {
	b := NewSWEBench()
	b.SetConfig(SWEBenchConfig{Model: "test-model"})
	_, err := b.Run(context.Background(), benchmarks.Instance{
		ID:      "x/cortex",
		Payload: "not a runnerPayload",
	}, benchmarks.Env{})
	if err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("want payload-type error, got %v", err)
	}
}

func TestSWEBench_RegistryHasSwebench(t *testing.T) {
	got, err := benchmarks.Get("swebench")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "swebench" {
		t.Errorf("Name: got %q", got.Name())
	}
}
