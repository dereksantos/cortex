package main

import (
	"strings"
	"testing"
	"time"
)

// resWithoutTools builds an assistant response with no tool calls — the
// "answered directly, took a while to think first" case thoughtStat targets.
func resWithoutToolsThinking() *AgentResponse {
	return &AgentResponse{Choices: []Choice{{Message: Message{Role: "assistant", Content: "the answer"}}}}
}

// TestThoughtStat covers item 5: a turn-end "thought Ns · tok" gutter line
// when reasoning occurred and was substantial (elapsed or token threshold),
// using the reported reasoning-token count when available and falling back
// to a length-based estimate otherwise.
func TestThoughtStat(t *testing.T) {
	tests := []struct {
		name        string
		reasoning   string
		elapsed     time.Duration
		res         *AgentResponse
		wantPrinted bool
		wantSubstr  string // substring the printed line must contain, when wantPrinted
	}{
		{
			name:        "no reasoning: nothing printed",
			reasoning:   "",
			elapsed:     10 * time.Second,
			res:         resWithoutToolsThinking(),
			wantPrinted: false,
		},
		{
			name:        "brief reasoning under both thresholds: nothing printed",
			reasoning:   "ok",
			elapsed:     500 * time.Millisecond,
			res:         resWithoutToolsThinking(),
			wantPrinted: false,
		},
		{
			name:        "elapsed threshold met: printed with estimated tokens",
			reasoning:   strings.Repeat("a", 40), // 40 chars / 4 = 10 estimated tokens
			elapsed:     5 * time.Second,
			res:         resWithoutToolsThinking(),
			wantPrinted: true,
			wantSubstr:  "thought 5s",
		},
		{
			name:      "reported reasoning tokens used over the estimate",
			reasoning: "brief",
			elapsed:   3 * time.Second,
			res: &AgentResponse{
				Choices: []Choice{{Message: Message{Role: "assistant", Content: "the answer"}}},
				Usage:   Usage{CompletionTokensDetails: &completionTokensDetails{ReasoningTokens: 1500}},
			},
			wantPrinted: true,
			wantSubstr:  "1.5k tok",
		},
		{
			name:        "token threshold met alone (elapsed under 2s)",
			reasoning:   strings.Repeat("a", 4000), // 4000/4 = 1000 estimated tokens
			elapsed:     1 * time.Second,
			res:         resWithoutToolsThinking(),
			wantPrinted: true,
			wantSubstr:  "1k tok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			p := &streamPrinter{out: &buf, start: time.Now().Add(-tt.elapsed)}
			p.reason.WriteString(tt.reasoning)
			p.thoughtStat(tt.res)

			out := buf.String()
			if tt.wantPrinted && out == "" {
				t.Fatalf("expected a thought-stat line, got nothing")
			}
			if !tt.wantPrinted && out != "" {
				t.Fatalf("expected no thought-stat line, got %q", out)
			}
			if tt.wantPrinted {
				plain := stripANSI(out)
				if !strings.Contains(plain, tt.wantSubstr) {
					t.Errorf("thought-stat = %q, want substring %q", plain, tt.wantSubstr)
				}
				if strings.Count(plain, "\n") != 1 {
					t.Errorf("thought-stat must be exactly one line, got %q", plain)
				}
			}
		})
	}
}

// TestThoughtStatSkippedWhenBreadcrumbPrinted: thoughtStat is a no-op once
// breadcrumb has already shown the reasoning trace for the same step — one
// gray gutter line per step, not two redundant ones.
func TestThoughtStatSkippedWhenBreadcrumbPrinted(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf, start: time.Now().Add(-10 * time.Second)}
	p.onReasoning(strings.Repeat("a", 4000)) // well past both thresholds
	res := resWithTools()                    // has tool calls, no prose printed -> breadcrumb fires

	p.breadcrumb(res)
	if !p.crumbed {
		t.Fatalf("breadcrumb should have printed for a silent tool step")
	}
	before := buf.String()
	p.thoughtStat(res)
	if buf.String() != before {
		t.Errorf("thoughtStat should have been skipped after breadcrumb printed; added %q",
			strings.TrimPrefix(buf.String(), before))
	}
}

// TestElapsedTail covers the "thinking… Ns · tail" label formatting shared by
// both display modes (the standalone spinner and the anchored status row).
func TestElapsedTail(t *testing.T) {
	tests := []struct {
		name  string
		start time.Time
		tail  string
		want  string
	}{
		{"zero start (unset, e.g. tests): reads as 0s, no tail", time.Time{}, "", "0s"},
		{"zero start with tail", time.Time{}, "hello", "0s · hello"},
		{"elapsed, no tail yet", time.Now().Add(-12 * time.Second), "", "12s"},
		{"elapsed with tail", time.Now().Add(-3 * time.Second), "reasoning excerpt", "3s · reasoning excerpt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := elapsedTail(tt.start, tt.tail); got != tt.want {
				t.Errorf("elapsedTail(...) = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOnReasoningShowsElapsedSeconds proves onReasoning's live status update
// carries the elapsed seconds since the model call started, not just the raw
// reasoning tail — the anchored-status-row path (onStatus).
func TestOnReasoningShowsElapsedSeconds(t *testing.T) {
	type call struct {
		on   bool
		tail string
	}
	var calls []call
	p := &streamPrinter{
		onStatus: func(on bool, tail string) { calls = append(calls, call{on, tail}) },
		start:    time.Now().Add(-5 * time.Second),
	}
	p.onReasoning("thinking about it")

	if len(calls) != 1 {
		t.Fatalf("got %d onStatus calls, want 1: %+v", len(calls), calls)
	}
	if !calls[0].on {
		t.Errorf("onStatus called with on=false, want true")
	}
	if !strings.HasPrefix(calls[0].tail, "5s") && !strings.HasPrefix(calls[0].tail, "6s") {
		t.Errorf("tail = %q, want it to start with the elapsed seconds (~5s)", calls[0].tail)
	}
	if !strings.Contains(calls[0].tail, "thinking about it") {
		t.Errorf("tail = %q, want it to carry the reasoning excerpt", calls[0].tail)
	}
}

// TestOnReasoningNoopAfterAnswerBegan: once the answer has started (p.began),
// onReasoning must not fire another status callback — the transient thinking
// indicator is only for the deliberation phase. begin() itself fires exactly
// one onStatus(false, "") to clear the indicator when the answer starts; a
// later onReasoning call must not add a second one.
func TestOnReasoningNoopAfterAnswerBegan(t *testing.T) {
	var calls int
	var buf strings.Builder
	p := &streamPrinter{
		out:      &buf,
		onStatus: func(bool, string) { calls++ },
		start:    time.Now(),
	}
	p.onContent("This is a long enough answer to clear the tool-marker holdback window.") // begin() fires onStatus(false, "") once
	if calls != 1 {
		t.Fatalf("onStatus called %d times after the answer began, want exactly 1 (the clear)", calls)
	}
	p.onReasoning("late reasoning that should be ignored")
	if calls != 1 {
		t.Errorf("onStatus called %d times after a post-answer onReasoning, want still 1", calls)
	}
}
