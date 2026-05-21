package llm

import (
	"errors"
	"fmt"
	"testing"
)

func TestParseContextOverflow(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantOK    bool
		wantReq   int
		wantAvail int
	}{
		{
			name:      "lemonade wrapped llama-server",
			msg:       "chatterbox: server error: llama-server request failed: request (4946 tokens) exceeds the available context size (4096 tokens), try increasing it",
			wantOK:    true,
			wantReq:   4946,
			wantAvail: 4096,
		},
		{
			name:      "singular token form",
			msg:       "request (1 token) exceeds the available context size (1 token)",
			wantOK:    true,
			wantReq:   1,
			wantAvail: 1,
		},
		{
			name:   "no match",
			msg:    "rate limit exceeded",
			wantOK: false,
		},
		{
			name:   "empty",
			msg:    "",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseContextOverflow(tc.msg)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (msg=%q)", ok, tc.wantOK, tc.msg)
			}
			if !ok {
				return
			}
			if got.RequestedTokens != tc.wantReq {
				t.Errorf("RequestedTokens = %d, want %d", got.RequestedTokens, tc.wantReq)
			}
			if got.AvailableTokens != tc.wantAvail {
				t.Errorf("AvailableTokens = %d, want %d", got.AvailableTokens, tc.wantAvail)
			}
		})
	}
}

func TestAsContextOverflow_UnwrapsFmtErrorf(t *testing.T) {
	// Simulate the typical wrapping pattern: a typed ContextOverflowError
	// returned by the provider, then wrapped by the harness loop with
	// fmt.Errorf and %w.
	base := &ContextOverflowError{
		Message:         "server error: request (8000 tokens) exceeds the available context size (4096 tokens)",
		AvailableTokens: 4096,
		RequestedTokens: 8000,
	}
	wrapped := fmt.Errorf("turn 3: %w", base)
	got, ok := AsContextOverflow(wrapped)
	if !ok {
		t.Fatal("AsContextOverflow should match the wrapped typed error")
	}
	if got.AvailableTokens != 4096 {
		t.Errorf("AvailableTokens = %d, want 4096", got.AvailableTokens)
	}
}

func TestAsContextOverflow_StringFallback(t *testing.T) {
	// The error chain has no typed ContextOverflowError, but the
	// formatted message carries the signature — the string fallback
	// must still catch it.
	err := errors.New("chatterbox (400): server error: llama-server request failed: request (5012 tokens) exceeds the available context size (4096 tokens)")
	got, ok := AsContextOverflow(err)
	if !ok {
		t.Fatal("string-based fallback should match the signature")
	}
	if got.RequestedTokens != 5012 || got.AvailableTokens != 4096 {
		t.Errorf("got req=%d avail=%d, want 5012 / 4096", got.RequestedTokens, got.AvailableTokens)
	}
}

func TestAsContextOverflow_NilAndUnrelated(t *testing.T) {
	if _, ok := AsContextOverflow(nil); ok {
		t.Error("nil error should not match")
	}
	if _, ok := AsContextOverflow(errors.New("connection refused")); ok {
		t.Error("unrelated error should not match")
	}
}
