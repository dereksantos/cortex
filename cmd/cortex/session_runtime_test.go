// session_runtime_test.go — Fix 2 of docs/cross-source-learning.md's
// "Code-reality check": turnArtifacts special-cased write_file/edit_file/
// bash for the capture summary's outcome line but left web_search/fetch_url
// invisible to it, so a fact learned from a search was materially harder
// for Learn to see than one learned from editing a file. These tests pin
// the bounded artifact-line format turnArtifacts now produces for both
// tools, and that captureTurn actually writes an event carrying it.
package main

import (
	"strings"
	"testing"
)

func webSearchCall(id, query string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID:       id,
		Function: FunctionCall{Name: FunctionWebSearch, Arguments: `{"query":"` + query + `"}`},
	}}}
}

func fetchURLCall(id, url string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID:       id,
		Function: FunctionCall{Name: FunctionFetchURL, Arguments: `{"url":"` + url + `"}`},
	}}}
}

func toolResult(id, content string) Message {
	return Message{Role: RoleTool, ToolCallID: id, Content: content}
}

func TestTurnArtifactsRecordsWebSearch(t *testing.T) {
	turnMsgs := []Message{
		webSearchCall("c1", "cortex memory tools"),
		toolResult("c1", "1. Cortex memory-tools doc\n   https://example.com/memory-tools\n   how the model curates notes\n\n2. Another result\n   https://example.com/other"),
	}
	outcome, _ := turnArtifacts(turnMsgs)
	if !strings.Contains(outcome, "searched: ") {
		t.Fatalf("outcome = %q, want a searched: segment", outcome)
	}
	if !strings.Contains(outcome, "cortex memory tools") {
		t.Errorf("outcome = %q, want the query present", outcome)
	}
	if !strings.Contains(outcome, "Cortex memory-tools doc") || !strings.Contains(outcome, "https://example.com/memory-tools") {
		t.Errorf("outcome = %q, want the top result's title and URL", outcome)
	}
	if !strings.Contains(outcome, "2 result(s)") {
		t.Errorf("outcome = %q, want the result count", outcome)
	}
}

func TestTurnArtifactsRecordsWebSearchNoResults(t *testing.T) {
	turnMsgs := []Message{
		webSearchCall("c1", "an unanswerable query"),
		toolResult("c1", "(no search results)"),
	}
	outcome, _ := turnArtifacts(turnMsgs)
	if !strings.Contains(outcome, "0 results") {
		t.Errorf("outcome = %q, want a 0 results line for an empty search", outcome)
	}
}

func TestTurnArtifactsRecordsFetchURL(t *testing.T) {
	turnMsgs := []Message{
		fetchURLCall("c1", "https://example.com/docs"),
		toolResult("c1", "URL: https://example.com/docs\nTitle: Docs\nContent-Type: text/html\n\nThis page explains the thing in detail."),
	}
	outcome, _ := turnArtifacts(turnMsgs)
	if !strings.Contains(outcome, "fetched: ") {
		t.Fatalf("outcome = %q, want a fetched: segment", outcome)
	}
	if !strings.Contains(outcome, "https://example.com/docs") {
		t.Errorf("outcome = %q, want the URL present", outcome)
	}
	if !strings.Contains(outcome, "bytes") {
		t.Errorf("outcome = %q, want a response-size figure", outcome)
	}
	if !strings.Contains(outcome, "This page explains the thing") {
		t.Errorf("outcome = %q, want an excerpt of the fetched content", outcome)
	}
}

// TestTurnArtifactsWebLinesAreBounded proves the "bounded — reuse the
// existing artifact line discipline" requirement: an oversized query, an
// oversized result title, and a huge fetched page must not make it into
// the capture summary unclipped, the same way captureExcerptCap already
// bounds the final-answer line.
func TestTurnArtifactsWebLinesAreBounded(t *testing.T) {
	hugeQuery := strings.Repeat("q", 5000)
	hugeTitle := strings.Repeat("t", 5000)
	turnMsgs := []Message{
		webSearchCall("c1", hugeQuery),
		toolResult("c1", "1. "+hugeTitle+"\n   https://example.com/x"),
	}
	outcome, _ := turnArtifacts(turnMsgs)
	if len(outcome) > 2*webArtifactTitleCapChars+200 {
		t.Errorf("web_search outcome line is %d chars, not bounded (query=%d, title=%d)", len(outcome), len(hugeQuery), len(hugeTitle))
	}
	if strings.Contains(outcome, hugeQuery) {
		t.Error("outcome contains the full oversized query, not truncated")
	}

	hugeBody := "URL: https://example.com/y\n\n" + strings.Repeat("body ", 5000)
	turnMsgs2 := []Message{
		fetchURLCall("c2", "https://example.com/y"),
		toolResult("c2", hugeBody),
	}
	outcome2, _ := turnArtifacts(turnMsgs2)
	if len(outcome2) > webArtifactExcerptCapChars+200 {
		t.Errorf("fetch_url outcome line is %d chars, not bounded (body=%d)", len(outcome2), len(hugeBody))
	}
}

// TestTurnArtifactsIgnoresWebCallsWithoutAResult proves a call whose result
// message never landed in turnMsgs (e.g. a truncated fixture, or a call
// still in flight) degrades to a labeled placeholder rather than panicking
// or silently vanishing.
func TestTurnArtifactsIgnoresWebCallsWithoutAResult(t *testing.T) {
	turnMsgs := []Message{webSearchCall("c1", "orphaned query")}
	outcome, _ := turnArtifacts(turnMsgs)
	if !strings.Contains(outcome, "no result captured") {
		t.Errorf("outcome = %q, want a no-result-captured placeholder for an unmatched call", outcome)
	}
}

// TestCaptureTurnWritesWebArtifacts is the end-to-end proof: a turn whose
// tool calls include web_search/fetch_url produces a capture.event whose
// ToolResult carries the bounded artifact lines above — the same journal
// path Learn reads (formatLearnEntry renders it via the existing outcome
// text, no new journal class).
func TestCaptureTurnWritesWebArtifacts(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := newMemSession(t)

	turnMsgs := []Message{
		{Role: RoleUser, Content: "look this up"},
		webSearchCall("c1", "cortex memory tools"),
		toolResult("c1", "1. Cortex memory-tools doc\n   https://example.com/memory-tools"),
		{Role: "assistant", Content: "found it"},
	}
	cs.captureTurn("look this up", turnMsgs)
	if cs.captures == 0 {
		t.Fatal("captureTurn recorded no event")
	}

	// scanCaptureWindow (learn.go) is the same reader Learn itself uses over
	// the capture writer-class journal — reusing it here proves the written
	// event is actually visible on Learn's own read path, not just present
	// in some unrelated form.
	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no capture entries on disk")
	}
	last := entries[len(entries)-1].Result
	if !strings.Contains(last, "searched:") || !strings.Contains(last, "cortex memory tools") {
		t.Errorf("captured entry.Result = %q, want a searched: segment naming the query", last)
	}

	// formatLearnEntry (learn.go) is what actually renders into the Learn
	// subagent's seed — confirm the web artifact survives that render too,
	// closing the loop the doc names ("the learning loop's digest then sees
	// them for free").
	rendered := formatLearnEntry(cs.SessionsDir(), entries[len(entries)-1])
	if !strings.Contains(rendered, "searched:") {
		t.Errorf("formatLearnEntry output = %q, want the searched: segment preserved", rendered)
	}
}
