package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The `provider` routing object rides on the wire only when configured, and
// then verbatim — a zero object must not be sent as `provider: {}`, and an
// unconfigured client must stay byte-compatible with plain OpenAI servers.
func TestOpenAICompatProviderRouting(t *testing.T) {
	no := false
	tests := []struct {
		name     string
		routing  *ProviderRouting
		wantSent bool
		want     string
	}{
		{"omitted when nil", nil, false, ""},
		{"omitted when zero", &ProviderRouting{}, false, ""},
		{
			"pinned order without fallback",
			&ProviderRouting{Order: []string{"novita/fp8"}, AllowFallbacks: &no},
			true,
			`{"order":["novita/fp8"],"allow_fallbacks":false}`,
		},
		{
			"only + quantizations",
			&ProviderRouting{Only: []string{"alibaba"}, Quantizations: []string{"bf16"}},
			true,
			`{"only":["alibaba"],"quantizations":["bf16"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodies []map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Errorf("decode request: %v", err)
				}
				bodies = append(bodies, raw)
				_ = json.NewEncoder(w).Encode(compatResponse{
					Choices: []compatChoice{{Message: compatMessage{Role: "assistant", Content: "ok"}}},
				})
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1", Provider: tt.routing})
			c.SetModel("m")
			if _, err := c.Generate(context.Background(), "hello"); err != nil {
				t.Fatalf("generate: %v", err)
			}
			if _, _, err := c.GenerateWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, nil, ""); err != nil {
				t.Fatalf("generate with tools: %v", err)
			}
			if len(bodies) != 2 {
				t.Fatalf("got %d requests, want 2", len(bodies))
			}
			for i, raw := range bodies {
				got, present := raw["provider"]
				if present != tt.wantSent {
					t.Fatalf("request %d: provider present=%v want %v (body provider=%s)", i, present, tt.wantSent, got)
				}
				if tt.wantSent && string(got) != tt.want {
					t.Errorf("request %d: provider=%s want %s", i, got, tt.want)
				}
			}
		})
	}
}

func TestProviderRoutingIsZero(t *testing.T) {
	yes := true
	tests := []struct {
		name string
		r    *ProviderRouting
		want bool
	}{
		{"nil", nil, true},
		{"empty", &ProviderRouting{}, true},
		{"order", &ProviderRouting{Order: []string{"x"}}, false},
		{"allow_fallbacks set", &ProviderRouting{AllowFallbacks: &yes}, false},
		{"data_collection", &ProviderRouting{DataCollection: "deny"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.IsZero(); got != tt.want {
				t.Errorf("IsZero()=%v want %v", got, tt.want)
			}
		})
	}
}
