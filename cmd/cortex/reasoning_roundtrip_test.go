// reasoning_roundtrip_test.go — keeping the model's reasoning within a turn
// (context.keep_reasoning). A hybrid-thinking model's chat template (Qwen3
// via llama.cpp --jinja) renders reasoning_content on the assistant messages
// of the CURRENT tool-calling chain; cortex used to drop it every step, so
// the model re-derived its plan each call. These tests lock: capture from
// blocking and streamed responses, the current-turn-only wire scope, the
// kill switch's byte-for-byte old request, the OpenRouter exclusion,
// budgeting (old reasoning shed before any tool result is stubbed), and
// transcript persistence (redacted; dropped when the switch is off).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestReasoningCapturedOntoMessage: both transport paths put the response's
// reasoning on the returned Message (not only on Choice), so appendMsg in
// runLoop carries it into the request log without any per-site plumbing.
func TestReasoningCapturedOntoMessage(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
		body   string
		want   string
	}{
		{
			name: "blocking reasoning_content",
			body: `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"plan: grep first","tool_calls":[{"id":"c1","type":"function","function":{"name":"grep","arguments":"{}"}}]}}]}`,
			want: "plan: grep first",
		},
		{
			name: "blocking reasoning alias",
			body: `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning":"plan B"}}]}`,
			want: "plan B",
		},
		{
			name: "blocking no reasoning",
			body: `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`,
			want: "",
		},
		{
			name:   "stream reasoning_content deltas",
			stream: true,
			body: strings.Join([]string{
				`data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"plan: "}}]}`,
				`data: {"choices":[{"delta":{"reasoning_content":"grep first"}}]}`,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"grep","arguments":"{}"}}]}}]}`,
				`data: [DONE]`, ``,
			}, "\n\n"),
			want: "plan: grep first",
		},
		{
			name:   "stream reasoning alias deltas",
			stream: true,
			body: strings.Join([]string{
				`data: {"choices":[{"delta":{"role":"assistant","reasoning":"plan B"}}]}`,
				`data: {"choices":[{"delta":{"content":"ok"}}]}`,
				`data: [DONE]`, ``,
			}, "\n\n"),
			want: "plan B",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var srv *httptest.Server
			if tt.stream {
				srv = httptest.NewServer(sseHandler(tt.body))
			} else {
				srv = blockingServer(t, tt.body)
			}
			defer srv.Close()
			req := &AgentRequest{Model: "m", BaseURL: srv.URL, Messages: []Message{{Role: RoleSystem, Content: "s"}, {Role: RoleUser, Content: "u"}}}
			var res *AgentResponse
			var err error
			if tt.stream {
				res, err = req.SendStream(context.Background(), nil, nil)
			} else {
				res, err = req.Send(context.Background())
			}
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if got := res.Choices[0].Message.Reasoning; got != tt.want {
				t.Errorf("Message.Reasoning = %q, want %q", got, tt.want)
			}
		})
	}
}

// reasoningLog is a two-turn request log: turn 1 (finished, with reasoning on
// both its assistant messages — e.g. replayed from a resumed transcript) and
// the in-flight turn 2 (one tool step with reasoning, its result, and a
// second tool step with reasoning).
func reasoningLog() []Message {
	call := func(id string) []ToolCall {
		return []ToolCall{{ID: id, Type: "function", Function: FunctionCall{Name: "grep", Arguments: `{"pattern":"x"}`}}}
	}
	return []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "turn one"},
		{Role: "assistant", ToolCalls: call("a1"), Reasoning: "T1-STEP1"},
		{Role: RoleTool, ToolCallID: "a1", Content: "r1"},
		{Role: "assistant", Content: "done one", Reasoning: "T1-FINAL"},
		{Role: RoleUser, Content: "turn two"},
		{Role: "assistant", ToolCalls: call("b1"), Reasoning: "T2-STEP1"},
		{Role: RoleTool, ToolCallID: "b1", Content: "r2"},
		{Role: "assistant", ToolCalls: call("b2"), Reasoning: "T2-STEP2"},
		{Role: RoleTool, ToolCallID: "b2", Content: "r3"},
	}
}

// stripAllReasoning returns a copy of msgs with every Reasoning cleared — the
// log the pre-keep-reasoning cortex would have held.
func stripAllReasoning(msgs []Message) []Message {
	out := append([]Message(nil), msgs...)
	for i := range out {
		out[i].Reasoning = ""
	}
	return out
}

// captureBody sends req (blocking or streamed) to a server that records the
// raw request body, and returns it.
func captureBody(t *testing.T, req *AgentRequest, stream bool) []byte {
	t.Helper()
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte(okResponse))
	}))
	defer srv.Close()
	req.BaseURL = srv.URL
	var err error
	if stream {
		_, err = req.SendStream(context.Background(), nil, nil)
	} else {
		_, err = req.Send(context.Background())
	}
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return got
}

// wireReasoning decodes a request body and returns each message's
// reasoning_content ("" when absent).
func wireReasoning(t *testing.T, body []byte) []string {
	t.Helper()
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	out := make([]string, len(payload.Messages))
	for i, m := range payload.Messages {
		if s, ok := m["reasoning_content"].(string); ok {
			out[i] = s
		}
	}
	return out
}

// TestWireReasoningScopedToCurrentTurn: only the current turn's assistant
// messages (after the last user message) carry reasoning_content; turn 1's
// reasoning stays in the log but never rides the wire. Same scope on the
// streamed path, and with the two-zone outline/ephemeral inserts in place.
func TestWireReasoningScopedToCurrentTurn(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
		mutate func(r *AgentRequest)
		want   []string // the non-empty reasoning_content values, in wire order
	}{
		{name: "blocking", want: []string{"T2-STEP1", "T2-STEP2"}},
		{name: "stream", stream: true, want: []string{"T2-STEP1", "T2-STEP2"}},
		{
			name: "with outline + ephemeral inserts and a demoted turn 1",
			mutate: func(r *AgentRequest) {
				r.OutlineBlock = "outline"
				r.EphemeralSystem = "memory index"
				r.PrefixEnd = 1
				r.TailFrom = 5 // turn 1 demoted
			},
			want: []string{"T2-STEP1", "T2-STEP2"},
		},
		{
			name: "a harness nudge mid-turn moves the boundary (template semantics)",
			mutate: func(r *AgentRequest) {
				r.Messages = append(r.Messages, Message{Role: RoleUser, Content: "nudge"},
					Message{Role: "assistant", Content: "final", Reasoning: "T2-AFTER-NUDGE"})
			},
			want: []string{"T2-AFTER-NUDGE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AgentRequest{Model: "m", Messages: reasoningLog()}
			if tt.mutate != nil {
				tt.mutate(req)
			}
			before := append([]Message(nil), req.Messages...)
			var got []string
			for _, r := range wireReasoning(t, captureBody(t, req, tt.stream)) {
				if r != "" {
					got = append(got, r)
				}
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("wire reasoning = %v, want %v", got, tt.want)
			}
			// The stored log is never mutated by wire assembly.
			if !messagesEqual(req.Messages, before) {
				t.Error("Send mutated the stored Messages' reasoning")
			}
		})
	}
}

// TestWireReasoningOffIsByteForByteOld: with the kill switch on (or on the
// OpenRouter dialect) a log full of reasoning produces EXACTLY the request
// body the same log without any reasoning produces — the pre-feature request.
// And a log with no reasoning at all is unchanged by the feature being on.
func TestWireReasoningOffIsByteForByteOld(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
		setup  func(r *AgentRequest)
	}{
		{name: "kill switch, blocking", setup: func(r *AgentRequest) { r.OmitReasoning = true }},
		{name: "kill switch, stream", stream: true, setup: func(r *AgentRequest) { r.OmitReasoning = true }},
		{name: "openrouter dialect", setup: func(r *AgentRequest) {
			applyEffort(r, llm.DialectOpenRouter, llm.Effort{})
		}},
		{name: "openrouter dialect, anthropic cache-breakpoint marshal path", setup: func(r *AgentRequest) {
			r.Model = "anthropic/claude"
			applyEffort(r, llm.DialectOpenRouter, llm.Effort{})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			with := &AgentRequest{Model: "m", Messages: reasoningLog()}
			tt.setup(with)
			without := &AgentRequest{Model: with.Model, Messages: stripAllReasoning(reasoningLog())}
			tt.setup(without)
			got := captureBody(t, with, tt.stream)
			want := captureBody(t, without, tt.stream)
			if !bytes.Equal(got, want) {
				t.Errorf("request with reasoning off differs from the no-reasoning request:\n got %s\nwant %s", got, want)
			}
			if bytes.Contains(got, []byte("reasoning_content")) {
				t.Errorf("reasoning_content on the wire with the round-trip off: %s", got)
			}
		})
	}

	t.Run("no reasoning anywhere: feature on changes nothing", func(t *testing.T) {
		on := &AgentRequest{Model: "m", Messages: stripAllReasoning(reasoningLog())}
		off := &AgentRequest{Model: "m", Messages: stripAllReasoning(reasoningLog()), OmitReasoning: true}
		if got, want := captureBody(t, on, false), captureBody(t, off, false); !bytes.Equal(got, want) {
			t.Errorf("no-reasoning request differs with the feature on:\n got %s\nwant %s", got, want)
		}
	})
}

// TestWireReasoningShape pins the exact wire shape of a current-turn
// assistant tool-call message carrying reasoning.
func TestWireReasoningShape(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
		{Role: "assistant", Reasoning: "need the file first", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "package a"},
	}}
	wire := req.wireMessages()
	got, err := json.Marshal(&wire[2])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}],"reasoning_content":"need the file first"}`
	if string(got) != want {
		t.Errorf("wire shape:\n got %s\nwant %s", got, want)
	}
}

// TestSentReasoningTokens: the estimate counts only reasoning that will be
// sent — current turn, round-trip on.
func TestSentReasoningTokens(t *testing.T) {
	on := &AgentRequest{Messages: reasoningLog()}
	want := cache.TokensOf(len("T2-STEP1") + len("T2-STEP2"))
	if got := on.sentReasoningTokens(); got != want {
		t.Errorf("sentReasoningTokens = %d, want %d (turn 2 only)", got, want)
	}
	off := &AgentRequest{Messages: reasoningLog(), OmitReasoning: true}
	if got := off.sentReasoningTokens(); got != 0 {
		t.Errorf("sentReasoningTokens with kill switch = %d, want 0", got)
	}
}

// TestApplyInTurnDemotionShedsReasoningFirst: over budget, the in-turn policy
// drops the current turn's OLDER reasoning before stubbing any tool result;
// the newest reasoning always survives. When shedding is not enough, tool
// results are stubbed on top. Prior-turn reasoning (never sent) is neither
// counted nor touched.
func TestApplyInTurnDemotionShedsReasoningFirst(t *testing.T) {
	// window 120000 → highWM 60000, lowWM 40000 tokens (×4 chars).
	tests := []struct {
		name          string
		steps         int
		reasoningSize int
		resultSize    int
		wantStubs     bool
		omit          bool
	}{
		// 10 steps × 28000 chars reasoning = 70000 tokens; results tiny.
		// Shedding old reasoning alone drains under lowWM → no stubs.
		{name: "reasoning alone over budget: shed, no stubs", steps: 10, reasoningSize: 28000, resultSize: 400, wantStubs: false},
		// 20 steps: reasoning 20×32000 chars = 160000 tok, results 20×12000
		// chars = 60000 tok. Even with all 19 old reasoning traces shed, the
		// newest (8000) + results (60000) stay over → stubs follow the shed.
		{name: "shed is not enough: shed all old, then stub", steps: 20, reasoningSize: 32000, resultSize: 12000, wantStubs: true},
		// Kill switch: reasoning is never sent, so it is neither counted nor
		// shed — 70000 tokens of unsent reasoning keeps the prompt under budget.
		{name: "kill switch: unsent reasoning not counted or shed", steps: 10, reasoningSize: 28000, resultSize: 400, wantStubs: false, omit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := indemoteSession(120000)
			cs.Request.OmitReasoning = tt.omit
			// A finished prior turn with reasoning that is never sent.
			cs.Request.Messages = append(cs.Request.Messages,
				Message{Role: RoleUser, Content: "prior"},
				Message{Role: "assistant", Content: "prior answer", Reasoning: strings.Repeat("p", 50000)})
			cs.Request.Messages = append(cs.Request.Messages, Message{Role: RoleUser, Content: "go"})
			turnStart := len(cs.Request.Messages) - 1
			for i := 0; i < tt.steps; i++ {
				a, r := indemoteToolResult(i, tt.resultSize)
				a.Reasoning = strings.Repeat("r", tt.reasoningSize)
				cs.Request.Messages = append(cs.Request.Messages, a, r)
			}
			cs.applyInTurnDemotion(cs.Request, turnStart)

			var kept, shed, stubs int
			lastAssistant := -1
			for i := turnStart; i < len(cs.Request.Messages); i++ {
				m := cs.Request.Messages[i]
				if m.Role == "assistant" {
					lastAssistant = i
					if m.Reasoning == "" {
						shed++
					} else {
						kept++
					}
				}
				if m.Role == RoleTool && strings.HasPrefix(m.Content, cache.InTurnStubPrefix) {
					stubs++
				}
			}
			if cs.Request.Messages[2].Reasoning == "" {
				t.Error("prior-turn reasoning was shed; it is never sent and must not be touched")
			}
			if tt.omit {
				if shed != 0 || stubs != 0 {
					t.Errorf("kill switch: shed=%d stubs=%d, want 0/0", shed, stubs)
				}
				return
			}
			if shed == 0 {
				t.Fatal("over budget but no reasoning was shed")
			}
			if cs.Request.Messages[lastAssistant].Reasoning == "" {
				t.Error("the newest reasoning was shed; it must always survive")
			}
			if (stubs > 0) != tt.wantStubs {
				t.Errorf("stubs = %d, wantStubs = %v (shed=%d kept=%d)", stubs, tt.wantStubs, shed, kept)
			}
			if tt.wantStubs && kept != 1 {
				t.Errorf("stubbing fired with %d reasoning still kept; every old reasoning must be shed first", kept)
			}
		})
	}
}

// TestTranscriptReasoningPersistence: reasoning persists as its own field,
// redacted like Content; the kill switch drops it entirely.
func TestTranscriptReasoningPersistence(t *testing.T) {
	const secret = "sk-or-v1-0123456789abcdef0123456789abcdef"
	off := false
	tests := []struct {
		name    string
		cfg     *Config
		wantHas bool
	}{
		{name: "default on: persisted, redacted", cfg: nil, wantHas: true},
		{name: "kill switch: dropped", cfg: &Config{Context: ContextConfig{KeepReasoning: &off}}, wantHas: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &CortexSession{Config: tt.cfg}
			m := Message{Role: "assistant", Content: "ok", Reasoning: "the key is " + secret}
			r := cs.redactedMessage(m)
			if strings.Contains(r.Reasoning, secret) {
				t.Errorf("persisted reasoning leaked the secret: %q", r.Reasoning)
			}
			if (r.Reasoning != "") != tt.wantHas {
				t.Errorf("persisted reasoning = %q, wantHas %v", r.Reasoning, tt.wantHas)
			}
			if tt.wantHas && cs.redactions == 0 {
				t.Error("reasoning redaction not counted")
			}
			if m.Reasoning != "the key is "+secret {
				t.Error("redactedMessage mutated the live message")
			}
			b, _ := json.Marshal(r)
			if tt.wantHas != bytes.Contains(b, []byte(`"reasoning_content"`)) {
				t.Errorf("transcript line = %s, want reasoning_content present=%v", b, tt.wantHas)
			}
		})
	}
}

// TestKeepReasoningConfig: absent = enabled (availability kill switch), and
// the project layer overrides field-by-field.
func TestKeepReasoningConfig(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{name: "nil config", cfg: nil, want: true},
		{name: "absent key", cfg: &Config{}, want: true},
		{name: "explicit true", cfg: &Config{Context: ContextConfig{KeepReasoning: &on}}, want: true},
		{name: "explicit false", cfg: &Config{Context: ContextConfig{KeepReasoning: &off}}, want: false},
		{name: "project false overrides user true", cfg: &Config{Context: mergeContext(ContextConfig{KeepReasoning: &on}, ContextConfig{KeepReasoning: &off})}, want: false},
		{name: "project unset keeps user false", cfg: &Config{Context: mergeContext(ContextConfig{KeepReasoning: &off}, ContextConfig{})}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.keepReasoningEnabled(); got != tt.want {
				t.Errorf("keepReasoningEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
