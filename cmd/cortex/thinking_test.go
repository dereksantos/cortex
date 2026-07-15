package main

import (
	"strings"
	"testing"
	"time"
)

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
