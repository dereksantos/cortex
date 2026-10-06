package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// goldenTextOnly pins the #216 requirement that text-only requests
// serialize byte for byte as they did before content parts existed: the
// exact bytes are inlined here (no testdata file to silently regenerate).
func TestChatMessageMarshalGolden(t *testing.T) {
	tests := []struct {
		name string
		msg  ChatMessage
		want string
	}{
		{
			name: "user text",
			msg:  ChatMessage{Role: "user", Content: "hello"},
			want: `{"role":"user","content":"hello"}`,
		},
		{
			name: "tool result",
			msg:  ToolResultMessage("call_1", "read_file", "observed output"),
			want: `{"role":"tool","content":"observed output","tool_call_id":"call_1","name":"read_file"}`,
		},
		{
			name: "assistant tool call, empty content",
			msg: ChatMessage{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c1", Type: "function",
				Function: ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`},
			}}},
			want: `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}`,
		},
		{
			name: "system prompt with newline",
			msg:  ChatMessage{Role: "system", Content: "line1\nline2"},
			want: `{"role":"system","content":"line1\nline2"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("byte-identical golden broken:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestChatMessageMarshalContentParts(t *testing.T) {
	const dataURI = "data:image/png;base64,iVBORw0KGgo="
	tests := []struct {
		name string
		msg  ChatMessage
		want string
	}{
		{
			name: "text + http image, detail omitted when unset",
			msg: ChatMessage{Role: "user", Parts: []ContentPart{
				TextPart("what is this?"),
				ImageURLPart("https://example.com/a.png", ""),
			}},
			want: `{"role":"user","content":[{"type":"text","text":"what is this?"},` +
				`{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}`,
		},
		{
			name: "data uri image with detail",
			msg: ChatMessage{Role: "user", Parts: []ContentPart{
				ImageURLPart(dataURI, ImageDetailHigh),
			}},
			want: `{"role":"user","content":[{"type":"image_url","image_url":` +
				`{"url":"` + dataURI + `","detail":"high"}}]}`,
		},
		{
			name: "parts win over string content, tool fields ride",
			msg: ChatMessage{Role: "tool", Content: "ignored", ToolCallID: "c9", Name: "read_file",
				Parts: []ContentPart{TextPart("image attached")}},
			want: `{"role":"tool","content":[{"type":"text","text":"image attached"}],` +
				`"tool_call_id":"c9","name":"read_file"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("marshal:\n got %s\nwant %s", got, tt.want)
			}
			// Round-trip: both shapes decode back.
			var back ChatMessage
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !HasImageParts(tt.msg.Parts) {
				return
			}
			if len(back.Parts) == 0 {
				t.Fatalf("round-trip lost parts: %+v", back)
			}
			if back.Parts[0].HasImage() && back.Parts[0].ImageURL != tt.msg.Parts[imageIdx(tt.msg.Parts)].ImageURL {
				t.Errorf("round-trip url: %q", back.Parts[0].ImageURL)
			}
		})
	}

	t.Run("string content still decodes as string", func(t *testing.T) {
		var m ChatMessage
		if err := json.Unmarshal([]byte(`{"role":"user","content":"hi"}`), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Content != "hi" || len(m.Parts) != 0 {
			t.Errorf("got %+v", m)
		}
	})
}

func imageIdx(parts []ContentPart) int {
	for i, p := range parts {
		if p.HasImage() {
			return i
		}
	}
	return 0
}

func TestValidateContentParts(t *testing.T) {
	tests := []struct {
		name    string
		parts   []ContentPart
		wantErr string // "" = ok; else substring of the error
	}{
		{"text only ok", []ContentPart{TextPart("x")}, ""},
		{"https image ok", []ContentPart{ImageURLPart("https://x.test/a.png", "")}, ""},
		{"http image ok", []ContentPart{ImageURLPart("http://x.test/a.png", "")}, ""},
		{"data uri ok", []ContentPart{ImageURLPart("data:image/png;base64,AAA", "")}, ""},
		{"empty url rejected", []ContentPart{ImageURLPart("", "")}, "empty url"},
		{"file scheme rejected", []ContentPart{ImageURLPart("file:///tmp/a.png", "")}, "scheme"},
		{"non-base64 data uri rejected", []ContentPart{ImageURLPart("data:image/png,AAA", "")}, "base64"},
		{"data uri without media type rejected", []ContentPart{ImageURLPart("data:;base64,AAA", "")}, "media type"},
		{"unknown type rejected", []ContentPart{{Type: "audio"}}, "unsupported type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateContentParts(tt.parts)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q lacks %q", err, tt.wantErr)
			}
		})
	}
}

func TestGateImages(t *testing.T) {
	img := []ContentPart{ImageURLPart("https://x.test/a.png", "")}
	tests := []struct {
		name    string
		parts   []ContentPart
		allow   bool
		wantErr bool
	}{
		{"no parts never gated", nil, false, false},
		{"text-only never gated", []ContentPart{TextPart("hi")}, false, false},
		{"vision model accepts images", img, true, false},
		{"text-only model rejects images", img, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := GateImages(tt.parts, "test/model-x", tt.allow)
			if tt.wantErr {
				if !errors.Is(err, ErrModelNoVision) {
					t.Fatalf("want ErrModelNoVision, got %v", err)
				}
				if !strings.Contains(err.Error(), "test/model-x") {
					t.Errorf("error must name the model: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAnthropicImageContent(t *testing.T) {
	tests := []struct {
		name     string
		part     ContentPart
		wantType string // "" = not ok
		wantKey  string
		wantVal  string
	}{
		{"text part not an image", TextPart("x"), "", "", ""},
		{"data uri becomes base64 source",
			ImageURLPart("data:image/jpeg;base64,QUJD", ""),
			"image", "media_type", "image/jpeg"},
		{"http url becomes url source",
			ImageURLPart("https://x.test/a.png", ""),
			"image", "url", "https://x.test/a.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blk, ok := AnthropicImageContent(tt.part)
			if tt.wantType == "" {
				if ok {
					t.Fatalf("want ok=false, got %v", blk)
				}
				return
			}
			if !ok {
				t.Fatalf("want ok=true")
			}
			if blk["type"] != tt.wantType {
				t.Errorf("type=%v want %s", blk["type"], tt.wantType)
			}
			src, _ := blk["source"].(map[string]any)
			if got := fmt.Sprint(src[tt.wantKey]); got != tt.wantVal {
				t.Errorf("source[%s]=%q want %q", tt.wantKey, got, tt.wantVal)
			}
		})
	}
}

// TestGenerateWithToolsGate drives the transport-level gates end to end:
// a vision client sends the parts array; a text-only client fails locally
// with a model-naming error before any request is made.
func TestGenerateWithToolsVisionGate(t *testing.T) {
	const dataURI = "data:image/png;base64,iVBORw0KGgo="
	tests := []struct {
		name    string
		vision  bool
		wantErr bool
	}{
		{"compat, vision off: clear error, nothing sent", false, true},
		{"compat, vision on: parts sent", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(EndpointConfig{Name: "gate-test", BaseURL: srv.URL + "/v1"})
			c.SetModel("vision-model")
			c.SetVision(tt.vision)
			msgs := []ChatMessage{{Role: "user", Parts: []ContentPart{
				TextPart("describe"), ImageURLPart(dataURI, ""),
			}}}
			_, _, err := c.GenerateWithTools(context.Background(), msgs, nil, "")
			if tt.wantErr {
				if !errors.Is(err, ErrModelNoVision) {
					t.Fatalf("want ErrModelNoVision, got %v", err)
				}
				if body != nil {
					t.Errorf("request must not reach the server: %v", body)
				}
				return
			}
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			var sent []struct {
				Role    string        `json:"role"`
				Content []ContentPart `json:"content"`
			}
			if err := json.Unmarshal(body["messages"], &sent); err != nil {
				t.Fatalf("wire messages not a parts shape: %v — %s", err, body["messages"])
			}
			if len(sent) != 1 || len(sent[0].Content) != 2 || !sent[0].Content[1].HasImage() {
				t.Errorf("wire parts: %s", body["messages"])
			}
		})
	}

	t.Run("openrouter, vision off: error names the model", func(t *testing.T) {
		t.Setenv("OPEN_ROUTER_API_KEY", "sk-test")
		c := NewOpenRouterClient(nil)
		c.SetModel("some/text-model")
		_, _, err := c.GenerateWithTools(context.Background(),
			[]ChatMessage{{Role: "user", Parts: []ContentPart{ImageURLPart("https://x.test/a.png", "")}}}, nil, "")
		if !errors.Is(err, ErrModelNoVision) || !strings.Contains(err.Error(), "some/text-model") {
			t.Fatalf("want model-naming vision error, got %v", err)
		}
	})
}

func TestOpenRouterListModelsModalities(t *testing.T) {
	tests := []struct {
		name       string
		modalities []string
		want       bool
	}{
		{"image modality", []string{"text", "image"}, true},
		{"text only", []string{"text"}, false},
		{"absent (catalog says nothing)", nil, false},
		{"case-insensitive", []string{"Image"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelAcceptsImages(tt.modalities); got != tt.want {
				t.Errorf("modelAcceptsImages(%v)=%v want %v", tt.modalities, got, tt.want)
			}
		})
	}
}
