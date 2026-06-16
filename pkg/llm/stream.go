package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// streamChunk is one SSE delta from an OpenAI-compatible /chat/completions
// stream. The final chunk (when stream_options.include_usage is set) carries a
// non-nil Usage and usually empty Choices.
type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *compatUsage   `json:"usage"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type streamDelta struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningContent is the model's chain-of-thought, emitted by
	// always-thinking models (e.g. gpt-oss) before the answer. Display-only:
	// callers surface it as live progress but never store or re-send it.
	ReasoningContent string                `json:"reasoning_content"`
	ToolCalls        []StreamToolCallDelta `json:"tool_calls"`
}

// StreamToolCallDelta is a fragment of a tool call. Across chunks the same
// Index accumulates: ID/Name arrive once (or in the first fragment), Arguments
// stream in pieces that must be concatenated in order.
type StreamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// StreamToolCall is a fully reassembled tool call from a stream.
type StreamToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments string
}

// StreamResult is the assembled outcome of a streamed completion: the full
// concatenated content, any reconstructed tool calls, token usage (zero unless
// the server honored stream_options.include_usage), and the finish reason.
type StreamResult struct {
	Content      string
	Reasoning    string // accumulated chain-of-thought (display-only)
	ToolCalls    []StreamToolCall
	Stats        GenerationStats
	FinishReason string
}

// StreamHTTPClient returns an *http.Client suitable for SSE: no whole-request
// Timeout (which would abort long generations mid-stream), with TTFB bounded by
// ResponseHeaderTimeout. Cancel the stream via the request context instead.
func StreamHTTPClient(headerTimeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: tr}
}

// StreamChat POSTs an OpenAI-compatible chat-completions request with
// "stream": true and consumes the SSE response, invoking onContent for each
// content delta and onReasoning for each chain-of-thought delta as they arrive.
// It returns the assembled StreamResult once the stream terminates ([DONE]).
// Non-200 responses reuse the same error parsing as the blocking path
// (wrapServerError), so overflow/backend-down errors surface identically.
// Either callback may be nil (e.g. when only the aggregate is wanted).
func StreamChat(ctx context.Context, hc *http.Client, url, apiKey string, body []byte, onContent, onReasoning func(string)) (StreamResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return StreamResult{}, fmt.Errorf("stream: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	SetAttribution(req.Header)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return StreamResult{}, fmt.Errorf("stream: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bb, _ := io.ReadAll(resp.Body)
		var er compatResponse
		if json.Unmarshal(bb, &er) == nil && er.Error != nil {
			return StreamResult{}, wrapServerError(fmt.Sprintf("stream (%d)", resp.StatusCode), er.Error.fullMessage())
		}
		return StreamResult{}, fmt.Errorf("stream status %d: %s", resp.StatusCode, string(bb))
	}

	var content strings.Builder
	var reasoning strings.Builder
	var tools toolCallAccumulator
	var res StreamResult

	// bufio.Reader (not Scanner) so a single oversized delta line — a big
	// tool-arguments fragment — never trips the 64KB scanner token cap.
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			data, ok := parseSSEData(line)
			if ok {
				if data == "[DONE]" {
					break
				}
				var chunk streamChunk
				if jErr := json.Unmarshal([]byte(data), &chunk); jErr == nil {
					if chunk.Usage != nil {
						res.Stats = GenerationStats{
							InputTokens:  chunk.Usage.PromptTokens,
							OutputTokens: chunk.Usage.CompletionTokens,
							CostUSD:      chunk.Usage.Cost,
						}
					}
					for _, ch := range chunk.Choices {
						if ch.FinishReason != "" {
							res.FinishReason = ch.FinishReason
						}
						if ch.Delta.Content != "" {
							content.WriteString(ch.Delta.Content)
							if onContent != nil {
								onContent(ch.Delta.Content)
							}
						}
						if ch.Delta.ReasoningContent != "" {
							reasoning.WriteString(ch.Delta.ReasoningContent)
							if onReasoning != nil {
								onReasoning(ch.Delta.ReasoningContent)
							}
						}
						tools.add(ch.Delta.ToolCalls)
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return StreamResult{}, fmt.Errorf("stream: read: %w", err)
		}
	}

	res.Content = content.String()
	res.Reasoning = reasoning.String()
	res.ToolCalls = tools.result()
	return res, nil
}

// parseSSEData extracts the payload of a `data:` SSE line, reporting false for
// blank lines, comments (`:`), and non-data event fields.
func parseSSEData(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}

// toolCallAccumulator reassembles streamed tool-call fragments. Fragments are
// keyed by Index; ID/Type/Name take the first non-empty value seen and
// Arguments concatenate in arrival order. Order of first appearance is
// preserved for the final slice.
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*StreamToolCall
}

func (a *toolCallAccumulator) add(deltas []StreamToolCallDelta) {
	for _, d := range deltas {
		if a.byIdx == nil {
			a.byIdx = map[int]*StreamToolCall{}
		}
		tc, ok := a.byIdx[d.Index]
		if !ok {
			tc = &StreamToolCall{}
			a.byIdx[d.Index] = tc
			a.order = append(a.order, d.Index)
		}
		if d.ID != "" {
			tc.ID = d.ID
		}
		if d.Type != "" {
			tc.Type = d.Type
		}
		if d.Function.Name != "" {
			tc.Name = d.Function.Name
		}
		tc.Arguments += d.Function.Arguments
	}
}

func (a *toolCallAccumulator) result() []StreamToolCall {
	if len(a.order) == 0 {
		return nil
	}
	out := make([]StreamToolCall, 0, len(a.order))
	for _, idx := range a.order {
		out = append(out, *a.byIdx[idx])
	}
	return out
}
