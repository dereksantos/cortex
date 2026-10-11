package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAICompatGenerateWithToolsCapturesReasoning: the blocking tool-call
// path captures the response's reasoning_content (or its `reasoning` alias)
// into ChatResult.Reasoning instead of discarding it.
func TestOpenAICompatGenerateWithToolsCapturesReasoning(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"reasoning_content", `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"plan","tool_calls":[{"id":"c1","type":"function","function":{"name":"grep","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "plan"},
		{"reasoning alias", `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning":"alias plan"}}]}`, "alias plan"},
		{"reasoning_content wins over alias", `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning_content":"rc","reasoning":"alias"}}]}`, "rc"},
		{"none", `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1"})
			c.SetModel("m")
			res, _, err := c.GenerateWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
			if err != nil {
				t.Fatalf("GenerateWithTools: %v", err)
			}
			if res.Reasoning != tt.want {
				t.Errorf("Reasoning = %q, want %q", res.Reasoning, tt.want)
			}
		})
	}
}
