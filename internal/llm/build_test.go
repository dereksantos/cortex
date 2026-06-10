package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestBuildProvider_ResolutionOrder mirrors the 8-case table in
// docs/provider-resolution-refactor.md. Each case constructs a cfg +
// modelID + options combination and asserts the concrete backend the
// resolver picks.
//
// OpenRouter slash-prefix cases require an OpenRouter key. The test
// sets OPEN_ROUTER_API_KEY before t.Run so secret.MustOpenRouterKey
// returns reliably on machines without a keychain entry. (The
// underlying sync.Once may cache an earlier resolution — that's fine,
// we only need the key path to succeed, not the source attribution.)
func TestBuildProvider_ResolutionOrder(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "test-key-for-resolution-order")

	const chatterboxBase = "http://localhost:13305/v1"
	const overrideBase = "http://x.example/v1"

	withChatterbox := func() *config.Config {
		return &config.Config{
			OllamaModel: "qwen2.5-coder:1.5b",
			OllamaURL:   "http://localhost:11434",
			Endpoints: []config.EndpointDef{
				{Name: "chatterbox", BaseURL: chatterboxBase},
			},
		}
	}

	withChatterboxRoleMap := func() *config.Config {
		c := withChatterbox()
		c.Models = &config.ModelsMap{
			Code: &config.RoleAssignment{
				Endpoint: "chatterbox",
				Model:    "Qwen3-Coder-30B-A3B-Instruct-GGUF",
			},
		}
		return c
	}

	cases := []struct {
		name    string
		cfg     *config.Config
		modelID string
		opts    []Option
		check   func(t *testing.T, p llm.Provider)
	}{
		{
			name:    "phase4_prefix",
			cfg:     withChatterbox(),
			modelID: "chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF",
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenAICompatClient)
				if !ok {
					t.Fatalf("want *OpenAICompatClient, got %T", p)
				}
				if c.BaseURL() != chatterboxBase {
					t.Errorf("base url = %q, want %q", c.BaseURL(), chatterboxBase)
				}
				if c.Model() != "Qwen3-Coder-30B-A3B-Instruct-GGUF" {
					t.Errorf("model = %q, want stripped form", c.Model())
				}
			},
		},
		{
			name:    "phase4_role_map",
			cfg:     withChatterboxRoleMap(),
			modelID: "Qwen3-Coder-30B-A3B-Instruct-GGUF",
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenAICompatClient)
				if !ok {
					t.Fatalf("want *OpenAICompatClient via role-map, got %T", p)
				}
				if c.BaseURL() != chatterboxBase {
					t.Errorf("base url = %q, want %q", c.BaseURL(), chatterboxBase)
				}
			},
		},
		{
			name:    "endpoint_override",
			cfg:     &config.Config{},
			modelID: "any-model-id",
			opts:    []Option{WithEndpointOverride(overrideBase)},
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenAICompatClient)
				if !ok {
					t.Fatalf("want *OpenAICompatClient, got %T", p)
				}
				if c.BaseURL() != overrideBase {
					t.Errorf("base url = %q, want %q", c.BaseURL(), overrideBase)
				}
				if c.Model() != "any-model-id" {
					t.Errorf("model = %q, want any-model-id", c.Model())
				}
			},
		},
		{
			name:    "endpoint_override_beats_phase4",
			cfg:     withChatterbox(),
			modelID: "chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF",
			opts:    []Option{WithEndpointOverride(overrideBase)},
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenAICompatClient)
				if !ok {
					t.Fatalf("want *OpenAICompatClient, got %T", p)
				}
				if c.BaseURL() != overrideBase {
					t.Errorf("override should win over phase 4: base url = %q, want %q", c.BaseURL(), overrideBase)
				}
			},
		},
		{
			name:    "slash_prefix_openrouter",
			cfg:     &config.Config{},
			modelID: "anthropic/claude-haiku-4.5",
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenRouterClient)
				if !ok {
					t.Fatalf("want *OpenRouterClient, got %T", p)
				}
				if c.APIURL() == llm.DefaultOllamaURL {
					t.Errorf("api url = Ollama default, expected OpenRouter endpoint")
				}
				if c.Model() != "anthropic/claude-haiku-4.5" {
					t.Errorf("model = %q, want anthropic/claude-haiku-4.5", c.Model())
				}
			},
		},
		{
			name:    "bare_name_ollama",
			cfg:     &config.Config{OllamaModel: "qwen2.5-coder:1.5b"},
			modelID: "qwen2.5-coder:7b",
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenRouterClient)
				if !ok {
					t.Fatalf("want *OpenRouterClient (BackendOllama wraps one), got %T", p)
				}
				if c.APIURL() != llm.DefaultOllamaURL {
					t.Errorf("api url = %q, want Ollama default %q", c.APIURL(), llm.DefaultOllamaURL)
				}
				if c.Model() != "qwen2.5-coder:7b" {
					t.Errorf("model = %q, want qwen2.5-coder:7b", c.Model())
				}
			},
		},
		{
			name:    "empty_model_uses_cfg_default",
			cfg:     &config.Config{OllamaModel: "qwen2.5-coder:1.5b"},
			modelID: "",
			check: func(t *testing.T, p llm.Provider) {
				c, ok := p.(*llm.OpenRouterClient)
				if !ok {
					t.Fatalf("want *OpenRouterClient (BackendOllama), got %T", p)
				}
				if c.Model() != "qwen2.5-coder:1.5b" {
					t.Errorf("model = %q, want cfg.OllamaModel default", c.Model())
				}
				if c.APIURL() != llm.DefaultOllamaURL {
					t.Errorf("api url = %q, want Ollama default", c.APIURL())
				}
			},
		},
		{
			name:    "nil_cfg_no_panic",
			cfg:     nil,
			modelID: "qwen2.5-coder:7b",
			check: func(t *testing.T, p llm.Provider) {
				// The only contract here is "doesn't panic" and returns
				// some Ollama-shaped provider.
				if p == nil {
					t.Fatal("want non-nil provider, got nil")
				}
				if _, ok := p.(*llm.OpenRouterClient); !ok {
					t.Fatalf("want *OpenRouterClient (BackendOllama), got %T", p)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p := BuildProvider(tc.cfg, tc.modelID, tc.opts...)
			tc.check(t, p)
		})
	}
}

// TestBuildProvider_LegacyAPIURL covers the WithAPIURL escape hatch
// the REPL uses to preserve the pre-refactor "apiURL signals
// OpenRouter when non-default" behavior. New callers should prefer
// WithEndpointOverride; this exists only to keep the REPL one-liner
// from changing routing for existing users.
func TestBuildProvider_LegacyAPIURL(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "test-key-for-legacy-apiurl")

	t.Run("ollama_shaped_apiurl_routes_to_ollama", func(t *testing.T) {
		p := BuildProvider(&config.Config{}, "qwen2.5-coder:1.5b",
			WithAPIURL(llm.DefaultOllamaURL))
		c, ok := p.(*llm.OpenRouterClient)
		if !ok {
			t.Fatalf("want *OpenRouterClient, got %T", p)
		}
		if c.APIURL() != llm.DefaultOllamaURL {
			t.Errorf("api url = %q, want %q", c.APIURL(), llm.DefaultOllamaURL)
		}
	})

	t.Run("non_default_apiurl_routes_to_openrouter", func(t *testing.T) {
		const customOR = "https://openrouter.test/api/v1/chat/completions"
		p := BuildProvider(&config.Config{}, "openai/gpt-oss-20b:free",
			WithAPIURL(customOR))
		c, ok := p.(*llm.OpenRouterClient)
		if !ok {
			t.Fatalf("want *OpenRouterClient, got %T", p)
		}
		if c.APIURL() != customOR {
			t.Errorf("api url = %q, want %q", c.APIURL(), customOR)
		}
	})
}

// TestBuildProvider_ChatTemplateKwargs verifies the per-model
// chat-template declaration in config.endpoints flows through
// BuildProvider to the wire: a model with declared kwargs sends them,
// a sibling model on the same endpoint does not.
func TestBuildProvider_ChatTemplateKwargs(t *testing.T) {
	gotKwargs := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotKwargs <- string(raw["chat_template_kwargs"])
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Endpoints: []config.EndpointDef{{
			Name:    "chatterbox",
			BaseURL: srv.URL + "/v1",
			Models:  []string{"coder", "reasoner"},
			ModelChatTemplateKwargs: map[string]map[string]any{
				"coder": {"enable_thinking": false},
			},
		}},
	}

	tests := []struct {
		model string
		want  string // serialized kwargs, "" = field absent
	}{
		{"coder", `{"enable_thinking":false}`},
		{"reasoner", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p := BuildProvider(cfg, tt.model)
			if p == nil {
				t.Fatal("want non-nil provider")
			}
			if _, err := p.Generate(context.Background(), "hi"); err != nil {
				t.Fatalf("generate: %v", err)
			}
			if got := <-gotKwargs; got != tt.want {
				t.Errorf("chat_template_kwargs on wire = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildEmbedder asserts the embedder helper returns an Ollama
// client. Today every embedding caller wants Ollama nomic-embed;
// Phase 4 may route this through cfg.Models.Embed later.
func TestBuildEmbedder(t *testing.T) {
	t.Run("non_nil_cfg", func(t *testing.T) {
		emb := BuildEmbedder(&config.Config{OllamaURL: "http://localhost:11434"})
		if emb == nil {
			t.Fatal("want non-nil embedder")
		}
		if _, ok := emb.(*llm.OllamaClient); !ok {
			t.Errorf("want *OllamaClient, got %T", emb)
		}
	})

	t.Run("nil_cfg_uses_default", func(t *testing.T) {
		emb := BuildEmbedder(nil)
		if emb == nil {
			t.Fatal("want non-nil embedder from default cfg")
		}
	})
}

// TestBuildProvider_NilCfg ensures the nil-cfg case the doc calls out
// can't regress. Some callers (debug commands, tests) pass nil.
func TestBuildProvider_NilCfg(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("BuildProvider(nil, ...) panicked: %v", r)
		}
	}()
	_ = BuildProvider(nil, "qwen2.5-coder:1.5b")
}

// guard: keep the test file from accidentally relying on a user's
// real keychain key.
var _ = os.Getenv
