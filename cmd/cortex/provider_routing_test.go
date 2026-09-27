package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

func TestSecretEnvNames(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want map[string]bool
	}{
		{"nil config", nil, map[string]bool{}},
		{"backend only", &Config{Backend: Backend{KeyEnv: "OPENROUTER_API_KEY"}}, map[string]bool{"OPENROUTER_API_KEY": true}},
		{
			"backend + role keys",
			&Config{
				Backend: Backend{KeyEnv: "A_KEY", KeyService: "svc"},
				Models:  map[string]ModelSpec{"code": {KeyEnv: "B_KEY"}, "study": {Model: "m"}},
			},
			map[string]bool{"A_KEY": true, "B_KEY": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]bool{}
			for _, n := range tt.cfg.secretEnvNames() {
				got[n] = true
			}
			if len(got) != len(tt.want) {
				t.Fatalf("secretEnvNames=%v want %v", got, tt.want)
			}
			for n := range tt.want {
				if !got[n] {
					t.Errorf("missing %s in %v", n, got)
				}
			}
		})
	}
}

// backend.provider (OpenRouter provider routing) must reach the wire on every
// request path — the coder's AgentRequest, subagent requests — and only on
// the OpenRouter backend. A reproducible benchmark run depends on this: an
// unpinned subagent call would be served by whatever upstream OpenRouter
// picks, silently changing the system under test.
func TestProviderRoutingConfig(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		project  string
		wantSent string // "" = provider must be absent from the wire
	}{
		{
			name:     "openrouter with pinned provider",
			project:  `{"backend":{"type":"openrouter","provider":{"order":["novita/fp8"],"allow_fallbacks":false}}}`,
			wantSent: `{"order":["novita/fp8"],"allow_fallbacks":false}`,
		},
		{
			name:     "non-openrouter backend never sends it",
			project:  `{"backend":{"type":"litellm","provider":{"order":["novita/fp8"],"allow_fallbacks":false}}}`,
			wantSent: "",
		},
		{
			name:     "openrouter without provider sends nothing",
			project:  `{"backend":{"type":"openrouter"}}`,
			wantSent: "",
		},
		{
			name:     "empty provider object sends nothing",
			project:  `{"backend":{"type":"openrouter","provider":{}}}`,
			wantSent: "",
		},
		{
			name:     "project routing replaces user routing wholesale",
			user:     `{"backend":{"type":"openrouter","provider":{"only":["deepinfra"],"ignore":["x"]}}}`,
			project:  `{"backend":{"provider":{"order":["alibaba"],"allow_fallbacks":false}}}`,
			wantSent: `{"order":["alibaba"],"allow_fallbacks":false}`,
		},
		{
			name:     "project without provider inherits user routing",
			user:     `{"backend":{"type":"openrouter","provider":{"order":["alibaba"],"allow_fallbacks":false}}}`,
			project:  `{"backend":{"key_service":"cortex-openrouter"}}`,
			wantSent: `{"order":["alibaba"],"allow_fallbacks":false}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			userPath := filepath.Join(dir, "user.json")
			projPath := filepath.Join(dir, "proj.json")
			if tt.user != "" {
				if err := os.WriteFile(userPath, []byte(tt.user), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(projPath, []byte(tt.project), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := loadMergedConfig(userPath, projPath)
			if cfg == nil {
				t.Fatal("merged config is nil")
			}

			coder := &AgentRequest{Model: "m", Provider: cfg.providerRouting()}
			cs := &CortexSession{Config: cfg, Request: coder}
			reqs := map[string]*AgentRequest{
				"coder": coder,
				"study": cs.subagentRequest(tools.Subagent{Role: roleStudy}, "seed"),
				"agent": cs.subagentRequest(tools.Subagent{Role: "agent"}, "seed"),
			}
			for path, req := range reqs {
				b, err := json.Marshal(req)
				if err != nil {
					t.Fatalf("%s: marshal: %v", path, err)
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(b, &raw); err != nil {
					t.Fatalf("%s: unmarshal: %v", path, err)
				}
				got, present := raw["provider"]
				switch {
				case tt.wantSent == "" && present:
					t.Errorf("%s: provider should be absent, got %s", path, got)
				case tt.wantSent != "" && string(got) != tt.wantSent:
					t.Errorf("%s: provider=%s want %s", path, got, tt.wantSent)
				}
			}
		})
	}
}
