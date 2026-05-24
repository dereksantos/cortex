// Package dag — DefaultRouter resolution-order tests.
//
// Per docs/per-node-routing-plan.md "Resolution at spawn time":
//   1. NodeSpec.Attrs["model"] explicit override wins.
//   2. NodeSpec.Requires chain → registry.PickForCapabilities → factory.Get.
//   3. Fallback to session-default provider.
//   4. nil + "no-match" when no path produced a provider.
//
// Tests use small inline fakes for ModelRegistry + ProviderFactory so
// the resolution logic is exercised without standing up real probes.

package dag

import (
	"context"
	"errors"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeRegistry returns a canned (ModelInfo, ok) for PickForCapabilities
// and records the requires chain it was called with. Other ModelRegistry
// methods are no-ops — DefaultRouter only consults PickForCapabilities.
type fakeRegistry struct {
	pick     llm.ModelInfo
	ok       bool
	gotCaps  []string
	gotCalls int
}

func (f *fakeRegistry) List(context.Context) []llm.ModelInfo { return nil }
func (f *fakeRegistry) Get(context.Context, string) (llm.ModelInfo, bool) {
	return llm.ModelInfo{}, false
}
func (f *fakeRegistry) Filter(context.Context, func(llm.ModelInfo) bool) []llm.ModelInfo {
	return nil
}
func (f *fakeRegistry) Refresh(context.Context) error { return nil }
func (f *fakeRegistry) PickForCapabilities(_ context.Context, requires []string) (llm.ModelInfo, bool) {
	f.gotCaps = requires
	f.gotCalls++
	return f.pick, f.ok
}

// fakeFactory returns canned providers per model id. Returns an error
// for ids in errFor — tests use this to drive override-failure
// fallthrough.
type fakeFactory struct {
	byID    map[string]llm.Provider
	errFor  map[string]error
	def     llm.Provider
	gotGets []string
}

func (f *fakeFactory) Get(modelID string) (llm.Provider, error) {
	f.gotGets = append(f.gotGets, modelID)
	if err, ok := f.errFor[modelID]; ok {
		return nil, err
	}
	if p, ok := f.byID[modelID]; ok {
		return p, nil
	}
	return nil, errors.New("no provider for " + modelID)
}
func (f *fakeFactory) Default() llm.Provider { return f.def }

func TestDefaultRouter_OverrideWins(t *testing.T) {
	overrideP := llm.NewMockProvider(0)
	defaultP := llm.NewMockProvider(0)
	reg := &fakeRegistry{pick: llm.ModelInfo{ID: "from-requires"}, ok: true}
	fac := &fakeFactory{byID: map[string]llm.Provider{"explicit-model": overrideP}}

	r := NewDefaultRouter(RouterDeps{Registry: reg, ProviderFactory: fac, Default: defaultP})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCallingSpecialist},
		Attrs:    map[string]any{"model": "explicit-model"},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != overrideP {
		t.Errorf("override should win, got %v", got)
	}
	if id != "explicit-model" || reason != "override" {
		t.Errorf("trace fields: got id=%q reason=%q, want explicit-model/override", id, reason)
	}
	if reg.gotCalls != 0 {
		t.Errorf("registry should not be consulted when override wins, got %d calls", reg.gotCalls)
	}
}

func TestDefaultRouter_RequiresChainResolves(t *testing.T) {
	pickedP := llm.NewMockProvider(0)
	defaultP := llm.NewMockProvider(0)
	reg := &fakeRegistry{pick: llm.ModelInfo{ID: "xlam-1.5b"}, ok: true}
	fac := &fakeFactory{byID: map[string]llm.Provider{"xlam-1.5b": pickedP}}

	r := NewDefaultRouter(RouterDeps{Registry: reg, ProviderFactory: fac, Default: defaultP})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCallingSpecialist, llm.CapToolCalling},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != pickedP {
		t.Error("expected provider for picked model id")
	}
	if id != "xlam-1.5b" || reason != "requires:xlam-1.5b" {
		t.Errorf("trace fields: got id=%q reason=%q, want xlam-1.5b/requires:xlam-1.5b", id, reason)
	}
	// Registry must see the full chain, not just the first cap — the
	// picker walks it internally.
	if got, want := len(reg.gotCaps), 2; got != want {
		t.Errorf("registry should see full chain (%d caps), got %d: %v", want, got, reg.gotCaps)
	}
}

func TestDefaultRouter_FallsBackToDefaultWhenChainEmpty(t *testing.T) {
	defaultP := llm.NewMockProvider(0)
	reg := &fakeRegistry{} // ok=false; would return no match if called
	fac := &fakeFactory{}

	r := NewDefaultRouter(RouterDeps{Registry: reg, ProviderFactory: fac, Default: defaultP})
	spec := NodeSpec{Function: FuncAttend, Op: "compress"} // empty Requires
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != defaultP {
		t.Errorf("empty Requires should fall back to default, got %v", got)
	}
	if id != "" || reason != "default" {
		t.Errorf("trace fields: got id=%q reason=%q, want \"\"/default", id, reason)
	}
	if reg.gotCalls != 0 {
		t.Errorf("registry should not be consulted when Requires is empty, got %d calls", reg.gotCalls)
	}
}

func TestDefaultRouter_FallsBackToDefaultOnNoMatch(t *testing.T) {
	defaultP := llm.NewMockProvider(0)
	reg := &fakeRegistry{ok: false} // chain exhausts
	fac := &fakeFactory{}

	r := NewDefaultRouter(RouterDeps{Registry: reg, ProviderFactory: fac, Default: defaultP})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCallingSpecialist},
	}
	got, _, reason := r.Resolve(context.Background(), spec)
	if got != defaultP {
		t.Errorf("no-match should fall back to default, got %v", got)
	}
	if reason != "default" {
		t.Errorf("reason: got %q, want default", reason)
	}
}

func TestDefaultRouter_OverrideErrorFallsThrough(t *testing.T) {
	// A stale Attrs["model"] referencing a missing model must NOT
	// block the spawn — fall through to Requires (or default) so the
	// harness stays usable when a saved override drifts out of date.
	pickedP := llm.NewMockProvider(0)
	defaultP := llm.NewMockProvider(0)
	reg := &fakeRegistry{pick: llm.ModelInfo{ID: "xlam-1.5b"}, ok: true}
	fac := &fakeFactory{
		byID:   map[string]llm.Provider{"xlam-1.5b": pickedP},
		errFor: map[string]error{"missing-model": errors.New("no such model")},
	}

	r := NewDefaultRouter(RouterDeps{Registry: reg, ProviderFactory: fac, Default: defaultP})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCallingSpecialist},
		Attrs:    map[string]any{"model": "missing-model"},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != pickedP {
		t.Errorf("stale override should fall through to Requires, got %v", got)
	}
	if id != "xlam-1.5b" || reason != "requires:xlam-1.5b" {
		t.Errorf("trace fields: got id=%q reason=%q, want xlam-1.5b/requires:xlam-1.5b", id, reason)
	}
}

// TestDefaultRouter_ConfigPinBeatsRequires — slice 9 of
// docs/per-node-routing-plan.md. RoutingByQname["decide.tool_call"]
// pins a specific model; the Router must use it instead of walking
// the Requires chain. The operator's whole reason for setting the
// pin is "the auto-pick is wrong; ignore it."
func TestDefaultRouter_ConfigPinBeatsRequires(t *testing.T) {
	pinnedP := llm.NewMockProvider(0)
	requiresP := llm.NewMockProvider(0)
	defaultP := llm.NewMockProvider(0)
	reg := &fakeRegistry{pick: llm.ModelInfo{ID: "auto-picked"}, ok: true}
	fac := &fakeFactory{byID: map[string]llm.Provider{
		"qwen3-1.7b-FLM": pinnedP,
		"auto-picked":    requiresP,
	}}

	r := NewDefaultRouter(RouterDeps{
		Registry:        reg,
		ProviderFactory: fac,
		Default:         defaultP,
		RoutingByQname:  map[string]string{"decide.tool_call": "qwen3-1.7b-FLM"},
	})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCallingSpecialist},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != pinnedP {
		t.Errorf("config pin should win over Requires, got %v", got)
	}
	if id != "qwen3-1.7b-FLM" || reason != "config:qwen3-1.7b-FLM" {
		t.Errorf("trace fields: got id=%q reason=%q, want qwen3-1.7b-FLM/config:qwen3-1.7b-FLM", id, reason)
	}
	if reg.gotCalls != 0 {
		t.Errorf("registry must NOT be consulted when config pin resolves, got %d calls", reg.gotCalls)
	}
}

// TestDefaultRouter_AttrsModelStillBeatsConfigPin — per-spawn override
// is rare and deliberate (LLM-emitted, test-pinned). It must trump
// the operator's config pin so a one-off can ride over the broad
// configuration. This is the inverse hierarchy: spawn > config >
// auto-pick > default.
func TestDefaultRouter_AttrsModelStillBeatsConfigPin(t *testing.T) {
	overrideP := llm.NewMockProvider(0)
	pinnedP := llm.NewMockProvider(0)
	fac := &fakeFactory{byID: map[string]llm.Provider{
		"per-spawn-model": overrideP,
		"config-model":    pinnedP,
	}}
	r := NewDefaultRouter(RouterDeps{
		ProviderFactory: fac,
		RoutingByQname:  map[string]string{"decide.tool_call": "config-model"},
	})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Attrs: map[string]any{"model": "per-spawn-model"},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != overrideP {
		t.Errorf("attrs.model should beat config pin, got %v", got)
	}
	if id != "per-spawn-model" || reason != "override" {
		t.Errorf("trace fields: got id=%q reason=%q, want per-spawn-model/override", id, reason)
	}
}

// TestDefaultRouter_StaleConfigPinFallsThroughToRequires — when the
// pinned model id doesn't resolve (typo, uninstalled), the Router
// must NOT block the spawn. Falls through to the Requires chain so
// the harness stays usable. Operator sees `model=requires:<id>` in
// the trace and knows their pin didn't bind.
func TestDefaultRouter_StaleConfigPinFallsThroughToRequires(t *testing.T) {
	reqP := llm.NewMockProvider(0)
	reg := &fakeRegistry{pick: llm.ModelInfo{ID: "from-requires"}, ok: true}
	fac := &fakeFactory{
		byID:   map[string]llm.Provider{"from-requires": reqP},
		errFor: map[string]error{"missing-pinned": errors.New("no such model")},
	}
	r := NewDefaultRouter(RouterDeps{
		Registry:        reg,
		ProviderFactory: fac,
		RoutingByQname:  map[string]string{"decide.tool_call": "missing-pinned"},
	})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCalling},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != reqP {
		t.Errorf("stale config pin should fall through to Requires, got %v", got)
	}
	if id != "from-requires" || reason != "requires:from-requires" {
		t.Errorf("trace fields: got id=%q reason=%q", id, reason)
	}
}

func TestDefaultRouter_NoProviderNoMatch(t *testing.T) {
	// No router deps configured beyond the empty struct — Resolve
	// returns nil + "no-match" so callers (handlers) know to keep
	// using their own cfg.Provider.
	r := NewDefaultRouter(RouterDeps{})
	spec := NodeSpec{
		Function: FuncDecide, Op: "tool_call",
		Requires: []string{llm.CapToolCallingSpecialist},
	}
	got, id, reason := r.Resolve(context.Background(), spec)
	if got != nil {
		t.Errorf("expected nil provider when no deps configured, got %v", got)
	}
	if id != "" || reason != "no-match" {
		t.Errorf("trace fields: got id=%q reason=%q, want \"\"/no-match", id, reason)
	}
}
