package decide

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Completer is the one model round-trip the chat backend needs: a system +
// user prompt in, the reply's content and (when the backend sends one) its
// separate reasoning text out. llm.OpenAICompatClient satisfies it; tests
// pass a func.
type Completer interface {
	GenerateWithSystemReasoning(ctx context.Context, prompt, system string) (content, reasoning string, err error)
}

// CompleterFunc adapts a function to Completer.
type CompleterFunc func(ctx context.Context, prompt, system string) (string, string, error)

// GenerateWithSystemReasoning implements Completer.
func (f CompleterFunc) GenerateWithSystemReasoning(ctx context.Context, prompt, system string) (string, string, error) {
	return f(ctx, prompt, system)
}

// ChatConfig configures the chat backend: any OpenAI-compatible endpoint
// (OpenRouter, a hosted provider, a local server) asked for strict JSON.
type ChatConfig struct {
	Endpoint string // base URL; "/v1" is appended when missing
	APIKey   string
	Model    string
	// Timeout bounds one decision. Zero means 30s. A decision that is late is
	// worth nothing to a caller that never waits, so this stays short.
	Timeout time.Duration
	// MaxCalls caps how many model calls this decider makes in its lifetime
	// (a session, or one replay task). Zero means unlimited. Over budget, a
	// decision returns StatusBudget without a call.
	MaxCalls int
	// MaxTokens caps the reply. Zero means 4096 — room for a reasoning model
	// to think before a one-line JSON answer (2048 cut finalize checks off
	// mid-reasoning on the first replay probe).
	MaxTokens int
}

// Chat is the chat-model Decider.
type Chat struct {
	c        Completer
	model    string
	timeout  time.Duration
	maxCalls int64
	calls    atomic.Int64
}

// NewChat builds a chat Decider over an OpenAI-compatible endpoint.
func NewChat(cfg ChatConfig) *Chat {
	base := strings.TrimRight(cfg.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	maxTok := cfg.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	p := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:    "decide",
		BaseURL: base,
		APIKey:  cfg.APIKey,
		Timeout: timeoutOr(cfg.Timeout),
	})
	p.SetModel(cfg.Model)
	p.SetTemperature(0)
	p.SetMaxTokens(maxTok)
	return NewChatWith(p, cfg)
}

// NewChatWith builds a chat Decider over any Completer (tests, other clients).
func NewChatWith(c Completer, cfg ChatConfig) *Chat {
	return &Chat{c: c, model: cfg.Model, timeout: timeoutOr(cfg.Timeout), maxCalls: int64(cfg.MaxCalls)}
}

func timeoutOr(d time.Duration) time.Duration {
	if d <= 0 {
		return 30 * time.Second
	}
	return d
}

// Name implements Decider.
func (d *Chat) Name() string { return "chat" }

// Calls reports how many model calls this decider has made.
func (d *Chat) Calls() int { return int(d.calls.Load()) }

// Decide implements Decider. It never returns an error: every failure is a
// Status on the Decision.
func (d *Chat) Decide(ctx context.Context, s Snapshot, asks []Ask) Decision {
	dec := Decision{Backend: d.Name(), Model: d.model, PromptVersion: PromptVersion()}
	if len(asks) == 0 {
		dec.Status, dec.Detail = StatusError, "no questions"
		return dec
	}
	if d.maxCalls > 0 && d.calls.Load() >= d.maxCalls {
		dec.Status = StatusBudget
		return dec
	}
	d.calls.Add(1)
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	start := time.Now()
	content, reasoning, err := d.c.GenerateWithSystemReasoning(ctx, RenderPrompt(s, asks), SystemPrompt)
	dec.Latency = time.Since(start)
	if err != nil {
		dec.Status, dec.Detail = classify(ctx, err), clip(err.Error(), 300)
		return dec
	}
	reply := content
	if strings.TrimSpace(reply) == "" {
		// A reasoning model that put its answer in the reasoning field: take
		// the last object there, still validated against the choices.
		reply = reasoning
	}
	answers, err := ParseAnswers(reply, asks)
	if err != nil {
		dec.Status, dec.Detail = StatusParseError, err.Error()+" | reply: "+clip(reply, 200)
		return dec
	}
	dec.Answers, dec.Status = answers, StatusOK
	return dec
}

// classify maps a call failure to a Status.
func classify(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return StatusTimeout
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate-limit") {
		return StatusRateLimited
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return StatusTimeout
	}
	return StatusError
}

// clip shortens s to n bytes on a rune boundary, marking the cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
