// discord_turn_test.go — end-to-end runTurn coverage against a scripted
// backend (turnTestBackend, serve_turn_test.go's pattern): an ordinary turn
// completing with progress-as-edits, and a /stop-style interrupt actually
// canceling an in-flight turn's context (Phase 7 sub-item 4's DoD, proven
// through the real Turn/runLoop path rather than just setCancel/interrupt in
// isolation, which discord_manager_test.go already covers).
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// discordTurnSessionFactory is turnTestSessionFactory (serve_turn_test.go)
// plus EnableMemory, matching newDiscordSessionFactory's real behavior
// closely enough for these tests (memory tools never fire against the fixed
// scripted reply).
func discordTurnSessionFactory(t *testing.T, backend *turnTestBackend) sessionFactory {
	t.Helper()
	root := t.TempDir()
	base := turnTestSessionFactory(backend)
	return func() *CortexSession {
		cs := base()
		cs.workspace = mustWorkspace(t, root)
		cs.deleteRoot = root
		return cs
	}
}

func mustWorkspace(t *testing.T, root string) *Workspace {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return ws
}

// TestDiscordRunTurnPostsProgressAndFinalReply proves an ordinary turn: the
// status message starts, gets edited to a final "done" marker, and the
// actual reply is sent as its own message.
func TestDiscordRunTurnPostsProgressAndFinalReply(t *testing.T) {
	backend := newTurnTestBackend(t)
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, discordTurnSessionFactory(t, backend))
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	bot.runTurn("chan1", "hello")

	replies := api.sentTo("chan1")
	if len(replies) != 2 { // the initial "⏳ working…" status message, then the reply
		t.Fatalf("sentTo(chan1) = %v, want 2 messages (status + reply)", replies)
	}
	if replies[len(replies)-1] != "ok" {
		t.Errorf("final reply = %q, want %q (the backend's scripted content)", replies[len(replies)-1], "ok")
	}
	if len(api.edits) == 0 {
		t.Fatal("no progress-status edits recorded")
	}
	last := api.edits[len(api.edits)-1]
	if last.content != "✓ done" {
		t.Errorf("final status edit = %q, want %q", last.content, "✓ done")
	}

	bot.mu.Lock()
	_, stillTracked := bot.statusMsg["chan1"]
	bot.mu.Unlock()
	if stillTracked {
		t.Error("finishProgress must forget the channel's status message once the turn ends")
	}
}

// TestDiscordInterruptCancelsAnInFlightTurn drives runTurn against a
// deliberately slow backend on its own goroutine, interrupts it mid-flight
// (the same setCancel/interrupt path a /stop command or 🛑 reaction uses),
// and asserts the turn actually unwinds as "🛑 interrupted" rather than
// completing normally — the real end-to-end proof behind
// TestDiscordInterruptCancelsTheTurnsContext's unit-level check.
func TestDiscordInterruptCancelsAnInFlightTurn(t *testing.T) {
	backend := newSlowTurnTestBackend(t, 300*time.Millisecond)
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, discordTurnSessionFactory(t, backend))
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	done := make(chan struct{})
	go func() {
		bot.runTurn("chan1", "do something slow")
		close(done)
	}()

	// Wait until the turn has actually registered its cancel func (proof the
	// backend request is in flight) before interrupting it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		bot.mu.Lock()
		_, live := bot.cancels["chan1"]
		bot.mu.Unlock()
		if live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runTurn never registered a cancel func")
		}
		time.Sleep(time.Millisecond)
	}

	if got := bot.interrupt("chan1"); !strings.Contains(got, "stopping") {
		t.Fatalf("interrupt() = %q, want a stopping acknowledgment", got)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runTurn did not return after interrupt — ctx cancel did not propagate")
	}

	replies := api.sentTo("chan1")
	if len(replies) == 0 || replies[len(replies)-1] != "🛑 interrupted" {
		t.Errorf("final reply = %v, want the last message to be \"🛑 interrupted\"", replies)
	}
}

// newSlowTurnTestBackend is turnTestBackend's fixed-reply shape with a
// configurable delay before responding, long enough to give an interrupt a
// real window to land mid-flight. The delay only ever elapses if the client
// doesn't cancel first — ctx cancellation aborts the client-side wait
// immediately regardless of how long the (stub) server would have taken.
func newSlowTurnTestBackend(t *testing.T, delay time.Duration) *turnTestBackend {
	t.Helper()
	b := &turnTestBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	t.Cleanup(b.srv.Close)
	return b
}
