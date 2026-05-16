package benchmarks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

// dummyBench is an in-test Benchmark used to exercise the registry and
// interface roundtrip without any network or LLM dependency.
type dummyBench struct {
	name      string
	instances []Instance
}

func (d *dummyBench) Name() string { return d.name }
func (d *dummyBench) Load(_ context.Context, opts LoadOpts) ([]Instance, error) {
	if opts.Limit > 0 && opts.Limit < len(d.instances) {
		return d.instances[:opts.Limit], nil
	}
	return d.instances, nil
}
func (d *dummyBench) Run(_ context.Context, inst Instance, _ Env) (*evalv2.CellResult, error) {
	return &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		RunID:                "run-" + inst.ID,
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		ScenarioID:           d.name + "/" + inst.ID,
		Benchmark:            d.name,
		Harness:              evalv2.HarnessCortex,
		Provider:             evalv2.ProviderOpenRouter,
		Model:                "test-model",
		ContextStrategy:      evalv2.StrategyCortex,
		CortexVersion:        "0.1.0",
		Temperature:          0.0,
		TaskSuccess:          true,
		TaskSuccessCriterion: evalv2.CriterionTestsPassAll,
	}, nil
}

func TestRegistry_RoundTrip(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	d := &dummyBench{
		name:      "dummy",
		instances: []Instance{{ID: "a"}, {ID: "b"}, {ID: "c"}},
	}
	Register("dummy", func() Benchmark { return d })

	got, err := Get("dummy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "dummy" {
		t.Errorf("Name=%q want %q", got.Name(), "dummy")
	}

	insts, err := got.Load(context.Background(), LoadOpts{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(insts) != 3 {
		t.Errorf("Load count=%d want 3", len(insts))
	}

	// Limit narrows correctly.
	insts2, err := got.Load(context.Background(), LoadOpts{Limit: 2})
	if err != nil {
		t.Fatalf("Load with limit: %v", err)
	}
	if len(insts2) != 2 {
		t.Errorf("Load(limit=2) count=%d want 2", len(insts2))
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	_, err := Get("nonexistent")
	if err == nil {
		t.Fatal("Get(nonexistent): want error, got nil")
	}
	if !errors.Is(err, ErrUnknownBenchmark) {
		t.Errorf("err=%v want errors.Is(ErrUnknownBenchmark)", err)
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("err=%v should mention queried name", err)
	}
}

func TestRegistry_UnknownListsRegistered(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	Register("foo", func() Benchmark { return &dummyBench{name: "foo"} })
	Register("bar", func() Benchmark { return &dummyBench{name: "bar"} })

	_, err := Get("baz")
	if err == nil {
		t.Fatal("Get(baz): want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bar") || !strings.Contains(msg, "foo") {
		t.Errorf("err=%v should list registered names; got %q", err, msg)
	}
}

func TestRegister_RejectsDuplicate(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	Register("dup", func() Benchmark { return &dummyBench{name: "dup"} })
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on duplicate registration, got none")
		}
	}()
	Register("dup", func() Benchmark { return &dummyBench{name: "dup"} })
}

func TestRegister_RejectsEmptyName(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on empty name, got none")
		}
	}()
	Register("", func() Benchmark { return &dummyBench{name: ""} })
}

func TestRegister_RejectsNilCtor(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on nil ctor, got none")
		}
	}()
	Register("nilctor", nil)
}

func TestRegistered_Sorted(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	resetRegistryForTest()

	Register("zeta", func() Benchmark { return &dummyBench{name: "zeta"} })
	Register("alpha", func() Benchmark { return &dummyBench{name: "alpha"} })
	Register("mike", func() Benchmark { return &dummyBench{name: "mike"} })

	got := Registered()
	want := []string{"alpha", "mike", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx=%d got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestDummyBench_ProducesValidCellResult(t *testing.T) {
	d := &dummyBench{name: "dummy", instances: []Instance{{ID: "a"}}}
	r, err := d.Run(context.Background(), Instance{ID: "a"}, Env{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("CellResult.Validate: %v", err)
	}
	if r.Benchmark != "dummy" {
		t.Errorf("Benchmark=%q want %q", r.Benchmark, "dummy")
	}
}
