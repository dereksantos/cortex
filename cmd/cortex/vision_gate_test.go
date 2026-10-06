package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestAgentRequestVisionGate covers #216's gate on cmd/cortex's own
// transport: a message carrying image parts is refused with a
// model-naming error before the request leaves, and accepted (sent as an
// OpenAI parts array) when the binding says the model is vision-capable.
// visionOKResponse is the blocking chat-completions body the gate test
// stand-in backend serves.
const visionOKResponse = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

func TestAgentRequestVisionGate(t *testing.T) {
	img := []llm.ContentPart{
		llm.TextPart("what's in this image?"),
		llm.ImageURLPart("data:image/png;base64,iVBORw0KGgo=", ""),
	}
	tests := []struct {
		name     string
		vision   bool
		wantErr  bool
		wantWire string // substring of the wire body when sent
	}{
		{"vision off: clear error naming the model", false, true, ""},
		{"vision on: parts array on the wire", true, false, `"type":"image_url"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body = string(raw)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(visionOKResponse))
			}))
			defer srv.Close()

			req := CortexArgs{}.Request()
			req.Model = "acme/text-model"
			req.BaseURL = srv.URL
			req.MaxAttempts = 1
			req.Vision = tt.vision
			req.Messages = append(req.Messages[:1], Message{Role: RoleUser, Parts: img})

			_, err := req.Send(context.Background())
			if tt.wantErr {
				if !errors.Is(err, llm.ErrModelNoVision) {
					t.Fatalf("want ErrModelNoVision, got %v", err)
				}
				if !strings.Contains(err.Error(), "acme/text-model") {
					t.Errorf("error must name the model: %v", err)
				}
				if body != "" {
					t.Errorf("nothing should have been sent, got: %s", body)
				}
				return
			}
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if !strings.Contains(body, tt.wantWire) {
				t.Errorf("wire body lacks %q: %s", tt.wantWire, body)
			}
		})
	}

	t.Run("text-only request serialization unchanged", func(t *testing.T) {
		req := CortexArgs{}.Request()
		req.Messages = []Message{{Role: RoleUser, Content: "hi"}}
		b, err := json.Marshal(req.wireMessages())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(b), `{"role":"user","content":"hi"}`) {
			t.Errorf("text-only wire changed: %s", b)
		}
	})
}

// TestResolveBindingVision covers the config side of the gate: explicit
// models.<role>.vision wins; an OpenRouter model without it falls back to
// the capability table's vision tag; everything else defaults to false.
func TestResolveBindingVision(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name    string
		backend Backend
		models  map[string]ModelSpec
		want    bool
	}{
		{"explicit true", Backend{Type: "litellm"}, map[string]ModelSpec{roleCode: {Model: "m", Vision: &yes}}, true},
		{"explicit false", Backend{Type: "openrouter"}, map[string]ModelSpec{roleCode: {Model: "acme/vl-8b", Vision: &no}}, false},
		{"openrouter vision-suffixed model", Backend{Type: "openrouter"}, map[string]ModelSpec{roleCode: {Model: "acme/gpt-4o-vision"}}, true},
		{"openrouter plain model", Backend{Type: "openrouter"}, map[string]ModelSpec{roleCode: {Model: "qwen/qwen3-coder"}}, false},
		{"non-openrouter unset stays false", Backend{Type: "litellm"}, map[string]ModelSpec{roleCode: {Model: "acme/gpt-4o-vision"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Backend: tt.backend, Models: tt.models}
			if got := cfg.resolveBinding(roleCode, nil).VisionEnabled(); got != tt.want {
				t.Errorf("VisionEnabled()=%v want %v", got, tt.want)
			}
		})
	}
}
