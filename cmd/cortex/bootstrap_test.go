package main

import (
	"errors"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeConfigProbe is a scripted ConfigProbe. calls counts invocations
// so tests can assert the config-hit row short-circuits the rest of
// the chain (later stages never probed).
type fakeConfigProbe struct {
	result ResolvedBackend
	ok     bool
	calls  int
}

func (f *fakeConfigProbe) Probe() (ResolvedBackend, bool) {
	f.calls++
	return f.result, f.ok
}

type fakeKeyProbe struct {
	source string
	ok     bool
	calls  int
}

func (f *fakeKeyProbe) Probe() (string, bool) {
	f.calls++
	return f.source, f.ok
}

type fakeOllamaProbe struct {
	up    bool
	calls int
}

func (f *fakeOllamaProbe) Probe() bool {
	f.calls++
	return f.up
}

type fakeGuidedSetup struct {
	ok    bool
	calls int
}

func (f *fakeGuidedSetup) Guide() bool {
	f.calls++
	return f.ok
}

// fakeSmokeProbe fails the smoke test for any ResolvedBackend.Source
// listed in fail, and passes every other source. An empty fail set
// passes everything.
type fakeSmokeProbe struct {
	fail map[string]bool
}

func (f *fakeSmokeProbe) Smoke(b ResolvedBackend) bool {
	return !f.fail[b.Source]
}

func TestBackendResolverChain(t *testing.T) {
	tests := []struct {
		name       string
		config     fakeConfigProbe
		key        fakeKeyProbe
		ollama     fakeOllamaProbe
		guided     fakeGuidedSetup
		failSmoke  []string
		wantSource string
		wantErr    bool
		// checkShortCircuit, if set, asserts the key/ollama/guided
		// probes were never invoked (config-hit short-circuits).
		checkShortCircuit bool
	}{
		{
			name: "config-hit short-circuits",
			config: fakeConfigProbe{
				ok:     true,
				result: ResolvedBackend{Source: "config", Backend: llm.BackendOpenRouter, Model: "anthropic/claude-haiku-4.5"},
			},
			key:               fakeKeyProbe{source: "openrouter-env", ok: true},
			ollama:            fakeOllamaProbe{up: true},
			guided:            fakeGuidedSetup{ok: true},
			wantSource:        "config",
			checkShortCircuit: true,
		},
		{
			name:       "config-miss + env key",
			config:     fakeConfigProbe{ok: false},
			key:        fakeKeyProbe{source: "openrouter-env", ok: true},
			wantSource: "openrouter-env",
		},
		{
			name:       "keychain hit",
			config:     fakeConfigProbe{ok: false},
			key:        fakeKeyProbe{source: "openrouter-keychain", ok: true},
			wantSource: "openrouter-keychain",
		},
		{
			name:       "all keys miss + ollama up",
			config:     fakeConfigProbe{ok: false},
			key:        fakeKeyProbe{ok: false},
			ollama:     fakeOllamaProbe{up: true},
			wantSource: "ollama-local",
		},
		{
			name:       "all down => guided openrouter/free",
			config:     fakeConfigProbe{ok: false},
			key:        fakeKeyProbe{ok: false},
			ollama:     fakeOllamaProbe{up: false},
			guided:     fakeGuidedSetup{ok: true},
			wantSource: "openrouter-guided",
		},
		{
			name: "smoke failure at config falls through to key",
			config: fakeConfigProbe{
				ok:     true,
				result: ResolvedBackend{Source: "config", Backend: llm.BackendOpenRouter},
			},
			key:        fakeKeyProbe{source: "openrouter-env", ok: true},
			failSmoke:  []string{"config"},
			wantSource: "openrouter-env",
		},
		{
			name:       "smoke failure at key falls through to ollama",
			config:     fakeConfigProbe{ok: false},
			key:        fakeKeyProbe{source: "openrouter-env", ok: true},
			ollama:     fakeOllamaProbe{up: true},
			failSmoke:  []string{"openrouter-env"},
			wantSource: "ollama-local",
		},
		{
			name:       "smoke failure at ollama falls through to guided",
			config:     fakeConfigProbe{ok: false},
			key:        fakeKeyProbe{ok: false},
			ollama:     fakeOllamaProbe{up: true},
			guided:     fakeGuidedSetup{ok: true},
			failSmoke:  []string{"ollama-local"},
			wantSource: "openrouter-guided",
		},
		{
			name: "smoke failure at every seated stage exhausts the chain",
			config: fakeConfigProbe{
				ok:     true,
				result: ResolvedBackend{Source: "config", Backend: llm.BackendOpenRouter},
			},
			key:       fakeKeyProbe{source: "openrouter-env", ok: true},
			ollama:    fakeOllamaProbe{up: true},
			guided:    fakeGuidedSetup{ok: true},
			failSmoke: []string{"config", "openrouter-env", "ollama-local", "openrouter-guided"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fail := map[string]bool{}
			for _, s := range tt.failSmoke {
				fail[s] = true
			}
			r := &BackendResolver{
				Config: &tt.config,
				Key:    &tt.key,
				Ollama: &tt.ollama,
				Guided: &tt.guided,
				Smoke:  &fakeSmokeProbe{fail: fail},
			}

			got, err := r.Resolve()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve() = %+v, want error", got)
				}
				if !errors.Is(err, ErrNoBackend) {
					t.Fatalf("Resolve() error = %v, want ErrNoBackend", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() unexpected error: %v", err)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Resolve() source = %q, want %q", got.Source, tt.wantSource)
			}
			if tt.checkShortCircuit {
				if tt.key.calls != 0 {
					t.Errorf("key probe called %d times, want 0 (config-hit should short-circuit)", tt.key.calls)
				}
				if tt.ollama.calls != 0 {
					t.Errorf("ollama probe called %d times, want 0 (config-hit should short-circuit)", tt.ollama.calls)
				}
				if tt.guided.calls != 0 {
					t.Errorf("guided setup called %d times, want 0 (config-hit should short-circuit)", tt.guided.calls)
				}
			}
		})
	}
}

// TestBackendResolverChainSeatsCuratedTopPick pins E2: both stages that seat
// a fresh OpenRouter backend without a config-pinned model (the key-probe
// hit and the guided-setup fallback) use curatedTopPick(), not a hardcoded
// model string.
func TestBackendResolverChainSeatsCuratedTopPick(t *testing.T) {
	top := curatedTopPick()

	t.Run("key probe hit", func(t *testing.T) {
		r := &BackendResolver{
			Config: &fakeConfigProbe{ok: false},
			Key:    &fakeKeyProbe{source: "openrouter-env", ok: true},
			Smoke:  &fakeSmokeProbe{},
		}
		got, err := r.Resolve()
		if err != nil {
			t.Fatalf("Resolve(): %v", err)
		}
		if got.Model != top.ID {
			t.Errorf("Resolve().Model = %q, want curated top pick %q", got.Model, top.ID)
		}
		if got.Window != top.Window {
			t.Errorf("Resolve().Window = %d, want %d", got.Window, top.Window)
		}
	})

	t.Run("guided setup fallback", func(t *testing.T) {
		r := &BackendResolver{
			Config: &fakeConfigProbe{ok: false},
			Key:    &fakeKeyProbe{ok: false},
			Ollama: &fakeOllamaProbe{up: false},
			Guided: &fakeGuidedSetup{ok: true},
			Smoke:  &fakeSmokeProbe{},
		}
		got, err := r.Resolve()
		if err != nil {
			t.Fatalf("Resolve(): %v", err)
		}
		if got.Model != top.ID {
			t.Errorf("Resolve().Model = %q, want curated top pick %q", got.Model, top.ID)
		}
		if got.Window != top.Window {
			t.Errorf("Resolve().Window = %d, want %d", got.Window, top.Window)
		}
	})
}

func TestBackendResolverChainNoBackends(t *testing.T) {
	r := &BackendResolver{
		Config: &fakeConfigProbe{ok: false},
		Key:    &fakeKeyProbe{ok: false},
		Ollama: &fakeOllamaProbe{up: false},
		Guided: &fakeGuidedSetup{ok: false},
		Smoke:  &fakeSmokeProbe{},
	}

	_, err := r.Resolve()
	if !errors.Is(err, ErrNoBackend) {
		t.Fatalf("Resolve() error = %v, want ErrNoBackend", err)
	}
}
