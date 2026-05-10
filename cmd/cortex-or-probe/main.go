// Throwaway probe: hits OpenRouter once on a :free model to lock down the
// per-call cost field shape and rate-limit headers used by the eval
// harness. Output goes to stdout (redirect to docs/openrouter-probe.json).
//
// Usage:
//   go run ./cmd/cortex-or-probe > docs/openrouter-probe.json
//
// Requires OPEN_ROUTER_API_KEY in env. Safe to delete once
// pkg/llm/openrouter.go (loop step 2) has internalized the response shape.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	chatURL = "https://openrouter.ai/api/v1/chat/completions"
	genURL  = "https://openrouter.ai/api/v1/generation"
	model   = "openai/gpt-oss-20b:free"
)

type probeOutput struct {
	Probe struct {
		Model     string `json:"model"`
		Timestamp string `json:"timestamp"`
	} `json:"probe"`
	ChatStatus    int               `json:"chat_status"`
	ChatLatencyMs int64             `json:"chat_latency_ms"`
	ChatHeaders   map[string]string `json:"chat_headers"`
	ChatBody      json.RawMessage   `json:"chat_body"`
	GenStatus     int               `json:"gen_status,omitempty"`
	GenLatencyMs  int64             `json:"gen_latency_ms,omitempty"`
	GenHeaders    map[string]string `json:"gen_headers,omitempty"`
	GenBody       json.RawMessage   `json:"gen_body,omitempty"`
	GenSkipped    string            `json:"gen_skipped,omitempty"`
}

func main() {
	key := os.Getenv("OPEN_ROUTER_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPEN_ROUTER_API_KEY not set")
		os.Exit(2)
	}

	out := probeOutput{}
	out.Probe.Model = model
	out.Probe.Timestamp = time.Now().UTC().Format(time.RFC3339)

	// 1. Chat completion. usage:{include:true} is OpenRouter's request-side
	//    opt-in for surfacing per-call cost in the response usage object.
	chatReq := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with exactly: ok"},
		},
		"max_tokens": 8,
		"usage":      map[string]bool{"include": true},
	}
	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		fatal("marshal chat req", err)
	}

	chatStart := time.Now()
	chatResp, err := postJSON(chatURL, key, chatBody)
	if err != nil {
		fatal("chat post", err)
	}
	defer chatResp.Body.Close()
	chatRaw, err := io.ReadAll(chatResp.Body)
	if err != nil {
		fatal("chat read", err)
	}
	out.ChatLatencyMs = time.Since(chatStart).Milliseconds()
	out.ChatStatus = chatResp.StatusCode
	out.ChatHeaders = relevantHeaders(chatResp.Header)
	out.ChatBody = prettyOrEscape(chatRaw)

	if chatResp.StatusCode != 200 {
		emit(&out)
		fmt.Fprintf(os.Stderr, "chat completion failed: status=%d\n", chatResp.StatusCode)
		os.Exit(1)
	}

	// 2. Generation lookup for authoritative cost/usage.
	var ccResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(chatRaw, &ccResp); err != nil || ccResp.ID == "" {
		out.GenSkipped = "no generation id in chat response"
		emit(&out)
		os.Exit(0)
	}

	// Generation endpoint can be ~1-2s behind the completion.
	time.Sleep(2 * time.Second)

	genStart := time.Now()
	genReq, err := http.NewRequest("GET", genURL+"?id="+ccResp.ID, nil)
	if err != nil {
		fatal("gen new request", err)
	}
	genReq.Header.Set("Authorization", "Bearer "+key)
	genReq.Header.Set("HTTP-Referer", "https://github.com/dereksantos/cortex")
	genReq.Header.Set("X-Title", "cortex-eval-harness-probe")
	genResp, err := (&http.Client{Timeout: 30 * time.Second}).Do(genReq)
	if err != nil {
		fatal("gen get", err)
	}
	defer genResp.Body.Close()
	genRaw, err := io.ReadAll(genResp.Body)
	if err != nil {
		fatal("gen read", err)
	}
	out.GenLatencyMs = time.Since(genStart).Milliseconds()
	out.GenStatus = genResp.StatusCode
	out.GenHeaders = relevantHeaders(genResp.Header)
	out.GenBody = prettyOrEscape(genRaw)

	emit(&out)
}

func postJSON(url, key string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	// Optional but recommended for OpenRouter attribution.
	req.Header.Set("HTTP-Referer", "https://github.com/dereksantos/cortex")
	req.Header.Set("X-Title", "cortex-eval-harness-probe")
	return (&http.Client{Timeout: 30 * time.Second}).Do(req)
}

// relevantHeaders keeps only rate-limit / OpenRouter-specific / timing
// headers — never emits anything that could carry a secret. Authorization
// is a request-side header so it never appears in the response anyway.
func relevantHeaders(h http.Header) map[string]string {
	keep := map[string]string{}
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-ratelimit") ||
			strings.HasPrefix(lk, "x-or-") ||
			strings.HasPrefix(lk, "x-openrouter-") ||
			lk == "date" || lk == "retry-after" {
			keep[k] = v[0]
		}
	}
	return keep
}

// prettyOrEscape returns indented JSON when b parses, otherwise wraps the
// raw bytes as a JSON string so the outer envelope is always valid.
func prettyOrEscape(b []byte) json.RawMessage {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err == nil {
		return pretty.Bytes()
	}
	s, _ := json.Marshal(string(b))
	return s
}

func emit(out *probeOutput) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
	}
}

func fatal(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
}
