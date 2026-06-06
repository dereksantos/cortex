// Package llm — generic OpenAI-compatible chat-completions provider.
//
// Used for any endpoint speaking the OpenAI HTTP shape: local Lemonade
// (chatterbox), LM Studio, vLLM, sglang, Together-like proxies, etc.
// OpenRouter has its own provider (openrouter.go) because it carries
// extra request hints (HTTP-Referer, X-Title) and surfaces per-call
// cost in a non-standard `usage.cost` field; the generic client here
// stays minimal and assumes nothing beyond the OpenAI baseline.
//
// Phase 4 model-registry substrate (see ROADMAP.md). One instance per
// configured endpoint; the model + role wiring lives a layer up.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultCompatTimeoutSec caps the HTTP request to an OpenAI-compat
// endpoint. Local inference servers behind this client (Lemonade,
// llama.cpp, vLLM) can take a long time on large-context calls —
// 30B-class models in particular routinely exceed a one-minute ceiling
// for a single forward pass over ~10K input tokens. Override via the
// CORTEX_COMPAT_TIMEOUT_SEC env var for slower hardware.
const defaultCompatTimeoutSec = 300

// compatTimeout returns the per-request HTTP timeout, honoring
// CORTEX_COMPAT_TIMEOUT_SEC when set to a positive integer.
func compatTimeout() time.Duration {
	if v := os.Getenv("CORTEX_COMPAT_TIMEOUT_SEC"); v != "" {
		var secs int
		if _, err := fmt.Sscanf(v, "%d", &secs); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultCompatTimeoutSec * time.Second
}

// EndpointConfig identifies one OpenAI-compatible endpoint. Name is a
// short stable identifier ("chatterbox", "lm-studio-local") used in
// telemetry and config; BaseURL is the OpenAI root (e.g.
// "http://localhost:13305/v1"); APIKey is optional — many local
// endpoints accept any string or none at all.
type EndpointConfig struct {
	Name    string
	BaseURL string
	APIKey  string
}

// CompatModel is one entry from an endpoint's /models listing.
// Labels are taken verbatim from the endpoint when it exposes them
// (Lemonade does; OpenAI's own API does not). Callers that need
// capability inference for label-less endpoints should layer that on
// top — see pkg/models for the static fallback table.
type CompatModel struct {
	ID            string
	Labels        []string
	ContextLength int
}

// OpenAICompatClient is a Provider for a single endpoint. The model is
// per-client (SetModel to swap); callers wiring multiple roles to
// different models on the same endpoint should construct one client
// per role, or call SetModel between role boundaries.
type OpenAICompatClient struct {
	name       string
	baseURL    string // OpenAI root, e.g. "http://localhost:13305/v1"
	apiKey     string // may be empty for endpoints that don't require auth
	model      string
	maxTokens  int
	httpClient *http.Client

	// temperature, when non-nil, is sent on every request. Initialized
	// from CORTEX_TEMPERATURE at construction (nil when unset → no
	// temperature sent, so the backend default applies — today's
	// behavior). SetTemperature overrides it for in-process callers.
	temperature *float64

	// swapTracker, when set, gets a Note(endpoint, model) after every
	// successful request — feeds the Phase 4 Slice E swap-aware
	// routing substrate. Nil is fine (no tracking).
	swapTracker *SwapTracker
}

// SetSwapTracker wires a shared tracker so this client reports its
// model-per-endpoint usage. Nil clears the wiring.
func (c *OpenAICompatClient) SetSwapTracker(t *SwapTracker) { c.swapTracker = t }

// NewOpenAICompatClient constructs a client bound to one endpoint.
// Pass the endpoint's OpenAI root URL (no trailing /chat/completions);
// the client appends paths as needed.
func NewOpenAICompatClient(ep EndpointConfig) *OpenAICompatClient {
	name := ep.Name
	if name == "" {
		name = "openai-compat"
	}
	return &OpenAICompatClient{
		name:        name,
		baseURL:     strings.TrimRight(ep.BaseURL, "/"),
		apiKey:      ep.APIKey,
		maxTokens:   defaultMaxTokens,
		temperature: envTemperature(),
		httpClient: &http.Client{
			Timeout: compatTimeout(),
		},
	}
}

// SetTemperature pins this client's sampling temperature, overriding the
// CORTEX_TEMPERATURE env default read at construction. Sent on every
// subsequent request.
func (c *OpenAICompatClient) SetTemperature(t float64) { c.temperature = &t }

// Name returns the configured endpoint identifier. Distinct from
// "openai-compat" so telemetry can attribute calls to specific
// endpoints (chatterbox vs lm-studio vs vllm-cluster-1).
func (c *OpenAICompatClient) Name() string { return c.name }

// IsAvailable returns true when the client has enough configuration to
// attempt a call. Does NOT probe the network — that's detect.go's job.
// A configured baseURL is sufficient; apiKey is optional per endpoint.
func (c *OpenAICompatClient) IsAvailable() bool { return c.baseURL != "" }

// SetModel picks the model used for subsequent calls. Pass the
// endpoint's model ID verbatim (e.g. "Qwen3-Coder-30B-A3B-Instruct-GGUF"
// for chatterbox, "qwen2.5-coder:3b" for an ollama-shim endpoint).
func (c *OpenAICompatClient) SetModel(m string) { c.model = m }

// Model returns the currently selected model.
func (c *OpenAICompatClient) Model() string { return c.model }

// SetMaxTokens overrides the default response token cap.
func (c *OpenAICompatClient) SetMaxTokens(n int) { c.maxTokens = n }

// BaseURL returns the configured root URL (for telemetry/debug).
func (c *OpenAICompatClient) BaseURL() string { return c.baseURL }

// LastCostUSD is always 0 for OpenAI-compatible endpoints. Local
// inference (chatterbox, LM Studio, vLLM) doesn't bill per token;
// hosted proxies that do can be wrapped with their own cost-tracking
// provider type. Satisfies the harness.LoopProvider interface so the
// agent loop can ignore cost accounting on local-only sessions.
func (c *OpenAICompatClient) LastCostUSD() float64 { return 0 }

// Generate produces a response for the given prompt.
func (c *OpenAICompatClient) Generate(ctx context.Context, prompt string) (string, error) {
	out, _, err := c.generate(ctx, prompt, "")
	return out, err
}

// GenerateWithSystem prepends a system-role message before the user prompt.
func (c *OpenAICompatClient) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	out, _, err := c.generate(ctx, prompt, system)
	return out, err
}

// GenerateWithStats returns the response plus token usage. Local
// endpoints typically don't expose cost (it's free), so callers
// computing dollars should multiply tokens by their out-of-band
// pricing model rather than expecting a server-side `usage.cost`.
func (c *OpenAICompatClient) GenerateWithStats(ctx context.Context, prompt string) (string, GenerationStats, error) {
	return c.generate(ctx, prompt, "")
}

// compatRequest is the standard OpenAI chat-completions body. No
// vendor extensions — the point of this client is universal
// compatibility.
type compatRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Messages    []compatMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
}

type compatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type compatResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []compatChoice `json:"choices"`
	Usage   compatUsage    `json:"usage"`
	Error   *compatErr     `json:"error,omitempty"`
}

type compatChoice struct {
	Message      compatMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type compatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type compatErr struct {
	Message string         `json:"message"`
	Type    string         `json:"type"`
	Code    string         `json:"code"`
	Details *compatErrDeep `json:"details,omitempty"`
}

// compatErrDeep is the nested error envelope lemonade-server wraps
// underlying llama-server failures in. The wire shape is:
//
//	{"error": {
//	   "message": "llama-server request failed",     // wrapper
//	   "details": {"response": {"error": {"message": "request (4921 tokens) exceeds the available context size (4096 tokens) ..."}}}
//	 }}
//
// The wrapper message is useless on its own ("llama-server request failed"
// tells the user nothing actionable). Unwrap returns the deepest available
// message so the REPL surfaces what actually went wrong.
type compatErrDeep struct {
	Response *struct {
		Error *struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
			Type    string `json:"type"`
		} `json:"error,omitempty"`
	} `json:"response,omitempty"`
	StatusCode int `json:"status_code,omitempty"`
}

// wrapServerError converts an OpenAI-compat server-side error payload
// into a Go error. Three cases:
//
//   - Context overflow (request exceeds the model's window) → typed
//     ContextOverflowError so the harness loop can catch via errors.As
//     and retry with a smaller payload.
//   - Backend-unreachable (lemonade returns "Network error: CURL error:
//     Could not connect to server" when llama-server is down/restarting)
//     → a clean single-line message naming the actual cause. The default
//     wrapper produced "<endpoint>: server error: Network error: CURL
//     error: Could not connect to server" — three error words from
//     three layers, none actionable.
//   - Anything else → "<endpoint>: server error: <msg>" preserved.
func wrapServerError(prefix, msg string) error {
	if overflow, ok := ParseContextOverflow(msg); ok {
		overflow.Message = prefix + ": server error: " + msg
		return overflow
	}
	if isBackendUnreachableMessage(msg) {
		return fmt.Errorf("%s: inference backend unreachable (server is up but its model backend isn't responding — try restarting the inference server)", prefix)
	}
	return fmt.Errorf("%s: server error: %s", prefix, msg)
}

// isBackendUnreachableMessage matches the wire-shape lemonade emits
// when its underlying llama-server (or other backend) is down. The
// frontend itself responds 200 OK with this error body, so we detect
// it from the message contents.
func isBackendUnreachableMessage(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "could not connect to server") ||
		strings.Contains(low, "connection refused") ||
		(strings.Contains(low, "network error") && strings.Contains(low, "curl error"))
}

// fullMessage returns the most actionable error string available: the
// nested llama-server message when present, otherwise the wrapper
// message. Keeps both visible when distinct so the user sees the
// wrapper-AND-cause pair (e.g. "llama-server request failed: request
// (4921 tokens) exceeds the available context size (4096 tokens)").
func (e *compatErr) fullMessage() string {
	if e == nil {
		return ""
	}
	if e.Details != nil && e.Details.Response != nil && e.Details.Response.Error != nil {
		inner := e.Details.Response.Error.Message
		if inner != "" && inner != e.Message {
			if e.Message == "" {
				return inner
			}
			return e.Message + ": " + inner
		}
	}
	return e.Message
}

func (c *OpenAICompatClient) generate(ctx context.Context, prompt, system string) (string, GenerationStats, error) {
	if c.model == "" {
		return "", GenerationStats{}, fmt.Errorf("%s: model not set (use SetModel before generating)", c.name)
	}

	msgs := make([]compatMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, compatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, compatMessage{Role: "user", Content: prompt})

	body := compatRequest{
		Model:       c.model,
		MaxTokens:   c.maxTokens,
		Messages:    msgs,
		Temperature: c.temperature,
	}

	raw, err := c.doRaw(ctx, "/chat/completions", body)
	if err != nil {
		return "", GenerationStats{}, err
	}

	var apiResp compatResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return "", GenerationStats{}, fmt.Errorf("%s: decode response: %w", c.name, err)
	}
	if apiResp.Error != nil {
		return "", GenerationStats{}, wrapServerError(c.name, apiResp.Error.fullMessage())
	}
	if len(apiResp.Choices) == 0 {
		return "", GenerationStats{}, fmt.Errorf("%s: response had no choices", c.name)
	}

	stats := GenerationStats{
		InputTokens:  apiResp.Usage.PromptTokens,
		OutputTokens: apiResp.Usage.CompletionTokens,
	}
	return apiResp.Choices[0].Message.Content, stats, nil
}

// doRaw POSTs JSON to baseURL+path. Authorization is only set when
// apiKey is non-empty — required by some endpoints (OpenAI proper,
// hosted proxies), ignored by most local ones (Lemonade, LM Studio).
func (c *OpenAICompatClient) doRaw(ctx context.Context, path string, body any) ([]byte, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("%s: baseURL not configured", c.name)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", c.name, err)
	}

	url := c.baseURL + path
	if os.Getenv("CORTEX_LLM_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[%s] POST %s\n", c.name, url)
		fmt.Fprintf(os.Stderr, "[%s] >>> %s\n", c.name, string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: new request: %w", c.name, err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	defer resp.Body.Close()

	bb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", c.name, err)
	}

	if os.Getenv("CORTEX_LLM_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[%s] <<< (%d) %s\n", c.name, resp.StatusCode, string(bb))
	}

	if resp.StatusCode != http.StatusOK {
		var er compatResponse
		if json.Unmarshal(bb, &er) == nil && er.Error != nil {
			return nil, wrapServerError(fmt.Sprintf("%s (%d)", c.name, resp.StatusCode), er.Error.fullMessage())
		}
		return nil, fmt.Errorf("%s status %d: %s", c.name, resp.StatusCode, string(bb))
	}

	// Note the model that just served on this endpoint. Both plaintext
	// and tool-call paths route through doRaw so this catches every
	// model-use event in one place.
	if c.swapTracker != nil && c.model != "" {
		c.swapTracker.Note(c.name, c.model)
	}
	return bb, nil
}

// ListModels fetches the endpoint's /models catalog. The OpenAI shape
// is `{"data":[{"id":...}]}`; endpoints that expose capability labels
// (Lemonade does) add a "labels" string array and "max_context_window"
// int. We accept both shapes via tolerant JSON parsing — fields the
// endpoint doesn't expose stay zero/nil.
func (c *OpenAICompatClient) ListModels(ctx context.Context) ([]CompatModel, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("%s: baseURL not configured", c.name)
	}

	url := c.baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%s models: new request: %w", c.name, err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s models: request: %w", c.name, err)
	}
	defer resp.Body.Close()

	bb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s models: read response: %w", c.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s models: status %d: %s", c.name, resp.StatusCode, string(bb))
	}

	var payload struct {
		Data []struct {
			ID               string   `json:"id"`
			Labels           []string `json:"labels"`
			MaxContextWindow int      `json:"max_context_window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bb, &payload); err != nil {
		return nil, fmt.Errorf("%s models: decode: %w", c.name, err)
	}

	out := make([]CompatModel, 0, len(payload.Data))
	for _, m := range payload.Data {
		out = append(out, CompatModel{
			ID:            m.ID,
			Labels:        m.Labels,
			ContextLength: m.MaxContextWindow,
		})
	}
	return out, nil
}
