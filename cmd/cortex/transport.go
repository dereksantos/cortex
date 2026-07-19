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
	Model       string    `json:"model"`
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
	// EphemeralSystem is per-turn context (for example, the memory index),
	// inserted as a fixed-position message between the stable prefix/outline and
	// hydrated tail. It is never folded into the system message: doing so would
	// invalidate the provider cache from byte zero. When unchanged, this slot
	// and the tail remain cacheable; when memory changes, only the suffix after
	// the stable prefix must be re-prefilled.
	EphemeralSystem string `json:"-"`

	// OutlineBlock is rendered outline of demoted turns (zone A of docs/context-architecture.md),
	// inserted as a fixed-position user message right after the system message.
	// Grows only by append (cache-stable). Empty until the first demotion.
	// Like EphemeralSystem it is wire-only: never stored in Messages, never persisted.
	OutlineBlock string `json:"-"`

	// PrefixEnd is the message-log index where demotable turn content begins
	// (WorkingSet.Base). Messages[1:PrefixEnd] precede any turn the working set
	// tracks — a Compact summary, or a resumed history the replay could not
	// stamp into spans — and are ALWAYS sent, as part of the stable prefix.
	// 0 or 1 means turn content starts right after the system message.
	PrefixEnd int `json:"-"`

	// TailFrom is the message-log index where the hydrated tail begins (WorkingSet.FrontierMsg).
	// Messages[PrefixEnd:TailFrom] are demoted — represented by OutlineBlock — and are NOT sent.
	// TailFrom <= PrefixEnd means nothing is demoted.
	TailFrom int `json:"-"`

	// Stream and StreamOptions are set only on the streaming payload (SendStream);
	// omitempty keeps the blocking request byte-identical to before.
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	// Usage opts into OpenRouter's cost reporting (usage:{include:true}); set
	// only for OpenRouter so local backends never see an unknown field.
	Usage *usageInclude `json:"usage,omitempty"`
	// Reasoning is OpenRouter's request-body `reasoning: {...}` field
	// (docs/thinking-models.md §2); set only for OpenRouter, mirroring how
	// Usage above is OpenRouter-only — local backends speak
	// ChatTemplateKwargs instead (see llm.Translate).
	Reasoning *llm.Reasoning `json:"reasoning,omitempty"`

	// Dialect and Effort are wire-invisible (json:"-"): which provider
	// dialect this request's effort fields were translated for, and the
	// resolved Effort itself. Recorded by applyEffort (effort.go) so a later
	// in-flight mutation (disableEffortForSend, escalateEffortOnce) can
	// recompute the SAME dialect's wire fields for a one-shot override,
	// rather than inferring the dialect from which field happens to be set
	// (an OpenRouter request with effort "on" leaves Reasoning nil too).
	Dialect llm.Dialect `json:"-"`
	Effort  llm.Effort  `json:"-"`

	// Timeout / MaxAttempts / Backoff (all json:"-") are the P1
	// timeout-unification transport knobs: the per-request HTTP deadline,
	// retry-attempt ceiling, and linear-backoff base. Zero means "use the
	// package default" (requestTimeout / maxSendAttempts / retryBackoff) —
	// so any AgentRequest built without going through requestFor or
	// NewCortexSession's stamping (every existing test literal in this
	// package) behaves exactly as before. See requestFor (loop.go) and
	// NewCortexSession (session_core.go) for where these get stamped from a
	// ModelSpec's config-resolved values.
	Timeout     time.Duration `json:"-"`
	MaxAttempts int           `json:"-"`
	Backoff     time.Duration `json:"-"`
}

func (r *AgentRequest) effectiveTimeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return requestTimeout
}

func (r *AgentRequest) effectiveMaxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return maxSendAttempts
}

func (r *AgentRequest) effectiveBackoff() time.Duration {
	if r.Backoff > 0 {
		return r.Backoff
	}
	return retryBackoff
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

// wireMessages returns the messages to send, assembled in a two-zone layout:
//   - element 0: r.Messages[0] (the system message)
//   - then r.Messages[1:PrefixEnd]: pre-turn content (Compact summary, unstamped
//     resumed history) — never demoted, part of the append-stable prefix
//   - if r.OutlineBlock != "": Message{Role: RoleUser, Content: r.OutlineBlock}
//   - if r.EphemeralSystem != "": Message{Role: RoleUser, Content: r.EphemeralSystem} (after outline)
//   - then the tail: r.Messages[TailFrom:] — the demoted region [PrefixEnd, TailFrom)
//     is what the outline stands in for
//
// The stored Messages are never mutated, so nothing accumulates and the transcript stays clean.
// Returns r.Messages unchanged if there's nothing to insert or demote.
func (r *AgentRequest) wireMessages() []Message {
	if len(r.Messages) == 0 {
		return r.Messages
	}

	prefixEnd := r.PrefixEnd
	if prefixEnd < 1 {
		prefixEnd = 1
	}
	if prefixEnd > len(r.Messages) {
		prefixEnd = len(r.Messages)
	}
	start := r.TailFrom
	if start < prefixEnd {
		start = prefixEnd
	}
	if start > len(r.Messages) {
		start = len(r.Messages)
	}

	// Early return when nothing is demoted or injected.
	if r.OutlineBlock == "" && start <= prefixEnd && r.EphemeralSystem == "" {
		return r.Messages
	}

	out := make([]Message, 0, 3+prefixEnd+len(r.Messages[start:]))
	out = append(out, r.Messages[0])
	out = append(out, r.Messages[1:prefixEnd]...)

	if r.OutlineBlock != "" {
		out = append(out, Message{Role: RoleUser, Content: r.OutlineBlock})
	}

	if r.EphemeralSystem != "" {
		out = append(out, Message{Role: RoleUser, Content: r.EphemeralSystem})
	}

	out = append(out, r.Messages[start:]...)

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

// httpClient is shared by all model calls. It carries no fixed Timeout of
// its own — the backstop guard (without which a server that accepts the
// request and never answers would hang the REPL forever) is the per-request
// context deadline sendOnce applies via r.effectiveTimeout(), which is what
// makes a per-role request_timeout_sec override actually take effect: a
// client-level Timeout here would silently re-impose the requestTimeout
// default as a hard ceiling even when a config override asks for longer.
var httpClient = &http.Client{}

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

	maxAttempts := r.effectiveMaxAttempts()
	backoff := r.effectiveBackoff()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * backoff):
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
	return nil, fmt.Errorf("model call failed after %d attempts: %w", maxAttempts, lastErr)
}

// sendOnce performs a single HTTP round trip. The per-request deadline is
// enforced via context (not httpClient.Timeout, which stays the process-wide
// backstop) so r.Timeout — the P1 per-role override — takes effect without
// needing a fresh *http.Client per call.
func (r *AgentRequest) sendOnce(ctx context.Context, url string, body []byte) (res *AgentResponse, retryable bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.effectiveTimeout())
	defer cancel()
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

	hc := llm.StreamHTTPClient(r.effectiveTimeout())

	var started bool
	guarded := func(s string) {
		started = true
		if onContent != nil {
			onContent(s)
		}
	}

	maxAttempts := r.effectiveMaxAttempts()
	backoff := r.effectiveBackoff()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * backoff):
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
	return nil, fmt.Errorf("model call failed after %d attempts: %w", maxAttempts, lastErr)
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
			Reasoning:    res.Reasoning,
		}},
		Usage: Usage{
			PromptTokens:     res.Stats.InputTokens,
			CompletionTokens: res.Stats.OutputTokens,
			TotalTokens:      res.Stats.TotalTokens(),
			Cost:             res.Stats.CostUSD,
			PromptTokensDetails: &promptTokensDetails{
				CachedTokens: res.Stats.CachedInputTokens,
			},
			CompletionTokensDetails: &completionTokensDetails{
				ReasoningTokens: res.Stats.ReasoningTokens,
			},
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
	// PromptTokensDetails carries the provider's prompt-cache report
	// (llama.cpp and OpenAI-compatible backends set cached_tokens; absent
	// elsewhere). cached_tokens out of prompt_tokens were served from cache —
	// the direct measure of the two-zone layout's prefix stability.
	PromptTokensDetails *promptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// CompletionTokensDetails carries a reasoning model's token split: how
	// many of CompletionTokens were spent on chain-of-thought vs the visible
	// answer. Absent (nil) on backends/models that don't report it.
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details,omitempty"`
}

// promptTokensDetails is the OpenAI-compatible usage sub-object.
type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// completionTokensDetails is the OpenAI-compatible usage sub-object carrying
// a reasoning model's token split.
type completionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// CachedPromptTokens returns the provider-reported cached prompt tokens, or 0
// when the backend doesn't report caching.
func (u Usage) CachedPromptTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// ReasoningTokens returns the provider-reported reasoning-token count, or 0
// when the backend doesn't report it.
func (u Usage) ReasoningTokens() int {
	if u.CompletionTokensDetails == nil {
		return 0
	}
	return u.CompletionTokensDetails.ReasoningTokens
}

// Choice represents the model response(s).
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	// Reasoning is the model's chain-of-thought for this choice, parsed off
	// the wire response's message.reasoning_content / message.reasoning (a
	// reasoning model's blocking-path fields) plus any leading
	// <think>...</think> fence stripped out of Message.Content — display/
	// telemetry-only, mirroring what the streaming path already exposes on
	// StreamResult.Reasoning. json:"-" so it is never re-serialized: the
	// custom UnmarshalJSON below is what actually populates it from the wire,
	// and Message itself carries no reasoning field at all, so a stored or
	// re-marshaled Message can never leak fence bytes or a reasoning field
	// (see TestChoiceReasoningNeverRoundTrips).
	Reasoning string `json:"-"`
}

// wireChoice mirrors the raw JSON shape of one choice in a blocking
// chat-completions response, with the message-level reasoning fields that
// Message itself deliberately omits.
type wireChoice struct {
	Index   int `json:"index"`
	Message struct {
		Role       string     `json:"role"`
		Content    string     `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		// ReasoningContent / Reasoning are the blocking-path counterparts of
		// streamDelta's fields in pkg/llm/stream.go (reasoning_content is the
		// common name; reasoning is OpenRouter's).
		ReasoningContent string `json:"reasoning_content"`
		Reasoning        string `json:"reasoning"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// UnmarshalJSON captures the message-level reasoning_content/reasoning fields
// (silently dropped by decoding straight into Message, which has no such
// field) into Choice.Reasoning, and strips a leading <think>...</think> fence
// out of Message.Content into the same field via llm.StripLeadingThinkFence —
// blocking-path parity for what pkg/llm/stream.go's thinkFenceFilter already
// does per-delta on the streaming path.
func (c *Choice) UnmarshalJSON(data []byte) error {
	var wire wireChoice
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("unmarshal choice: %w", err)
	}
	c.Index = wire.Index
	c.FinishReason = wire.FinishReason

	content, fenceReasoning := llm.StripLeadingThinkFence(wire.Message.Content)
	c.Message = Message{
		Role:       wire.Message.Role,
		Content:    content,
		ToolCalls:  wire.Message.ToolCalls,
		ToolCallID: wire.Message.ToolCallID,
	}

	reasoning := wire.Message.ReasoningContent
	if reasoning == "" {
		reasoning = wire.Message.Reasoning
	}
	if fenceReasoning != "" {
		if reasoning != "" {
			reasoning += fenceReasoning
		} else {
			reasoning = fenceReasoning
		}
	}
	c.Reasoning = reasoning
	return nil
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
