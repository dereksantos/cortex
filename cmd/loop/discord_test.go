package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// chunkMessage feeds Discord's 2000-char limit, so every chunk must stay within
// max, break on the nicest available boundary, and lose no content except the
// single separator a break lands on.
func TestChunkMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want []string
	}{
		{"empty", "", 10, nil},
		{"under limit", "hello", 10, []string{"hello"}},
		{"exactly limit", "helloworld", 10, []string{"helloworld"}},
		{"breaks on newline", "line one\nline two", 10, []string{"line one", "line two"}},
		{"breaks on last space within window", "alpha beta gamma", 10, []string{"alpha", "beta gamma"}},
		{"hard cut unbroken run", "aaaaaaaaaaaaa", 5, []string{"aaaaa", "aaaaa", "aaa"}},
		{"breaks at last newline within window", "a\nb\nc\nd", 5, []string{"a\nb", "c\nd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chunkMessage(tt.in, tt.max)
			if len(got) != len(tt.want) {
				t.Fatalf("chunkMessage(%q,%d) = %v (%d chunks), want %v (%d)", tt.in, tt.max, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("chunk %d = %q, want %q", i, got[i], tt.want[i])
				}
				if len(got[i]) > tt.max {
					t.Errorf("chunk %d length %d exceeds max %d", i, len(got[i]), tt.max)
				}
			}
		})
	}
}

// No chunk may ever exceed the limit, whatever the input — the property that
// actually keeps Discord from rejecting a send.
func TestChunkMessageNeverExceedsMax(t *testing.T) {
	inputs := []string{
		strings.Repeat("x", 5000),
		strings.Repeat("word ", 1000),
		strings.Repeat("line\n", 800),
	}
	const max = 1990
	for _, in := range inputs {
		for i, c := range chunkMessage(in, max) {
			if len(c) > max {
				t.Errorf("input len %d: chunk %d length %d exceeds max %d", len(in), i, len(c), max)
			}
		}
	}
}

// parseBotCommand distinguishes manual overrides from ordinary messages by the
// first token, and carries the change name for !new.
func TestParseBotCommand(t *testing.T) {
	tests := []struct {
		in       string
		wantKind string
		wantArg  string
	}{
		{"!status", "status", ""},
		{"!continue", "continue", ""},
		{"!new add-metrics", "new", "add-metrics"},
		{"!new   the auth bug  ", "new", "the auth bug"},
		{"!new", "new", ""},
		{"fix the login bug", "", "fix the login bug"},
		{"", "", ""},
		{"!newish thing", "", "!newish thing"}, // not !new — must not match as a command
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			kind, arg := parseBotCommand(tt.in)
			if kind != tt.wantKind || arg != tt.wantArg {
				t.Errorf("parseBotCommand(%q) = (%q, %q), want (%q, %q)", tt.in, kind, arg, tt.wantKind, tt.wantArg)
			}
		})
	}
}

// parseRouteDecision must lift the JSON out of any surrounding prose, normalize
// an unrecognized label to "continue" (with zeroed confidence so it can never
// trip the new_change threshold), clamp confidence to [0,1], and report ok=false
// only when there is no parseable object at all.
func TestParseRouteDecision(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantOK   bool
		wantDec  string
		wantConf float64
		wantName string
	}{
		{"plain new_change", `{"decision":"new_change","name":"add-auth","confidence":0.9,"why":"x"}`, true, "new_change", 0.9, "add-auth"},
		{"plain continue", `{"decision":"continue","name":"","confidence":0.4,"why":"x"}`, true, "continue", 0.4, ""},
		{"prose wrapped", "Sure!\n{\"decision\":\"new_change\",\"confidence\":0.8}\nhope that helps", true, "new_change", 0.8, ""},
		{"uppercase label", `{"decision":"NEW_CHANGE","confidence":0.85}`, true, "new_change", 0.85, ""},
		{"unknown label → continue, conf zeroed", `{"decision":"maybe","confidence":0.99}`, true, "continue", 0, ""},
		{"confidence clamped high", `{"decision":"new_change","confidence":1.7}`, true, "new_change", 1, ""},
		{"confidence clamped low", `{"decision":"new_change","confidence":-0.5}`, true, "new_change", 0, ""},
		{"no json object", "I cannot decide that.", false, "", 0, ""},
		{"malformed json", `{"decision":"new_change",`, false, "", 0, ""},
		{"empty", "", false, "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, ok := parseRouteDecision(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("parseRouteDecision(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if dec.Decision != tt.wantDec || dec.Confidence != tt.wantConf || dec.Name != tt.wantName {
				t.Errorf("parseRouteDecision(%q) = {decision:%q conf:%v name:%q}, want {decision:%q conf:%v name:%q}",
					tt.in, dec.Decision, dec.Confidence, dec.Name, tt.wantDec, tt.wantConf, tt.wantName)
			}
		})
	}
}

// stubProvider is a controllable llm.Provider for the classifyRoute fail-safe
// paths — it returns a fixed response or error and a settable availability.
type stubProvider struct {
	resp      string
	err       error
	available bool
}

func (s stubProvider) Name() string      { return "stub" }
func (s stubProvider) IsAvailable() bool { return s.available }
func (s stubProvider) Generate(_ context.Context, _ string) (string, error) {
	return s.resp, s.err
}
func (s stubProvider) GenerateWithSystem(_ context.Context, _, _ string) (string, error) {
	return s.resp, s.err
}
func (s stubProvider) GenerateWithStats(_ context.Context, _ string) (string, llm.GenerationStats, error) {
	return s.resp, llm.GenerationStats{}, s.err
}

// classifyRoute must fail safe (ok=false → caller continues) on a nil provider,
// an unavailable provider, or an LLM error, and only succeed when an available
// provider returns a parseable object.
func TestClassifyRoute(t *testing.T) {
	ctx := context.Background()
	t.Run("nil provider", func(t *testing.T) {
		if _, ok := classifyRoute(ctx, nil, "m", "g"); ok {
			t.Error("nil provider should fail safe (ok=false)")
		}
	})
	t.Run("unavailable provider", func(t *testing.T) {
		p := stubProvider{resp: `{"decision":"new_change","confidence":0.9}`, available: false}
		if _, ok := classifyRoute(ctx, p, "m", "g"); ok {
			t.Error("unavailable provider should fail safe (ok=false)")
		}
	})
	t.Run("llm error", func(t *testing.T) {
		p := stubProvider{err: errors.New("boom"), available: true}
		if _, ok := classifyRoute(ctx, p, "m", "g"); ok {
			t.Error("llm error should fail safe (ok=false)")
		}
	})
	t.Run("happy path", func(t *testing.T) {
		p := stubProvider{resp: `{"decision":"new_change","name":"x","confidence":0.95}`, available: true}
		dec, ok := classifyRoute(ctx, p, "m", "g")
		if !ok || dec.Decision != routeNewChange || dec.Confidence != 0.95 {
			t.Errorf("classifyRoute happy path = (%+v, %v), want new_change/0.95/true", dec, ok)
		}
	})
}

// stripMention removes the bot's mention in both plain and nickname forms so the
// model sees the request, not the plumbing.
func TestStripMention(t *testing.T) {
	const botID = "12345"
	tests := []struct {
		in   string
		want string
	}{
		{"<@12345> fix the bug", " fix the bug"},
		{"<@!12345> deploy", " deploy"},
		{"no mention here", "no mention here"},
		{"hey <@12345> and <@!12345> done", "hey  and  done"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := stripMention(tt.in, botID); got != tt.want {
				t.Errorf("stripMention(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
