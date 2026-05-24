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
