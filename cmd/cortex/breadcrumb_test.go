package main

import (
	"regexp"
	"strings"
	"testing"
)

// timestampPrefixRe matches the "HH:MM:SS  " gutter prefix that replaced the
// per-role icon (2026-07-19 de-glyph decision, display.go's gutterPrefix):
// the timestamp's color is now the only thing marking a note/thought line.
var timestampPrefixRe = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\s+`)

// resWithTools builds an assistant response carrying a tool call, the case where
// a step makes progress without emitting answer prose.
func resWithTools() *AgentResponse {
	return &AgentResponse{Choices: []Choice{{
		Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			Function: FunctionCall{Name: "read_file", Arguments: `{"path":"go.mod"}`},
		}}},
	}}}
}

// TestBreadcrumbPersistsSilentToolStep: a step that narrates via reasoning and
// then calls a tool (no content) must leave a persisted one-line trace — the fix
// for narration that otherwise only flashes on the transient thinking ticker.
func TestBreadcrumbPersistsSilentToolStep(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf} // no spinner, no onStatus
	p.onReasoning("The user is asking me to read the go.mod file. I will use read_file.")
	p.finish() // no content streamed → nothing printed yet
	if buf.Len() != 0 {
		t.Fatalf("a reasoning-only step should print nothing until the breadcrumb, got %q", buf.String())
	}
	p.breadcrumb(resWithTools())

	out := stripANSI(buf.String())
	if !timestampPrefixRe.MatchString(out) {
		t.Errorf("breadcrumb should carry the timestamp gutter, got %q", out)
	}
	if !strings.Contains(out, "read the go.mod file") {
		t.Errorf("breadcrumb should trace the reasoning, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("breadcrumb must be exactly one line, got %q", out)
	}
}

// TestBreadcrumbSkippedWhenProsePrinted: if the step already streamed answer
// prose, that prose persisted on its own — no breadcrumb on top of it.
func TestBreadcrumbSkippedWhenProsePrinted(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf} // md nil → raw streaming
	p.onReasoning("some reasoning")
	p.onContent("Here is the answer.")
	p.finish()
	before := buf.String()
	p.breadcrumb(resWithTools())
	if buf.String() != before {
		t.Errorf("no breadcrumb expected when prose was printed; added %q", strings.TrimPrefix(buf.String(), before))
	}
}

// TestBreadcrumbSkippedWithoutToolCalls: a final-answer step (no tool calls)
// gets no breadcrumb even if it produced no prose here.
func TestBreadcrumbSkippedWithoutToolCalls(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf}
	p.onReasoning("thinking about the final answer")
	p.finish()
	p.breadcrumb(&AgentResponse{Choices: []Choice{{Message: Message{Role: "assistant"}}}})
	if buf.Len() != 0 {
		t.Errorf("no breadcrumb expected without tool calls, got %q", buf.String())
	}
}

// TestBreadcrumbSkippedWithoutReasoning: nothing to trace → nothing printed.
func TestBreadcrumbSkippedWithoutReasoning(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf}
	p.finish()
	p.breadcrumb(resWithTools())
	if buf.Len() != 0 {
		t.Errorf("no breadcrumb expected without reasoning, got %q", buf.String())
	}
}

func TestCollapseLine(t *testing.T) {
	if got := collapseLine("  multi\n  line\t text ", 140); got != "multi line text" {
		t.Errorf("collapseLine whitespace = %q", got)
	}
	long := strings.Repeat("a", 200)
	got := collapseLine(long, 140)
	if !strings.HasSuffix(got, "...") || len([]rune(got)) != 143 {
		t.Errorf("collapseLine should cap at 140 runes + ASCII ellipsis, got %d runes", len([]rune(got)))
	}
}
