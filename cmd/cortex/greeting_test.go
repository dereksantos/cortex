package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// greetingTestBackend is a minimal scripted Sender seam (the same style as
// context_eval_test.go's contextEvalBackend): it counts requests and answers
// each with a fixed assistant reply, letting the test assert the greeting
// turn fired exactly once (or not at all) without a live model.
type greetingTestBackend struct {
	mu    sync.Mutex
	count int
	srv   *httptest.Server
}

func newGreetingTestBackend(t *testing.T) *greetingTestBackend {
	t.Helper()
	b := &greetingTestBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.count++
		b.mu.Unlock()
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"hello, I'm cortex"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *greetingTestBackend) requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

func newGreetingTestSession(t *testing.T, backend *greetingTestBackend) *CortexSession {
	t.Helper()
	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.Request.BaseURL = backend.srv.URL
	cs.StartTranscript()
	if cs.transcript != nil {
		t.Cleanup(func() { cs.transcript.Close() })
	}
	return cs
}

// TestGreetingFiresOnceOnFirstRun pins GOAL.md M1.5: on first run the
// greeting turn fires exactly once (one round-trip through the scripted
// Sender seam) and leaves the marker so a second MaybeGreet call in the same
// process — mirroring a second launch — fires none.
func TestGreetingFiresOnceOnFirstRun(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	backend := newGreetingTestBackend(t)
	cs := newGreetingTestSession(t, backend)

	if !IsFirstRun() {
		t.Fatal("precondition: expected IsFirstRun() = true before greeting")
	}

	if err := MaybeGreet(context.Background(), cs); err != nil {
		t.Fatalf("MaybeGreet: %v", err)
	}
	if got := backend.requests(); got != 1 {
		t.Fatalf("greeting turn made %d requests, want 1", got)
	}
	if !pathExists(greetingMarkerPath()) {
		t.Fatalf("greeting marker %q not written after MaybeGreet", greetingMarkerPath())
	}
	if IsFirstRun() {
		t.Fatal("IsFirstRun() = true after MaybeGreet, want false (marker should suppress it)")
	}

	// A second call in the same "run" — standing in for the next launch —
	// must not fire the turn again.
	if err := MaybeGreet(context.Background(), cs); err != nil {
		t.Fatalf("second MaybeGreet: %v", err)
	}
	if got := backend.requests(); got != 1 {
		t.Fatalf("greeting turn made %d requests after a second MaybeGreet call, want still 1 (non-first-run fires none)", got)
	}
}

// TestGreetingSkippedWhenNotFirstRun pins the non-first-run half of M1.5:
// a run that isn't first (config.json already present) must not fire the
// greeting turn at all.
func TestGreetingSkippedWhenNotFirstRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	t.Chdir(t.TempDir())

	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("failed to write fixture config.json: %v", err)
	}
	if IsFirstRun() {
		t.Fatal("precondition: expected IsFirstRun() = false with config.json present")
	}

	backend := newGreetingTestBackend(t)
	cs := newGreetingTestSession(t, backend)

	if err := MaybeGreet(context.Background(), cs); err != nil {
		t.Fatalf("MaybeGreet: %v", err)
	}
	if got := backend.requests(); got != 0 {
		t.Fatalf("greeting turn made %d requests on a non-first run, want 0", got)
	}
	if pathExists(greetingMarkerPath()) {
		t.Fatal("greeting marker written on a non-first run, want untouched")
	}
}

// TestGreetingWritesMarkerOnlyAfterSuccess pins that a failed greeting turn
// (backend error) does NOT write the marker, so the next launch can retry —
// MaybeGreet's doc comment promises this ordering.
func TestGreetingWritesMarkerOnlyAfterSuccess(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	t.Cleanup(srv.Close)

	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.Request.BaseURL = srv.URL
	cs.StartTranscript()
	if cs.transcript != nil {
		t.Cleanup(func() { cs.transcript.Close() })
	}

	if err := MaybeGreet(context.Background(), cs); err == nil {
		t.Fatal("MaybeGreet with a failing backend returned nil error, want non-nil")
	}
	if pathExists(greetingMarkerPath()) {
		t.Fatal("greeting marker written despite a failed turn")
	}
	if !IsFirstRun() {
		t.Fatal("IsFirstRun() = false after a failed greeting, want true (should retry next launch)")
	}
}

// TestGreetingPromptGolden pins greetingPrompt verbatim (GOAL.md §3 P1
// greeting: "golden-pinned so any change is a visible diff requiring a
// Decisions entry") and asserts it states principles rather than a task
// recipe: no imperative command list, just an introduction plus a
// consent-gated offer. M1.7 extended the text to also ask where the user's
// code lives (D3's ask half); the golden value below reflects that.
func TestGreetingPromptGolden(t *testing.T) {
	const want = `This is the user's first time running you. Introduce yourself in a ` +
		`few short sentences: who you are (a coding agent with working memory ` +
		`that persists across turns) and how you like to work. Mention, as an ` +
		`offer only, that you can look over their local AI landscape ` +
		`(installed harnesses, runtimes, other projects on this machine) if ` +
		`they'd like — make clear it's read-only and only happens if they say ` +
		`yes. Also ask where their code generally lives on this machine (for ` +
		`example ~/code or ~/projects), so you'll know where to look later if ` +
		`they ever want that survey — this is just information gathering, not ` +
		`the survey itself. Keep it brief and warm; don't recite a menu of ` +
		`commands or tools.`

	if greetingPrompt != want {
		t.Errorf("greetingPrompt changed (golden pin):\ngot:  %q\nwant: %q", greetingPrompt, want)
	}
	if !strings.Contains(greetingPrompt, "if") {
		t.Error("greetingPrompt should gate the landscape offer on consent (\"if\"), not command it unconditionally")
	}
	if !strings.Contains(greetingPrompt, "where their code") {
		t.Error("greetingPrompt should ask where the user's code lives (GOAL.md M1.7 / D3's ask half)")
	}
}
