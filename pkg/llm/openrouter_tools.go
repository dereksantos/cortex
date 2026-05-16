package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// OpenAI-format tool-call extension over OpenRouterClient. This file
// is additive: it does NOT touch the Provider interface or the
// plaintext Generate* path. Callers that don't need tool-use see no
// change in behavior or surface area.
//
// The Cortex coding harness (internal/harness) uses this directly,
// without going through the Provider interface, because tool-use is
// fundamentally a different shape of conversation than single-turn
// completion. Pretending otherwise via a Provider-level abstraction
// would only obscure the shape.

// ToolSpec is one function declaration sent to OpenRouter in the
// `tools` array of a chat-completions request. Mirrors OpenAI's
// function-calling schema 1:1 (`type` is always "function" today).
type ToolSpec struct {
	Type     string   `json:"type"`
	Function ToolFunc `json:"function"`
}

// ToolFunc is the function-shaped half of a ToolSpec. Parameters is
// raw JSON because it's a JSON-Schema document — typing it as `any`
// would defeat the point of having a schema. Callers build the schema
// as `json.RawMessage(`{"type":"object","properties":...}`)`.
type ToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is one tool invocation the model emitted in its assistant
// turn. Arguments is a JSON string (NOT decoded) because the model
// may emit malformed JSON — lenient parsing is the harness's job, not
// the transport's.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function" today
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the inner payload of a ToolCall. Arguments is
// the raw JSON string the model emitted.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatMessage is one message in the conversation history. The shape
// is the same for system / user / assistant / tool roles; consumers
// populate only the fields appropriate to a given role.
//
//   - role=system, user: Content
//   - role=assistant (final): Content
//   - role=assistant (tool call): ToolCalls (Content typically empty)
//   - role=tool: Content + ToolCallID + Name
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ChatResult is GenerateWithTools's structured response. Either
// ToolCalls is non-empty (model wants the harness to dispatch tools
// and call again) OR Content is non-empty (model gave a final answer
// — done).
type ChatResult struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
}

// orToolsRequest mirrors orRequest but adds the tool-call surface.
// Kept separate so the plaintext path's JSON shape is unchanged (the
// model providers behind OpenRouter sometimes behave subtly
// differently when `tools` is present-but-empty vs absent).
type orToolsRequest struct {
	Model       string        `json:"model"`
	MaxTokens   int           `json:"max_tokens"`
	Messages    []ChatMessage `json:"messages"`
	Tools       []ToolSpec    `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"`
	Usage       orUsageReq    `json:"usage"`
	Temperature *float64      `json:"temperature,omitempty"`
}

// orToolsChoice / orToolsMessage mirror orChoice/orMessage with the
// added tool_calls field on the message.
type orToolsChoice struct {
	Message      orToolsMessage `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type orToolsMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type orToolsResponse struct {
	ID       string          `json:"id"`
	Model    string          `json:"model"`
	Provider string          `json:"provider"`
	Choices  []orToolsChoice `json:"choices"`
	Usage    orUsageResp     `json:"usage"`
	Error    *orErr          `json:"error,omitempty"`
}

// GenerateWithTools issues one chat-completions request with tool
// schemas and returns the model's response. The caller is responsible
// for appending the assistant message (including any tool_calls) to
// the conversation history and dispatching tools — this method is one
// turn, not a loop.
//
// toolChoice may be:
//   - "" or "auto" — model decides whether to call tools
//   - "none" — model must not call tools
//   - "required" — model must call at least one tool
//   - map[string]any{...} — force a specific function (OpenAI-style)
//
// Token counts populate GenerationStats; per-call USD is exposed via
// LastCostUSD() the same way Generate*() does. The model is whichever
// SetModel() most recently set (or the constructor's value).
func (c *OpenRouterClient) GenerateWithTools(ctx context.Context, msgs []ChatMessage, tools []ToolSpec, toolChoice any) (ChatResult, GenerationStats, error) {
	if c.apiKey == "" {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("openrouter: OPEN_ROUTER_API_KEY not set")
	}

	// Normalize toolChoice: empty string -> "auto" (the model decides).
	if s, ok := toolChoice.(string); ok && s == "" {
		toolChoice = "auto"
	}

	body := orToolsRequest{
		Model:      c.model,
		MaxTokens:  c.maxTokens,
		Messages:   msgs,
		Tools:      tools,
		ToolChoice: toolChoice,
		Usage:      orUsageReq{Include: true},
	}

	bb, err := c.doRaw(ctx, body)
	if err != nil {
		return ChatResult{}, GenerationStats{}, err
	}

	var resp orToolsResponse
	if err := json.Unmarshal(bb, &resp); err != nil {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("openrouter: decode tools response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("openrouter: response had no choices")
	}

	c.lastCostUSD = resp.Usage.Cost
	c.lastProvider = resp.Provider

	choice := resp.Choices[0]
	out := ChatResult{
		Content:      choice.Message.Content,
		ToolCalls:    choice.Message.ToolCalls,
		FinishReason: choice.FinishReason,
	}
	stats := GenerationStats{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}
	return out, stats, nil
}

// AssistantMessageFromResult converts a ChatResult into the
// assistant-role ChatMessage that should be appended to the
// conversation history. The caller passes the returned message back
// in the `msgs` slice on the next GenerateWithTools call so the model
// sees its own tool-call turn.
func AssistantMessageFromResult(r ChatResult) ChatMessage {
	return ChatMessage{
		Role:      "assistant",
		Content:   r.Content,
		ToolCalls: r.ToolCalls,
	}
}

// ToolResultMessage builds a role=tool ChatMessage for the response
// to a single tool call. content is the tool's output text (or an
// error message for the model to consume — the model treats both
// uniformly, so any deterministic textual signal is fine).
func ToolResultMessage(callID, name, content string) ChatMessage {
	return ChatMessage{
		Role:       "tool",
		Content:    content,
		ToolCallID: callID,
		Name:       name,
	}
}
