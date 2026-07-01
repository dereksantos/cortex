package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// AgentRequest captures parameters to be sent to the agent via API call.
type AgentRequest struct {
	Model string `json:"model"`
	// TODO(derek.s): Rename this to Journal once basic repl is established and integrate with journalling engine.
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Tools       []Tool    `json:"tools,omitempty"`
	// MaxTokens caps OUTPUT (completion) tokens per request — the runaway backstop
	// (see codeMaxOutputTokens). Stamped finite at the single build site
	// (requestFor) / the engine Bounds / session init, so a request is never sent
	// unbounded.
	MaxTokens int `json:"max_tokens,omitempty"`
	// ChatTemplateKwargs passes variables to the server-side chat template
	// (llama.cpp via LiteLLM honors it; unknown variables are ignored). Used to
	// disable built-in reasoning on hybrid thinking models — see
	// ModelSpec.TemplateKwargs.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	// BaseURL is the endpoint root (e.g. http://localhost:4000), resolved from
	// config. Not serialized — it's transport, not request body.
	BaseURL string `json:"-"`
	// APIKey is the Bearer token for endpoints that need one (e.g. OpenRouter).
	// Empty for local endpoints. Not serialized.
	APIKey string `json:"-"`
	// EphemeralSystem is per-turn context (e.g. retrieved memory) merged into
	// the system message ONLY for the wire payload — never stored in Messages,
	// so it doesn't accumulate across turns or persist. Set before a turn,
	// cleared after. The durable record of what was retrieved lives in the
	// transcript as a separate labelled entry, not here.
	EphemeralSystem string `json:"-"`

	// Stream and StreamOptions are set only on the streaming payload (SendStream);
	// omitempty keeps the blocking request byte-identical to before.
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	// Usage opts into OpenRouter's cost reporting (usage:{include:true}); set
	// only for OpenRouter so local backends never see an unknown field.
	Usage *usageInclude `json:"usage,omitempty"`
}

// usageInclude is OpenRouter's request-side flag to return dollar cost in the
// response usage object.
type usageInclude struct {
	Include bool `json:"include"`
}

// streamOptions toggles OpenAI's include_usage so the streamed response ends
// with a chunk carrying token counts (otherwise streaming reports no usage,
// and the context gauge would never update).
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireMessages returns the messages to send: Messages as-is, or — when an
// ephemeral per-turn note is set — a copy with that note folded onto the LAST
// USER message. The stored Messages are never mutated, so nothing accumulates
// and the transcript stays clean.
func (r *AgentRequest) wireMessages() []Message {
	if r.EphemeralSystem == "" || len(r.Messages) == 0 {
		return r.Messages
	}
	out := make([]Message, len(r.Messages))
	copy(out, r.Messages)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == RoleUser {
			out[i].Content = out[i].Content + "\n\n" + r.EphemeralSystem
			return out
		}
	}
	return out
}

// applyPromptCache marks Anthropic prompt-cache breakpoints on the wire messages
// so the stable prefix is billed at ~10% on a hit.
func applyPromptCache(msgs []Message, model string) {
	if !strings.HasPrefix(model, "anthropic/") || len(msgs) == 0 {
		return
	}
	ephemeral := &cacheControl{Type: "ephemeral"}
	msgs[0].cache = ephemeral
	for i := len(msgs) - 1; i >= 1; i-- {
		if msgs[i].Role == RoleUser {
			if i-1 >= 1 {
				msgs[i-1].cache = ephemeral
			}
			break
		}
	}
}

// httpClient is shared by all model calls. The timeout is the backstop guard:
// without it a server that accepts the request and never answers hangs the
// REPL forever.
var httpClient = &http.Client{Timeout: requestTimeout}

// Send runs one model call with bounded retry. Transient failures retry up to
// maxSendAttempts with linear backoff; anything else returns immediately.
func (r *AgentRequest) Send(ctx context.Context) (*AgentResponse, error) {
	payload := *r
	payload.Messages = r.wireMessages()
	applyPromptCache(payload.Messages, r.Model)
	b, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("error marshaling agent request: %w", err)
	}

	base := r.BaseURL
	if base == "" {
		base = defaultEndpoint
	}
	url := llm.NormalizeBaseURL(base) + "/chat/completions"

	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * retryBackoff):
			}
			payload.Temperature = r.Temperature + float64(attempt-1)*0.4
			if nb, mErr := json.Marshal(&payload); mErr == nil {
				b = nb
			}
		}
		res, retryable, err := r.sendOnce(ctx, url, b)
		if err == nil {
			return res, nil
		}
		if !retryable || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("model call failed after %d attempts: %w", maxSendAttempts, lastErr)
}

// sendOnce performs a single HTTP round trip.
func (r *AgentRequest) sendOnce(ctx context.Context, url string, body []byte) (res *AgentResponse, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("error building agent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	llm.SetAttribution(req.Header)
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("error executing agent request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("error reading agent response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		transient := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, transient, fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(respBody))
	}

	var response AgentResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, false, fmt.Errorf("error unmarshaling agent response: %w", err)
	}
	return &response, false, nil
}

// SendStream runs one model call over SSE and assembles the result into the
// same *AgentResponse shape Send returns.
func (r *AgentRequest) SendStream(ctx context.Context, onContent, onReasoning func(string)) (*AgentResponse, error) {
	payload := *r
	payload.Messages = r.wireMessages()
	applyPromptCache(payload.Messages, r.Model)
	payload.Stream = true
	payload.StreamOptions = &streamOptions{IncludeUsage: true}
	b, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("error marshaling agent request: %w", err)
	}

	base := r.BaseURL
	if base == "" {
		base = defaultEndpoint
	}
	url := llm.NormalizeBaseURL(base) + "/chat/completions"

	hc := llm.StreamHTTPClient(requestTimeout)

	var started bool
	guarded := func(s string) {
		started = true
		if onContent != nil {
			onContent(s)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * retryBackoff):
			}
		}
		res, err := llm.StreamChat(ctx, hc, url, r.APIKey, b, guarded, onReasoning)
		if err == nil {
			return assembleStreamResponse(res), nil
		}
		if started || ctx.Err() != nil || !retryableStreamErr(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("model call failed after %d attempts: %w", maxSendAttempts, lastErr)
}

// assembleStreamResponse maps the streamed aggregate into the wire
// AgentResponse shape.
func assembleStreamResponse(res llm.StreamResult) *AgentResponse {
	calls := make([]ToolCall, 0, len(res.ToolCalls))
	for _, tc := range res.ToolCalls {
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		calls = append(calls, ToolCall{
			ID:       tc.ID,
			Type:     typ,
			Function: FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	return &AgentResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: res.Content, ToolCalls: calls},
			FinishReason: res.FinishReason,
		}},
		Usage: Usage{
			PromptTokens:     res.Stats.InputTokens,
			CompletionTokens: res.Stats.OutputTokens,
			TotalTokens:      res.Stats.TotalTokens(),
			Cost:             res.Stats.CostUSD,
		},
	}
}

// retryableStreamErr classifies a streaming failure as transient.
func retryableStreamErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "request failed") ||
		strings.Contains(msg, "stream status 429") ||
		strings.Contains(msg, "stream status 5")
}

// AgentResponse captures the agents response from an AgentRequest.
type AgentResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Usage captures token counts for the agent request and response.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

// Choice represents the model response(s).
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Message contains a single prompt and the role.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`

	cache *cacheControl
}

// cacheControl is an Anthropic prompt-cache breakpoint.
type cacheControl struct {
	Type string `json:"type"`
}

// contentPart is the structured content form Anthropic requires.
type contentPart struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON emits normal string-content messages unless a cache breakpoint is
// set, then emits Anthropic's structured content part shape.
func (m *Message) MarshalJSON() ([]byte, error) {
	if m.cache == nil {
		type alias Message
		return json.Marshal(alias(*m))
	}
	return json.Marshal(struct {
		Role       string        `json:"role"`
		Content    []contentPart `json:"content"`
		ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
		ToolCallID string        `json:"tool_call_id,omitempty"`
	}{
		Role:       m.Role,
		Content:    []contentPart{{Type: "text", Text: m.Content, CacheControl: m.cache}},
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	})
}
