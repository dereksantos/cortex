package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool-calling extension over OpenAICompatClient. Additive: doesn't
// touch the Provider interface or the plaintext Generate* path.
//
// The Cortex coding harness (internal/harness) and the v2 cortex
// harness both use GenerateWithTools through the LoopProvider
// interface; this method is what makes OpenAICompatClient a drop-in
// replacement for OpenRouterClient in those callers.

// compatToolsRequest mirrors orToolsRequest but drops the
// `usage:{include:true}` opt-in extension — that's OpenRouter-specific
// (the inline cost field that ride along). Standard OpenAI servers
// just return token counts in `usage` by default.
type compatToolsRequest struct {
	Model      string        `json:"model"`
	MaxTokens  int           `json:"max_tokens"`
	Messages   []ChatMessage `json:"messages"`
	Tools      []ToolSpec    `json:"tools,omitempty"`
	ToolChoice any           `json:"tool_choice,omitempty"`
}

type compatToolsResponse struct {
	ID      string              `json:"id"`
	Model   string              `json:"model"`
	Choices []compatToolsChoice `json:"choices"`
	Usage   compatUsage         `json:"usage"`
	Error   *compatErr          `json:"error,omitempty"`
}

type compatToolsChoice struct {
	Message      compatToolsMessage `json:"message"`
	FinishReason string             `json:"finish_reason"`
}

type compatToolsMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// GenerateWithTools issues one chat-completions request with tool
// schemas attached, returning the model's response. Same contract as
// OpenRouterClient.GenerateWithTools — single turn, caller drives the
// loop.
//
// toolChoice semantics match OpenAI's:
//   - "" or "auto": model decides
//   - "none":       model must not call tools
//   - "required":   model must call at least one tool
//   - map[...]:     force a specific function
//
// Per the chatterbox-integration.md doc §5, Qwen3-Coder is tuned for
// OpenAI-style tool calling and occasionally emits malformed JSON in
// tool args. Recovery is the caller's responsibility (this method
// returns the assistant message verbatim — the harness loop has a
// one-retry recovery path keyed on json.JSONDecodeError).
func (c *OpenAICompatClient) GenerateWithTools(ctx context.Context, msgs []ChatMessage, tools []ToolSpec, toolChoice any) (ChatResult, GenerationStats, error) {
	if c.model == "" {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("%s: model not set", c.name)
	}

	if s, ok := toolChoice.(string); ok && s == "" {
		toolChoice = "auto"
	}

	body := compatToolsRequest{
		Model:      c.model,
		MaxTokens:  c.maxTokens,
		Messages:   msgs,
		Tools:      tools,
		ToolChoice: toolChoice,
	}

	bb, err := c.doRaw(ctx, "/chat/completions", body)
	if err != nil {
		return ChatResult{}, GenerationStats{}, err
	}

	var resp compatToolsResponse
	if err := json.Unmarshal(bb, &resp); err != nil {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("%s: decode tools response: %w", c.name, err)
	}
	if resp.Error != nil {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("%s: server error: %s", c.name, resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return ChatResult{}, GenerationStats{}, fmt.Errorf("%s: response had no choices", c.name)
	}

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
