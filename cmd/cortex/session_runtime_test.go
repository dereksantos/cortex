// session_runtime_test.go — Fix 2 of docs/cross-source-learning.md's
// "Code-reality check": turnArtifacts special-cased write_file/edit_file/
// bash for the capture summary's outcome line but left web_search/fetch_url
// invisible to it, so a fact learned from a search was materially harder
// for Learn to see than one learned from editing a file. These tests pin
// the bounded artifact-line format turnArtifacts now produces for both
// tools, and that captureTurn actually writes an event carrying it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/pkg/config"
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

// TestCaptureTurnRedactsSecretsFromJournal (issue #103): a secret that
// surfaces in a tool's OUTPUT (and in the user's prompt) must reach the
// on-disk journal ONLY as [REDACTED:…], never verbatim. This is the journal
// surface — distinct from the transcript's per-message redaction
// (session.go's writeTranscript): captureTurn is the single choke point every
// loop capture flows through (loop.run and cortex learn's replay both read
// this same journal), so masking it here covers both. The live in-memory
// turnMsgs are left verbatim (the model can still use the value this turn);
// only what is persisted is masked. We read the event back through
// scanCaptureWindow — the SAME reader Learn itself uses — and additionally
// scan the raw JSONL, so a secret can't hide in a field Learn doesn't read.
func TestCaptureTurnRedactsSecretsFromJournal(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := newMemSession(t)
	if cs.capturer == nil {
		t.Fatal("session has no capturer wired")
	}

	const secret = "sk-or-v1-0123456789abcdef0123456789abcdef"
	turnMsgs := []Message{
		{Role: RoleUser, Content: "what is my key?"},
		webSearchCall("c1", "how to rotate an api key"),
		toolResult("c1", "1. Rotate keys\n   your current key is "+secret),
		{Role: "assistant", Content: "here is your key: " + secret},
	}
	cs.captureTurn("here is the key: "+secret, turnMsgs)
	if cs.captures == 0 {
		t.Fatal("captureTurn recorded no event")
	}

	// Learn's own read path (scanCaptureWindow) sees the secret ONLY redacted.
	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no capture entries on disk")
	}
	last := entries[len(entries)-1]
	if strings.Contains(last.Result, secret) {
		t.Errorf("captured entry.Result still contains the verbatim secret — issue #103 requires it redacted before the journal write")
	}
	if strings.Contains(last.Prompt, secret) {
		t.Errorf("captured entry.Prompt still contains the verbatim secret — the user prompt must be redacted too")
	}
	if !strings.Contains(last.Result, "[REDACTED:") {
		t.Errorf("captured entry.Result = %q, want at least one [REDACTED:...] marker (the tool result and the answer both carried the secret)", last.Result)
	}
	if !strings.Contains(last.Prompt, "[REDACTED:") {
		t.Errorf("captured entry.Prompt = %q, want a [REDACTED:...] marker for the redacted user prompt", last.Prompt)
	}

	// Raw JSONL scan: no secret may survive in ANY persisted field, including
	// ones Learn's reader doesn't touch (Metadata, Context, etc.).
	dir := filepath.Join(cs.ContextDir(), "journal", "capture")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading journal %s: %v", path, err)
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("journal file %s still contains the verbatim secret", path)
		}
	}
	if len(matches) == 0 {
		t.Fatal("no capture journal file found — the event was not persisted")
	}

	// The live in-memory turnMsgs are untouched: the model can still use the
	// value this turn, so it must still hold the verbatim secret.
	liveHasSecret := false
	for _, m := range turnMsgs {
		if strings.Contains(m.Content, secret) {
			liveHasSecret = true
			break
		}
	}
	if !liveHasSecret {
		t.Errorf("live in-memory turnMsgs lost the secret — only the persisted event should be redacted")
	}
}

// TestTranscriptStateAndNotesRedactSecrets (issue #103, review fix 2): the
// session file holds MORE than per-message entries — the kindState snapshot
// (writeSessionState, which persists outline text built from live, unredacted
// messages) and kindNote entries (transcriptNote, harness-side markers) both
// reach the same file, and BOTH redact on the copy that hits disk while the
// live in-memory state stays verbatim (the outline block rides the model's
// context this turn, so masking the live copy would blind the model to a
// value it still uses). This test drives a real demotion (the outline holds
// a demoted turn whose user text and reply carry a secret), then reads the
// RAW session file back — every line, including kindState and kindNote — and
// fails if the verbatim key appears anywhere.
func TestTranscriptStateAndNotesRedactSecrets(t *testing.T) {
	t.Chdir(t.TempDir())
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	// Window left at the default (fallbackWindow, 32768): high watermark
	// 16384 / low 10922 tokens, so a ~12000-token turn demotes on the next
	// DemoteBatch (the window must NOT be overridden — a smaller window
	// shrinks the watermarks, and the turn would not cross the high one).
	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })

	const secret = "sk-or-v1-0123456789abcdef0123456789abcdef"
	// Turn 1 (~18000 tokens — over the high watermark so the NEXT turn's
	// DemoteBatch demotes it into the outline): user text and reply both
	// carry the secret.
	userText := "store this: my key is " + secret
	cs.Append(Message{Role: RoleUser, Content: userText})
	for i := 0; i < 24; i++ {
		cs.Append(Message{Role: RoleTool, ToolCallID: "c" + ritoa(i), Content: strings.Repeat("t", 3000)})
	}
	cs.Append(Message{Role: "assistant", Content: "I stored it, the key is " + secret + " ok"})
	turn1End := len(cs.Request.Messages)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: turn1End, Tokens: estTurnTokens(cs.Request.Messages[1:turn1End])})

	// Add a small SECOND turn and drain it: DemoteBatch never demotes the
	// most recent turn (the frontier keeps at least one turn hydrated), so a
	// single-turn log can never be demoted no matter how large — exactly the
	// shape the real demote loop hands the next turn's DemoteBatch on.
	cs.Append(Message{Role: RoleUser, Content: "next question"})
	cs.Append(Message{Role: "assistant", Content: "answer"})
	turn2End := len(cs.Request.Messages)
	cs.ws.AddTurn(cache.TurnSpan{Start: turn1End, End: turn2End, Tokens: estTurnTokens(cs.Request.Messages[turn1End:turn2End])})

	// Force turn 1 to demote, the way the next turn's DemoteBatch does: the
	// outline entry holds the turn's user text and reply head verbatim (live
	// copy), built from the wire messages exactly like turn.go's demote loop.
	batch := cs.ws.DemoteBatch()
	if len(batch) != 1 {
		t.Fatalf("DemoteBatch demoted %d turns, want 1 (turn 1 must be over the high watermark)", len(batch))
	}
	cs.outline = append(cs.outline, turnOutlineEntry(1, batch[0], cs.Request.Messages[batch[0].Start:batch[0].End], cs.SessionID))
	if !strings.Contains(cs.outline[0].User, secret) {
		t.Fatal("fixture: the demoted turn's outline User entry does not hold the verbatim secret")
	}

	// The state snapshot (written at turn end by turn.go's deferred
	// writeSessionState) must redact the outline text it persists.
	cs.writeSessionState()

	// A harness-side note carrying the same secret (transcriptNote is used
	// for e.g. the reasoning-fallback marker) must be redacted on disk too.
	cs.transcriptNote("backend error while storing " + secret + " — see log")

	// Read the RAW session file back: every line, including kindState and
	// kindNote entries, must hold the secret only as a redaction marker.
	path := filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	raw := string(b)
	if strings.Contains(raw, secret) {
		t.Errorf("session file %s still contains the verbatim secret — kindMessage, kindState and kindNote entries must all be redacted before persisting", path)
	}
	if !strings.Contains(raw, "[REDACTED:provider-key]") {
		t.Errorf("session file %s has no [REDACTED:provider-key] marker, want at least one (the turn's text and the note both carried the secret)", path)
	}

	// Per-kind: the kindState snapshot's persisted outline and the kindNote
	// entry must each carry the marker — not just the kindMessage entries
	// that the per-message writeTranscript redaction already covers.
	var sawStateMarker, sawNoteMarker bool
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad session line %q: %v", line, err)
		}
		switch e.Kind {
		case kindState:
			if e.State == nil {
				t.Fatal("kindState entry has no state")
			}
			for _, oe := range e.State.Outline {
				if strings.Contains(oe.User, secret) {
					t.Errorf("kindState outline User = %q, want it redacted (the live copy stays verbatim, the snapshot does not)", oe.User)
				}
				if strings.Contains(oe.User, "[REDACTED:") {
					sawStateMarker = true
				}
			}
			if strings.Contains(e.State.OutlineFolded, secret) {
				t.Errorf("kindState outline_folded still contains the verbatim secret")
			}
		case kindNote:
			if strings.Contains(e.Content, secret) {
				t.Errorf("kindNote content = %q, want it redacted (transcriptNote masks the copy that hits disk)", e.Content)
			}
			if strings.Contains(e.Content, "[REDACTED:") {
				sawNoteMarker = true
			}
		}
	}
	if !sawStateMarker {
		t.Errorf("no kindState outline entry carries a [REDACTED:…] marker — the demoted turn's text carried the secret, so the snapshot must mask it")
	}
	if !sawNoteMarker {
		t.Errorf("no kindNote entry carries a [REDACTED:…] marker — the note carried the secret, so it must be masked on disk")
	}

	// The live in-memory copy stays UNMASKED: the outline block rides the
	// model's context this turn, so the model can still use the value.
	if !strings.Contains(cs.outline[0].User, secret) {
		t.Errorf("live cs.outline[0].User lost the verbatim secret — the live outline must stay unmasked (only what is persisted is redacted)")
	}
	if !strings.Contains(cs.Request.Messages[1].Content, secret) {
		t.Errorf("live Request.Messages[1] lost the verbatim secret — the in-memory log must stay unmasked")
	}
}

// TestTurnCLIStdoutContractWithRedactions (issue #103, review fix 3): a
// `cortex turn` whose persisted messages carry a secret must keep issue
// #118's answer-only stdout contract — stdout holds ONLY the verbatim reply,
// and the redaction notice goes to stderr. The turn is driven end-to-end
// (a real send against an SSE stub, so the reply's own masking is real),
// then its reporting goes through cli.go's reportTurnText — the SAME code
// runTurnCLI's non-JSON branch calls — with two buffers, so a regression in
// cli.go's routing (e.g. the redaction notice handed the stdout writer
// instead of stderr) fails this test.
func TestTurnCLIStdoutContractWithRedactions(t *testing.T) {
	const secret = "sk-or-v1-0123456789abcdef0123456789abcdef"
	bodyAnswer := fmt.Sprintf(`{"choices":[{"delta":{"role":"assistant","content":"the key is %s"}}]}`, secret)
	bodyFinishStop := `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
	bodyUsage := `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := sseBody(bodyAnswer, bodyFinishStop, bodyUsage)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	// NOT quiet: headless `cortex turn` sends quiet, but quiet sends use the
	// blocking JSON path, and this test's SSE fixture only feeds the SSE
	// parser — the streaming (SendStream) path. The turn-boundary contract
	// under test (printRedactions' sink) is independent of how the reply
	// arrived, and a non-quiet session's reply is still read from
	// TurnResult exactly as cli.go does.
	cs.Config = &Config{}
	cs.capturer = capture.New(&config.Config{ContextDir: filepath.Join(root, ".cortex")})
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript did not open a transcript")
	}
	t.Cleanup(func() { cs.transcript.Close() })

	res, err := cs.Turn(context.Background(), "print my key")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.Redactions <= 0 {
		t.Fatalf("fixture: TurnResult.Redactions = %d, want > 0 (the turn's persisted messages carried the secret)", res.Redactions)
	}

	// The non-json branch of runTurnCLI, via the SAME helper it calls
	// (reportTurnText, cli.go) on test writers — the redaction notice must
	// land on stderr (printRedactions is handed the stderr writer there — the
	// #118 fix under test); a regression that hands it the stdout writer
	// (the original fmt.Println → stdout bug) fails the assertions below.
	var outBuf, errBuf bytes.Buffer
	reportTurnText(&outBuf, &errBuf, err, res, cs.SessionID)

	stdout, stderr := outBuf.String(), errBuf.String()
	if stdout != res.Reply+"\n" {
		t.Errorf("stdout = %q, want exactly the reply only (%q) — issue #118's answer-only contract", stdout, res.Reply+"\n")
	}
	if !strings.Contains(stdout, secret) {
		t.Errorf("stdout lost the verbatim reply — the headless answer must stay unredacted (only disk is masked)")
	}
	if strings.Contains(stderr, secret) {
		t.Errorf("stderr carries the verbatim secret — the redaction notice must not echo the value")
	}
	if !strings.Contains(stderr, fmt.Sprintf("%d secret pattern(s) redacted", res.Redactions)) {
		t.Errorf("stderr = %q, want the redaction notice for %d", stderr, res.Redactions)
	}
	if strings.Contains(stdout, "secret pattern(s) redacted") {
		t.Errorf("the redaction notice leaked to stdout — it belongs on stderr (issue #118)")
	}
}
