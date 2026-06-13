// Package llm — provider factory resolution-order tests.
//
// Pins the three-tier resolution order Get/build implement:
//  1. cfg.ResolveModelRoute (Phase 4 endpoint registry, incl.
//     endpoint.Models bare-name list)
//  2. Slash-prefixed modelID with no Phase 4 hit → OpenRouter
//  3. Bare modelID → Ollama
//
// The order matters because operators declare endpoints (Phase 4) to
// override the legacy slash heuristic — a "coder" bare name should
// route to chatterbox:4000 when that endpoint declares "coder" in
// its Models list, not silently fall through to Ollama.

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
)

func TestProviderFactory_BareName_RoutesViaEndpointModelsList(t *testing.T) {
	cfg := &config.Config{
		Endpoints: []config.EndpointDef{
			{
				Name:    "chatterbox",
				BaseURL: "http://chatterbox:4000",
				Models:  []string{"coder", "xlam-1b-fc-r"},
			},
		},
	}
	f := NewProviderFactory(FactoryConfig{Cfg: cfg})
	p, err := f.Get("xlam-1b-fc-r")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// OpenAICompatClient is the type Phase 4 routes use; identifying
	// the concrete type confirms the resolution path (vs Ollama
	// LLMClient or OpenRouterClient).
	if _, ok := p.(*OpenAICompatClient); !ok {
		t.Errorf("expected *OpenAICompatClient for endpoint-routed bare name, got %T", p)
	}
}

func TestProviderFactory_BareName_FallsThroughToOllamaWhenUnconfigured(t *testing.T) {
	// No endpoint declares this model — bare names fall through to
	// the Ollama backend (legacy behavior preserved).
	cfg := &config.Config{
		Endpoints: []config.EndpointDef{
			{Name: "chatterbox", BaseURL: "http://chatterbox:4000", Models: []string{"coder"}},
		},
	}
	f := NewProviderFactory(FactoryConfig{Cfg: cfg, OllamaAPIURL: "http://localhost:11434"})
	p, err := f.Get("unlisted-bare-name")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := p.(*OpenAICompatClient); ok {
		t.Errorf("unlisted bare name should NOT route via OpenAI-compat (chatterbox), got %T", p)
	}
}

// The factory must build endpoint clients with the SAME wire behavior
// as internal/llm.BuildProvider: per-model chat_template_kwargs ride
// the request, and a bare-root base_url still hits /v1/chat/completions.
// Regression: the factory dropped kwargs, so the budget classifier's
// reasoner ran with thinking ON and blew its 30s deadline (2026-06-12).
func TestProviderFactory_EndpointRoute_KwargsAndV1OnTheWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path: got %s want /v1/chat/completions", r.URL.Path)
		}
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		got, present := raw["chat_template_kwargs"]
		if !present || !strings.Contains(string(got), `"enable_thinking":false`) {
			t.Errorf("chat_template_kwargs missing or wrong: present=%v body=%s", present, got)
		}
		_ = json.NewEncoder(w).Encode(compatResponse{
			Choices: []compatChoice{{Message: compatMessage{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer srv.Close()

	cfg := &config.Config{
		Endpoints: []config.EndpointDef{
			{
				Name:    "chatterbox",
				BaseURL: srv.URL, // bare root, no /v1 — like the live fleet config
				Models:  []string{"reasoner"},
				ModelChatTemplateKwargs: map[string]map[string]any{
					"reasoner": {"enable_thinking": false},
				},
			},
		},
	}
	f := NewProviderFactory(FactoryConfig{Cfg: cfg})
	p, err := f.Get("reasoner")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := p.Generate(context.Background(), "hello"); err != nil {
		t.Fatalf("generate: %v", err)
	}
}

func TestProviderFactory_ExplicitEndpointPrefix_StillResolves(t *testing.T) {
	// Pre-Phase-4 callers that used "endpoint/model" form keep working
	// — same resolution path as the new bare-name route, just
	// triggered by the slash-prefix form.
	cfg := &config.Config{
		Endpoints: []config.EndpointDef{
			{Name: "chatterbox", BaseURL: "http://chatterbox:4000"},
		},
	}
	f := NewProviderFactory(FactoryConfig{Cfg: cfg})
	p, err := f.Get("chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := p.(*OpenAICompatClient); !ok {
		t.Errorf("expected *OpenAICompatClient for chatterbox/-prefixed route, got %T", p)
	}
}
