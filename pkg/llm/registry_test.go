package llm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProbe is a test stub that returns canned models and lets tests
// observe call counts.
type fakeProbe struct {
	name   string
	models []ModelInfo
	err    error
	calls  atomic.Int32
}

func (p *fakeProbe) Name() string { return p.name }

func (p *fakeProbe) Probe(_ context.Context) ([]ModelInfo, error) {
	p.calls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out, nil
}

func TestModelInfo_HasCapability(t *testing.T) {
	m := ModelInfo{Capabilities: []string{CapCoding, CapToolCalling}}
	if !m.HasCapability(CapCoding) {
		t.Errorf("expected CapCoding to be present")
	}
	if !m.HasCapability(CapToolCalling) {
		t.Errorf("expected CapToolCalling to be present")
	}
	if m.HasCapability(CapEmbedding) {
		t.Errorf("expected CapEmbedding to be absent")
	}
}

func TestCompositeRegistry_List_DedupesByProbeOrder(t *testing.T) {
	// Two probes both expose "shared-model" but with different metadata.
	// The first probe in cfg.Probes wins on collision.
	local := &fakeProbe{
		name: "local",
		models: []ModelInfo{
			{ID: "shared-model", Endpoint: "local", EffectiveContextWindow: 8192},
			{ID: "local-only", Endpoint: "local"},
		},
	}
	cloud := &fakeProbe{
		name: "cloud",
		models: []ModelInfo{
			{ID: "shared-model", Endpoint: "cloud", EffectiveContextWindow: 131072},
			{ID: "cloud-only", Endpoint: "cloud"},
		},
	}
	reg := NewCompositeRegistry(RegistryConfig{
		Probes: []Probe{local, cloud},
		TTL:    time.Minute,
	})
	models := reg.List(context.Background())
	if got, want := len(models), 3; got != want {
		t.Fatalf("expected %d unique models after dedupe, got %d: %+v", want, got, models)
	}
	shared, ok := reg.Get(context.Background(), "shared-model")
	if !ok {
		t.Fatal("shared-model not found")
	}
	if shared.Endpoint != "local" {
		t.Errorf("expected first-probe win (Endpoint=local), got %s", shared.Endpoint)
	}
}

func TestCompositeRegistry_Filter(t *testing.T) {
	probe := &fakeProbe{
		name: "p1",
		models: []ModelInfo{
			{ID: "chat-only", Capabilities: []string{CapCoding}},
			{ID: "embed-only", Capabilities: []string{CapEmbedding}},
			{ID: "both", Capabilities: []string{CapCoding, CapEmbedding}},
		},
	}
	reg := NewCompositeRegistry(RegistryConfig{Probes: []Probe{probe}})
	chat := reg.Filter(context.Background(), func(m ModelInfo) bool {
		return m.HasCapability(CapCoding) && !m.HasCapability(CapEmbedding)
	})
	if got, want := len(chat), 1; got != want {
		t.Errorf("filter count: got %d want %d", got, want)
	}
	if len(chat) > 0 && chat[0].ID != "chat-only" {
		t.Errorf("unexpected filter result: %+v", chat)
	}
}

func TestCompositeRegistry_ProbeErrorDoesNotBlankRegistry(t *testing.T) {
	good := &fakeProbe{
		name: "good",
		models: []ModelInfo{
			{ID: "alive", Endpoint: "good"},
		},
	}
	dead := &fakeProbe{name: "dead", err: errors.New("probe down")}
	var reportedErr error
	reg := NewCompositeRegistry(RegistryConfig{
		Probes:  []Probe{good, dead},
		OnError: func(_ string, err error) { reportedErr = err },
	})
	models := reg.List(context.Background())
	if got, want := len(models), 1; got != want {
		t.Errorf("expected %d models from surviving probe, got %d", want, got)
	}
	if reportedErr == nil {
		t.Errorf("expected OnError to be invoked on dead probe")
	}
}

func TestCompositeRegistry_CacheHitsAvoidReprobe(t *testing.T) {
	probe := &fakeProbe{name: "p1", models: []ModelInfo{{ID: "m1"}}}
	reg := NewCompositeRegistry(RegistryConfig{
		Probes: []Probe{probe},
		TTL:    time.Minute,
	})
	_ = reg.List(context.Background())
	_ = reg.List(context.Background())
	_, _ = reg.Get(context.Background(), "m1")
	if got := probe.calls.Load(); got != 1 {
		t.Errorf("expected 1 probe call (cached), got %d", got)
	}
}

func TestCompositeRegistry_RefreshForcesReprobe(t *testing.T) {
	probe := &fakeProbe{name: "p1", models: []ModelInfo{{ID: "m1"}}}
	reg := NewCompositeRegistry(RegistryConfig{
		Probes: []Probe{probe},
		TTL:    time.Hour,
	})
	_ = reg.List(context.Background())
	if err := reg.Refresh(context.Background()); err != nil {
		t.Errorf("Refresh returned error: %v", err)
	}
	if got := probe.calls.Load(); got != 2 {
		t.Errorf("expected 2 probe calls (initial + refresh), got %d", got)
	}
}

// pickRegistry is a small builder for PickForCapabilities tests: one
// fakeProbe holding a known model set, no TTL pressure, deterministic
// dedupe order. Returns a registry whose cache is the given models.
func pickRegistry(t *testing.T, models []ModelInfo) ModelRegistry {
	t.Helper()
	probe := &fakeProbe{name: "test", models: models}
	reg := NewCompositeRegistry(RegistryConfig{
		Probes: []Probe{probe},
		TTL:    time.Hour,
	})
	// Warm the cache so PickForCapabilities operates on the canned set.
	_ = reg.List(context.Background())
	return reg
}

func TestPickForCapabilities_EmptyRequiresReturnsFalse(t *testing.T) {
	reg := pickRegistry(t, []ModelInfo{
		{ID: "any-model", Capabilities: []string{CapToolCalling}, IsLocal: true},
	})
	got, ok := reg.PickForCapabilities(context.Background(), nil)
	if ok {
		t.Errorf("empty requires should return false, got %+v", got)
	}
}

func TestPickForCapabilities_NoMatchReturnsFalse(t *testing.T) {
	reg := pickRegistry(t, []ModelInfo{
		{ID: "embedder", Capabilities: []string{CapEmbedding}, IsLocal: true},
	})
	got, ok := reg.PickForCapabilities(context.Background(), []string{CapCoding, CapToolCalling})
	if ok {
		t.Errorf("no matching cap should return false, got %+v", got)
	}
}

func TestPickForCapabilities_ChainFallsThroughToBase(t *testing.T) {
	// Specialist not present, base is. Picker should fall through the
	// chain and return the base candidate.
	reg := pickRegistry(t, []ModelInfo{
		{ID: "qwen-coder-30b", Capabilities: []string{CapCoding, CapToolCalling}, IsLocal: true, SizeBillion: 30},
	})
	got, ok := reg.PickForCapabilities(
		context.Background(),
		[]string{CapToolCallingSpecialist, CapToolCalling},
	)
	if !ok {
		t.Fatal("expected fallback to CapToolCalling, got no match")
	}
	if got.ID != "qwen-coder-30b" {
		t.Errorf("expected qwen-coder-30b on fallback, got %s", got.ID)
	}
}

func TestPickForCapabilities_SpecialistPrefersSmaller(t *testing.T) {
	// Two specialists: 1.5B and 7B. Picker should prefer the smaller
	// (specialists are valued for speed + reliability per task, not
	// size).
	reg := pickRegistry(t, []ModelInfo{
		{ID: "xlam-7b", Capabilities: []string{CapToolCalling, CapToolCallingSpecialist}, IsLocal: true, SizeBillion: 7},
		{ID: "xlam-1.5b", Capabilities: []string{CapToolCalling, CapToolCallingSpecialist}, IsLocal: true, SizeBillion: 1.5},
	})
	got, ok := reg.PickForCapabilities(context.Background(), []string{CapToolCallingSpecialist})
	if !ok {
		t.Fatal("expected a specialist match")
	}
	if got.ID != "xlam-1.5b" {
		t.Errorf("specialist should prefer smaller, got %s", got.ID)
	}
}

func TestPickForCapabilities_GeneralistPrefersLarger(t *testing.T) {
	// Two non-specialist tool-callers. Picker should prefer the larger
	// (more capable model when no specialty signal narrows the choice).
	reg := pickRegistry(t, []ModelInfo{
		{ID: "llama3-8b", Capabilities: []string{CapToolCalling}, IsLocal: true, SizeBillion: 8},
		{ID: "llama3-70b", Capabilities: []string{CapToolCalling}, IsLocal: true, SizeBillion: 70},
	})
	got, ok := reg.PickForCapabilities(context.Background(), []string{CapToolCalling})
	if !ok {
		t.Fatal("expected a match")
	}
	if got.ID != "llama3-70b" {
		t.Errorf("generalist should prefer larger, got %s", got.ID)
	}
}

func TestPickForCapabilities_LocalBeatsCloud(t *testing.T) {
	// Local + small specialist vs cloud + large specialist. Local wins
	// regardless of the smaller-preferring tiebreaker — locality is the
	// outer sort key.
	reg := pickRegistry(t, []ModelInfo{
		{ID: "cloud-xlam-1.5b", Capabilities: []string{CapToolCalling, CapToolCallingSpecialist}, IsLocal: false, SizeBillion: 1.5},
		{ID: "local-xlam-7b", Capabilities: []string{CapToolCalling, CapToolCallingSpecialist}, IsLocal: true, SizeBillion: 7},
	})
	got, ok := reg.PickForCapabilities(context.Background(), []string{CapToolCallingSpecialist})
	if !ok {
		t.Fatal("expected a specialist match")
	}
	if got.ID != "local-xlam-7b" {
		t.Errorf("local should beat cloud even when cloud is smaller, got %s", got.ID)
	}
}

func TestPickForCapabilities_ChainStopsAtFirstMatch(t *testing.T) {
	// Both specialist and base candidates available. Chain visits
	// specialist first; picker must return that and not continue to
	// the base.
	reg := pickRegistry(t, []ModelInfo{
		{ID: "xlam-1.5b", Capabilities: []string{CapToolCalling, CapToolCallingSpecialist}, IsLocal: true, SizeBillion: 1.5},
		{ID: "llama3-70b", Capabilities: []string{CapToolCalling}, IsLocal: true, SizeBillion: 70},
	})
	got, ok := reg.PickForCapabilities(
		context.Background(),
		[]string{CapToolCallingSpecialist, CapToolCalling},
	)
	if !ok {
		t.Fatal("expected a match")
	}
	if got.ID != "xlam-1.5b" {
		t.Errorf("chain should stop at first match (specialist), got %s", got.ID)
	}
}

func TestPickForCapabilities_DeterministicByIdOnTies(t *testing.T) {
	// Same locality, same size — picker must fall through to a
	// deterministic id sort so traces stay reproducible across runs.
	reg := pickRegistry(t, []ModelInfo{
		{ID: "model-b", Capabilities: []string{CapToolCalling}, IsLocal: true, SizeBillion: 7},
		{ID: "model-a", Capabilities: []string{CapToolCalling}, IsLocal: true, SizeBillion: 7},
		{ID: "model-c", Capabilities: []string{CapToolCalling}, IsLocal: true, SizeBillion: 7},
	})
	got, ok := reg.PickForCapabilities(context.Background(), []string{CapToolCalling})
	if !ok {
		t.Fatal("expected a match")
	}
	if got.ID != "model-a" {
		t.Errorf("tie should sort by id ascending, got %s", got.ID)
	}
}

func TestPickForCapabilities_FallsBackToContextWindowSize(t *testing.T) {
	// When SizeBillion is missing, EffectiveContextWindow is the
	// secondary size signal. Two unsized models, one with a larger
	// context — the generalist preference picks the larger window.
	reg := pickRegistry(t, []ModelInfo{
		{ID: "unsized-small-ctx", Capabilities: []string{CapToolCalling}, IsLocal: true, EffectiveContextWindow: 8192},
		{ID: "unsized-large-ctx", Capabilities: []string{CapToolCalling}, IsLocal: true, EffectiveContextWindow: 131072},
	})
	got, ok := reg.PickForCapabilities(context.Background(), []string{CapToolCalling})
	if !ok {
		t.Fatal("expected a match")
	}
	if got.ID != "unsized-large-ctx" {
		t.Errorf("generalist should prefer larger ctx when sizes unknown, got %s", got.ID)
	}
}

func TestTrimChatSuffix(t *testing.T) {
	tests := map[string]string{
		"http://localhost:11434/v1/chat/completions": "http://localhost:11434",
		"http://localhost:11434":                     "http://localhost:11434",
		"http://example.com/v1/chat/completions":     "http://example.com",
		"":                                           "",
	}
	for input, want := range tests {
		if got := trimChatSuffix(input); got != want {
			t.Errorf("trimChatSuffix(%q): got %q want %q", input, got, want)
		}
	}
}
