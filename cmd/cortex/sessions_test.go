package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/fslock"
)

func writeTestSession(t *testing.T, dir, id string, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0644); err != nil {
		t.Fatalf("write session %s: %v", id, err)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	// Older session first (ids are timestamps; lexicographic order = chronological).
	// Transcript lines embed Message (role/content promoted to top level).
	writeTestSession(t, dir, "20260101-000000",
		`{"kind":"message","role":"system","content":"sys"}`,
		`{"kind":"message","role":"user","content":"first prompt here"}`,
		`{"kind":"message","role":"assistant","content":"reply"}`,
		`{"kind":"retrieval","query":"x"}`, // not a core message
	)
	writeTestSession(t, dir, "20260202-000000",
		`{"kind":"message","role":"system","content":"sys"}`,
		`{"kind":"message","role":"user","content":"newer\nsecond line"}`,
	)
	// A non-session file must be ignored.
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644)

	infos, err := listSessions(dir, 0)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d sessions, want 2", len(infos))
	}
	// Newest first.
	if infos[0].ID != "20260202-000000" || infos[1].ID != "20260101-000000" {
		t.Errorf("order = [%s, %s], want newest first", infos[0].ID, infos[1].ID)
	}
	// Core message count excludes system + retrieval entries.
	if infos[1].Messages != 2 {
		t.Errorf("older session msgs = %d, want 2 (1 user + 1 assistant)", infos[1].Messages)
	}
	if infos[1].First != "first prompt here" {
		t.Errorf("older first prompt = %q", infos[1].First)
	}
	// First prompt is the first line only.
	if infos[0].First != "newer" {
		t.Errorf("newer first prompt = %q, want first line only", infos[0].First)
	}
}

func TestListSessionsLimit(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"20260101-000001", "20260101-000002", "20260101-000003"} {
		writeTestSession(t, dir, id, `{"kind":"message","role":"user","content":"hi"}`)
	}
	infos, err := listSessions(dir, 2)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d, want 2 (limit)", len(infos))
	}
	if infos[0].ID != "20260101-000003" {
		t.Errorf("first = %s, want newest", infos[0].ID)
	}
}

func TestListSessionsNoDir(t *testing.T) {
	if _, err := listSessions(filepath.Join(t.TempDir(), "missing"), 0); err == nil {
		t.Fatal("expected error for missing sessions dir")
	}
}

func TestRelTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "?"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-2 * 24 * time.Hour), "2d ago"},
	}
	for _, tt := range tests {
		if got := relTime(tt.t); got != tt.want {
			t.Errorf("relTime(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  hello world  "); got != "hello world" {
		t.Errorf("got %q", got)
	}
	if got := firstLine("line one\nline two"); got != "line one" {
		t.Errorf("got %q, want first line", got)
	}
}

// TestResumeRejectsLockedSession proves the single-writer guarantee end to
// end: while one session holds the exclusive lock on a transcript, a second
// session that tries to resume the same file gets a clear busy error instead
// of silently interleaving appends. The lock is released when the first
// session's artifact (its *os.File) is closed.
func TestResumeRejectsLockedSession(t *testing.T) {
	cs := newTestSession(t)
	cs.Append(Message{Role: RoleUser, Content: "first life"})
	id := cs.SessionID // cs.transcript still open → holds the lock

	intruder := &CortexSession{Request: CortexArgs{}.Request()}
	err := intruder.ResumeTranscript(id)
	if !errors.Is(err, fslock.ErrBusy) {
		t.Fatalf("second resume err = %v, want fslock.ErrBusy", err)
	}
	if intruder.transcript != nil {
		t.Fatal("intruder must not have opened the transcript")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Errorf("error message %q does not mention the session is busy", err.Error())
	}
}

// TestResumeAfterCloseSucceeds proves the lock is a held-file lock: once the
// first session closes its transcript, a second session can resume the same
// file. This is the legitimate hand-off (process exits, a new one resumes).
func TestResumeAfterCloseSucceeds(t *testing.T) {
	cs := newTestSession(t)
	cs.Append(Message{Role: RoleUser, Content: "first life"})
	id := cs.SessionID
	cs.transcript.Close()
	cs.transcript = nil

	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(id); err != nil {
		t.Fatalf("resume after close: %v", err)
	}
	defer resumed.transcript.Close()
	if got := resumed.Request.Messages[len(resumed.Request.Messages)-1].Content; got != "first life" {
		t.Errorf("resumed last message = %q, want %q", got, "first life")
	}
}

// newTestSession builds a persisted session in an isolated cwd.
func newTestSession(t *testing.T) *CortexSession {
	t.Helper()
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript did not open a transcript file")
	}
	t.Cleanup(func() { cs.transcript.Close() })
	return cs
}

func TestTranscriptRoundTrip(t *testing.T) {
	cs := newTestSession(t)

	cs.Append(Message{Role: RoleUser, Content: "fix the bug"})
	cs.Append(Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "c1", Type: "function", Function: FunctionCall{Name: FunctionBash, Arguments: `{"command":"go test"}`}},
	}})
	cs.Append(Message{Role: RoleTool, ToolCallID: "c1", Content: "ok"})

	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Close() // release the lock so a second session can resume the file
	if err := resumed.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.transcript.Close()

	want := cs.Request.Messages
	got := resumed.Request.Messages
	if len(got) != len(want) {
		t.Fatalf("resumed %d messages, want %d", len(got), len(want))
	}
	if got[0].Role != RoleSystem {
		t.Errorf("messages[0] role = %q, want the persisted system prompt", got[0].Role)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content || got[i].ToolCallID != want[i].ToolCallID {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The assistant message's tool calls must survive the round trip — resume
	// with a dangling tool result would 400 on the next send.
	if calls := got[2].ToolCalls; len(calls) != 1 || calls[0].ID != "c1" || calls[0].Function.Name != FunctionBash {
		t.Errorf("tool calls did not survive round trip: %+v", calls)
	}
	if resumed.SessionID != cs.SessionID {
		t.Errorf("resumed id %q, want %q", resumed.SessionID, cs.SessionID)
	}
}

func TestResumeAppendsToSameFile(t *testing.T) {
	cs := newTestSession(t)
	cs.Append(Message{Role: RoleUser, Content: "first life"})

	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Close() // hand the lock to the resuming session
	if err := resumed.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.transcript.Close()
	resumed.Append(Message{Role: RoleUser, Content: "second life"})

	cs2 := &CortexSession{Request: CortexArgs{}.Request()}
	resumed.Close() // and to the third
	if err := cs2.ResumeTranscript(""); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	defer cs2.transcript.Close()
	last := cs2.Request.Messages[len(cs2.Request.Messages)-1]
	if last.Content != "second life" {
		t.Errorf("post-resume append did not persist; last message = %q", last.Content)
	}
}

func TestResumeLatestPicksNewest(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := sessionsDir()
	os.MkdirAll(dir, 0755)
	line := func(content string) []byte {
		b, _ := json.Marshal(sessionEntry{Message: Message{Role: RoleUser, Content: content}})
		return append(b, '\n')
	}
	os.WriteFile(filepath.Join(dir, "20260101-000000.jsonl"), line("old"), 0644)
	os.WriteFile(filepath.Join(dir, "20260201-000000.jsonl"), line("new"), 0644)

	cs := &CortexSession{Request: CortexArgs{}.Request()}
	if err := cs.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer cs.transcript.Close()
	if cs.SessionID != "20260201-000000" {
		t.Errorf("resumed %q, want the newest session", cs.SessionID)
	}
	if cs.Request.Messages[0].Content != "new" {
		t.Errorf("loaded %q, want the newest transcript's content", cs.Request.Messages[0].Content)
	}
}

func TestResumeErrors(t *testing.T) {
	t.Run("no sessions dir", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.ResumeTranscript(""); err == nil {
			t.Fatal("expected error with no sessions directory")
		}
	})

	t.Run("malformed line is an error, not a silent skip", func(t *testing.T) {
		t.Chdir(t.TempDir())
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "20260101-000000.jsonl"), []byte("{not json\n"), 0644)

		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.ResumeTranscript(""); err == nil {
			t.Fatal("expected error for malformed transcript")
		}
	})
}

// An unpersisted session (study CLI, tests) must work identically — Append
// without a transcript is not an error.
func TestAppendWithoutTranscript(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Append(Message{Role: RoleUser, Content: "no persistence"})
	if n := len(cs.Request.Messages); n != 2 {
		t.Errorf("got %d messages, want 2", n)
	}
}

// stubCompactSummarize replaces the compaction summarizer call (no model, no
// network) for the duration of a test, recording the content and window it was
// given. compressed=true marks a real compaction; false → nothing to compact.
func stubCompactSummarize(t *testing.T, digest string, compressed bool, err error) (gotContent *string, gotWindow *int) {
	t.Helper()
	saved := compactSummarize
	t.Cleanup(func() { compactSummarize = saved })
	gotContent, gotWindow = new(string), new(int)
	compactSummarize = func(_ context.Context, _ *CortexSession, content string, window int) (string, bool, error) {
		*gotContent, *gotWindow = content, window
		return digest, compressed, err
	}
	return gotContent, gotWindow
}

func appendTestTurn(cs *CortexSession, ordinal int, user, assistant string) {
	cs.turnNo = ordinal
	start := len(cs.Request.Messages)
	cs.Append(Message{Role: RoleUser, Content: user})
	cs.Append(Message{Role: "assistant", Content: assistant})
	cs.ws.AddTurn(cache.TurnSpan{Start: start, End: len(cs.Request.Messages), Tokens: estTurnTokens(cs.Request.Messages[start:])})
	cs.turns = ordinal
	cs.turnNo = 0
}

func TestCompactRebuildsHistory(t *testing.T) {
	gotContent, gotWindow := stubCompactSummarize(t,
		"user is hardening the loop; edited cmd/cortex/main.go; tests pass", true, nil)

	cs := newTestSession(t)
	cs.Window = 64000
	cs.Study.Window = 32768
	cs.LastPromptTokens = 60000
	cs.ws = cs.newWorkingSet(1)
	appendTestTurn(cs, 1, "long conversation", "lots of work")
	appendTestTurn(cs, 2, "newest task context", "current answer")
	oldID := cs.SessionID
	sys := cs.Request.Messages[0]

	if err := cs.Compact(context.Background()); err != nil {
		t.Fatalf("compact: %v", err)
	}
	defer cs.transcript.Close()

	// Only the eligible old prefix was summarized; the newest complete turn
	// remains verbatim.
	if !strings.Contains(*gotContent, "long conversation") || strings.Contains(*gotContent, "newest task context") {
		t.Errorf("summarized content = %q, want only the old completed prefix", *gotContent)
	}
	if *gotWindow != 16000 {
		t.Errorf("study window = %d, want 16000 (codeWindow/4)", *gotWindow)
	}

	// History = original system seed + one state digest + newest raw turn.
	msgs := cs.Request.Messages
	if len(msgs) != 4 {
		t.Fatalf("compacted history has %d messages, want 4", len(msgs))
	}
	if msgs[0].Content != sys.Content || msgs[0].Role != RoleSystem {
		t.Error("system seed should survive compaction unchanged")
	}
	if msgs[1].Role != RoleUser || !strings.Contains(msgs[1].Content, "hardening the loop") {
		t.Errorf("digest message = %+v", msgs[1])
	}

	if msgs[2].Content != "newest task context" || msgs[3].Content != "current answer" {
		t.Errorf("newest turn was not retained verbatim: %+v", msgs[2:])
	}

	// Gauge now reflects the retained state instead of pretending context is empty.
	if cs.LastPromptTokens == 0 {
		t.Error("LastPromptTokens should reflect compacted state")
	}
	if cs.SessionID == oldID {
		t.Error("compaction should start a NEW session id")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir(), oldID+".jsonl")); err != nil {
		t.Errorf("raw transcript should stay on disk: %v", err)
	}

	// The new transcript must resume to exactly the compacted state.
	cs.Close() // release the lock before reopening the same file
	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(cs.SessionID); err != nil {
		t.Fatalf("resume after compact: %v", err)
	}
	defer resumed.transcript.Close()
	if len(resumed.Request.Messages) != 4 || !strings.Contains(resumed.Request.Messages[1].Content, "hardening the loop") || resumed.Request.Messages[2].Content != "newest task context" {
		t.Errorf("resume should restore digest plus newest raw turn, got %d messages", len(resumed.Request.Messages))
	}
}

func TestCompactFoldsExistingStateLayer(t *testing.T) {
	gotContent, _ := stubCompactSummarize(t, "updated state", true, nil)
	cs := newTestSession(t)
	cs.Request.Messages = append(cs.Request.Messages, Message{Role: RoleUser, Content: "[Session state]\nprior-decision-marker"})
	cs.writeTranscript(cs.Request.Messages[1])
	cs.ws = cs.newWorkingSet(2)
	appendTestTurn(cs, 1, "older raw turn", "older answer")
	appendTestTurn(cs, 2, "newest raw turn", "newest answer")

	if err := cs.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*gotContent, "prior-decision-marker") || !strings.Contains(*gotContent, "older raw turn") {
		t.Fatalf("compaction input lost layered state: %q", *gotContent)
	}
	if strings.Contains(*gotContent, "newest raw turn") {
		t.Fatalf("compaction input included newest turn: %q", *gotContent)
	}
}

func TestCompactErrors(t *testing.T) {
	t.Run("unpersisted session", func(t *testing.T) {
		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.Compact(context.Background()); err == nil {
			t.Fatal("expected error for unpersisted session")
		}
	})

	t.Run("uncompressed is refused — nothing to compress", func(t *testing.T) {
		stubCompactSummarize(t, "", false, nil) // fit a single chunk → nothing to compact
		cs := newTestSession(t)
		cs.ws = cs.newWorkingSet(1)
		appendTestTurn(cs, 1, "old "+strings.Repeat("x", 3000), "old answer")
		appendTestTurn(cs, 2, "current", "current answer")
		before := len(cs.Request.Messages)

		err := cs.Compact(context.Background())
		if err == nil || !strings.Contains(err.Error(), "nothing to compact") {
			t.Fatalf("expected nothing-to-compact error, got %v", err)
		}
		if len(cs.Request.Messages) != before {
			t.Error("a refused compact must leave history unchanged")
		}
	})

	t.Run("empty digest leaves history unchanged", func(t *testing.T) {
		stubCompactSummarize(t, "  ", true, nil) // compressed but empty
		cs := newTestSession(t)
		cs.ws = cs.newWorkingSet(1)
		appendTestTurn(cs, 1, "old "+strings.Repeat("x", 3000), "old answer")
		appendTestTurn(cs, 2, "current", "current answer")
		before := len(cs.Request.Messages)

		if err := cs.Compact(context.Background()); err == nil {
			t.Fatal("expected error for empty digest")
		}
		if len(cs.Request.Messages) != before {
			t.Error("a failed compact must leave history unchanged")
		}
	})
}

func TestClearResetsSession(t *testing.T) {
	cs := newTestSession(t)
	cs.Request.Model = "switched-model"
	cs.Request.BaseURL = "http://somewhere:1234"
	cs.LastPromptTokens = 9000
	cs.Append(Message{Role: RoleUser, Content: "old work"})
	oldID := cs.SessionID

	cs.Clear()
	defer cs.transcript.Close()

	if n := len(cs.Request.Messages); n != 1 || cs.Request.Messages[0].Role != RoleSystem {
		t.Errorf("cleared history = %d messages, want just the system seed", n)
	}
	if cs.Request.Model != "switched-model" || cs.Request.BaseURL != "http://somewhere:1234" {
		t.Error("clear must preserve the model binding")
	}
	if cs.LastPromptTokens != 0 {
		t.Error("clear must reset the gauge")
	}
	if cs.SessionID == oldID {
		t.Error("clear should start a new session id")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir(), oldID+".jsonl")); err != nil {
		t.Errorf("old transcript should stay on disk: %v", err)
	}
}

// Same-second sessions (compact and clear do this routinely) must get
// distinct transcript files, not interleave into one.
func TestStartTranscriptCollisionSafe(t *testing.T) {
	t.Chdir(t.TempDir())
	a := &CortexSession{Request: CortexArgs{}.Request()}
	b := &CortexSession{Request: CortexArgs{}.Request()}
	a.StartTranscript()
	b.StartTranscript()
	defer a.transcript.Close()
	defer b.transcript.Close()

	if a.SessionID == "" || b.SessionID == "" {
		t.Fatal("both sessions should persist")
	}
	if a.SessionID == b.SessionID {
		t.Errorf("same-second sessions share id %q", a.SessionID)
	}
}

// core messages.
func TestLoadTranscriptBackCompat(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := sessionsDir()
	os.MkdirAll(dir, 0755)
	// Legacy line: {ts, role, content} with no "kind".
	legacy := `{"ts":"2026-01-01T00:00:00Z","role":"user","content":"legacy turn"}` + "\n"
	path := filepath.Join(dir, "20260101-000000.jsonl")
	os.WriteFile(path, []byte(legacy), 0644)

	msgs, turns, err := loadTranscript(path)
	if err != nil {
		t.Fatalf("loadTranscript: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "legacy turn" {
		t.Errorf("legacy (kind-less) entry should replay as a core message, got %+v", msgs)
	}
	if len(turns) != 1 || turns[0] != 0 {
		t.Errorf("legacy entry should have turn=0 stamp, got %v", turns)
	}
}

func TestResumeReplaysWorkingSet(t *testing.T) {
	t.Run("stamped transcript replays spans", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)

		// Build transcript with turn stamps
		var lines []string
		lines = append(lines, `{"kind":"message","role":"system","content":"s"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"user","content":"first question"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"assistant","content":"first answer"}`)
		lines = append(lines, `{"kind":"message","turn":2,"role":"user","content":"second question"}`)
		lines = append(lines, `{"kind":"message","turn":2,"role":"assistant","content":"second answer"}`)

		path := filepath.Join(dir, "test-session.jsonl")
		content := strings.Join(lines, "\n") + "\n"
		os.WriteFile(path, []byte(content), 0644)

		cs := &CortexSession{Window: 1000, Request: &AgentRequest{Messages: []Message{}}}
		if err := cs.ResumeTranscript("test-session"); err != nil {
			t.Fatalf("ResumeTranscript: %v", err)
		}
		defer cs.transcript.Close()

		// Verify messages loaded
		if len(cs.Request.Messages) != 5 {
			t.Errorf("len(cs.Request.Messages) = %d, want 5", len(cs.Request.Messages))
		}

		// Verify turns count
		if cs.turns != 2 {
			t.Errorf("cs.turns = %d, want 2", cs.turns)
		}

		// Verify working set: frontier should be at 1 (after seed/system message)
		frontier := cs.ws.FrontierMsg()
		if frontier != 1 {
			t.Errorf("cs.ws.FrontierMsg() = %d, want 1", frontier)
		}

		// Verify TailTokens > 0 (spans were recorded)
		if cs.ws.TailTokens() == 0 {
			t.Error("cs.ws.TailTokens() = 0, want > 0")
		}
	})

	t.Run("legacy unstamped transcript stays hydrated", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)

		// Build transcript without turn stamps
		var lines []string
		lines = append(lines, `{"kind":"message","role":"system","content":"s"}`)
		lines = append(lines, `{"kind":"message","role":"user","content":"first question"}`)
		lines = append(lines, `{"kind":"message","role":"assistant","content":"first answer"}`)
		lines = append(lines, `{"kind":"message","role":"user","content":"second question"}`)
		lines = append(lines, `{"kind":"message","role":"assistant","content":"second answer"}`)

		path := filepath.Join(dir, "legacy-session.jsonl")
		content := strings.Join(lines, "\n") + "\n"
		os.WriteFile(path, []byte(content), 0644)

		cs := &CortexSession{Window: 1000, Request: &AgentRequest{Messages: []Message{}}}
		if err := cs.ResumeTranscript("legacy-session"); err != nil {
			t.Fatalf("ResumeTranscript: %v", err)
		}
		defer cs.transcript.Close()

		// Verify messages loaded
		if len(cs.Request.Messages) != 5 {
			t.Errorf("len(cs.Request.Messages) = %d, want 5", len(cs.Request.Messages))
		}

		// Verify turns count is 0 for unstamped
		if cs.turns != 0 {
			t.Errorf("cs.turns = %d, want 0", cs.turns)
		}

		// Verify no spans recorded
		if cs.ws.TailTokens() != 0 {
			t.Errorf("cs.ws.TailTokens() = %d, want 0", cs.ws.TailTokens())
		}

		// Verify frontier equals message count
		if cs.ws.FrontierMsg() != len(cs.Request.Messages) {
			t.Errorf("cs.ws.FrontierMsg() = %d, want %d", cs.ws.FrontierMsg(), len(cs.Request.Messages))
		}
	})

	t.Run("gapped stamps fall back safely", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)

		// Build transcript with gapped stamps (turn 1, then turn 3)
		var lines []string
		lines = append(lines, `{"kind":"message","role":"system","content":"s"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"user","content":"first question"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"assistant","content":"first answer"}`)
		lines = append(lines, `{"kind":"message","turn":3,"role":"user","content":"third question"}`)
		lines = append(lines, `{"kind":"message","turn":3,"role":"assistant","content":"third answer"}`)

		path := filepath.Join(dir, "gapped-session.jsonl")
		content := strings.Join(lines, "\n") + "\n"
		os.WriteFile(path, []byte(content), 0644)

		cs := &CortexSession{Window: 1000, Request: &AgentRequest{Messages: []Message{}}}
		if err := cs.ResumeTranscript("gapped-session"); err != nil {
			t.Fatalf("ResumeTranscript: %v", err)
		}
		defer cs.transcript.Close()

		// Verify no spans recorded due to invalid sequence
		if cs.ws.TailTokens() != 0 {
			t.Errorf("cs.ws.TailTokens() = %d, want 0", cs.ws.TailTokens())
		}

		// Verify frontier equals message count
		if cs.ws.FrontierMsg() != len(cs.Request.Messages) {
			t.Errorf("cs.ws.FrontierMsg() = %d, want %d", cs.ws.FrontierMsg(), len(cs.Request.Messages))
		}

		// Verify turns is the max stamp (3)
		if cs.turns != 3 {
			t.Errorf("cs.turns = %d, want 3", cs.turns)
		}
	})
}
