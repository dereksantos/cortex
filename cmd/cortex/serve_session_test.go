// M4.2b1 — SessionManager: live create/resume session lifecycle over
// `cortex serve` (GOAL.md §6 M4.2, split into M4.2b1/b2/b3 per STATE.md).
// Turn/SSE (M4.2b2) is not exercised here.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/registry"
)

// fakeClock is a manually-advanced clock for the M4.7 idle-eviction tests —
// tests never sleep (matches M6.2's later "injected clock" convention this
// increment establishes first).
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// hermeticSessionFactory returns a sessionFactory that never touches real
// config or the network (CortexArgs{}.Request() ignores its receiver; no
// BaseURL is needed since M4.2b1 never runs a turn) — the same hand-built
// *CortexSession{} pattern greeting_test.go established for hermetic
// session tests.
func hermeticSessionFactory() sessionFactory {
	return func() *CortexSession {
		return &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	}
}

func TestSessionManagerCreateStartsAndTracksNewSession(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	ms, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ms.ID() == "" {
		t.Fatal("Create() left the session id empty")
	}

	if _, err := os.Stat(filepath.Join(root, ".cortex", "sessions", ms.ID()+".jsonl")); err != nil {
		t.Errorf("transcript file not written for %s: %v", ms.ID(), err)
	}

	got, ok := mgr.Get(ms.ID())
	if !ok {
		t.Fatal("Get() did not find the session Create() just tracked")
	}
	if got != ms {
		t.Error("Get() returned a different *managedSession than Create() produced")
	}
}

func TestSessionManagerCreateUnknownProjectReturnsTypedError(t *testing.T) {
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	if _, err := mgr.Create("nope"); err == nil {
		t.Fatal("Create(unregistered project) = nil error, want registry.ErrProjectNotFound")
	} else if !strings.Contains(err.Error(), "nope") {
		t.Errorf("Create error %q does not mention the unknown project name", err)
	}
}

func TestSessionManagerResumeOfLiveSessionReturnsSamePointerWithoutReopening(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resumed, err := mgr.Resume("blog", created.ID())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed != created {
		t.Error("Resume() of an already-live session id returned a different *managedSession, want the same live one (no reopen)")
	}
}

// TestSessionManagerResumeRehydratesFromTranscriptAfterRestart proves the
// "serve owns no state" invariant (GOAL.md M4.6) at the SessionManager
// level: a second, independent manager instance — standing in for a serve
// restart — resumes the same session id straight from the transcript file
// the first manager's Create wrote, recovering the same messages.
func TestSessionManagerResumeRehydratesFromTranscriptAfterRestart(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}

	firstMgr := NewSessionManager(reg, hermeticSessionFactory())
	created, err := firstMgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantMessages := len(created.cs.Request.Messages)
	created.cs.transcript.Close() // simulate the process (and its transcript lock) going away

	secondMgr := NewSessionManager(reg, hermeticSessionFactory())
	resumed, err := secondMgr.Resume("blog", created.ID())
	if err != nil {
		t.Fatalf("Resume after restart: %v", err)
	}
	if resumed.ID() != created.ID() {
		t.Errorf("resumed id = %q, want %q", resumed.ID(), created.ID())
	}
	if got := len(resumed.cs.Request.Messages); got != wantMessages {
		t.Errorf("resumed session has %d messages, want %d (rehydrated from transcript)", got, wantMessages)
	}
}

func TestSessionManagerResumeUnknownIDReturnsError(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	if _, err := mgr.Resume("blog", "does-not-exist"); err == nil {
		t.Fatal("Resume(unknown id) = nil error, want an error")
	}
}

// TestSessionManagerEvictsIdleSessionAndResumeRehydrates covers M4.7: a
// session idle past the (shrunk, test-only) threshold is evicted from the
// live map on the next Get(), and a subsequent Resume() re-hydrates it from
// the on-disk transcript exactly like the M4.6-proven restart path does —
// eviction is just "the manager forgot it was live," not data loss.
func TestSessionManagerEvictsIdleSessionAndResumeRehydrates(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	clock := &fakeClock{now: time.Now()}
	mgr.SetClock(clock.Now)
	mgr.SetIdleTimeout(time.Second)

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantMessages := len(created.cs.Request.Messages)

	if _, ok := mgr.Get(created.ID()); !ok {
		t.Fatal("Get() evicted the session before the idle threshold elapsed")
	}

	clock.Advance(2 * time.Second)

	if _, ok := mgr.Get(created.ID()); ok {
		t.Fatal("Get() still returns the session past the idle threshold, want eviction")
	}

	resumed, err := mgr.Resume("blog", created.ID())
	if err != nil {
		t.Fatalf("Resume after eviction: %v", err)
	}
	if resumed.ID() != created.ID() {
		t.Errorf("resumed id = %q, want %q", resumed.ID(), created.ID())
	}
	if got := len(resumed.cs.Request.Messages); got != wantMessages {
		t.Errorf("resumed session has %d messages, want %d (rehydrated from transcript)", got, wantMessages)
	}
}

// TestSessionManagerZeroIdleTimeoutNeverEvicts pins the default (unset)
// behavior every pre-M4.7 test already relies on: idleTimeout's zero value
// disables eviction entirely, so SetIdleTimeout/SetClock are opt-in and
// every existing NewSessionManager(reg, factory) call site is unaffected.
func TestSessionManagerZeroIdleTimeoutNeverEvicts(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	clock := &fakeClock{now: time.Now()}
	mgr.SetClock(clock.Now)

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	clock.Advance(365 * 24 * time.Hour)

	if _, ok := mgr.Get(created.ID()); !ok {
		t.Fatal("Get() evicted the session even though no idle timeout was set")
	}
}

// TestSessionManagerTouchExtendsIdleWindow proves Touch (called by
// handleTurn/handleTurnStream on every request against a live session)
// resets the idle clock, so a session under continuous use is never evicted
// out from under an in-flight conversation.
func TestSessionManagerTouchExtendsIdleWindow(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	clock := &fakeClock{now: time.Now()}
	mgr.SetClock(clock.Now)
	mgr.SetIdleTimeout(2 * time.Second)

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	clock.Advance(1500 * time.Millisecond)
	mgr.Touch(created.ID())
	clock.Advance(1500 * time.Millisecond) // 3s since Create, but only 1.5s since Touch

	if _, ok := mgr.Get(created.ID()); !ok {
		t.Fatal("Touch() did not extend the idle window; session was evicted early")
	}
}

func TestCreateSessionEndpointCreatesNewSession(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == "" {
		t.Error("response id is empty")
	}
	if got.Resumed {
		t.Error("Resumed = true for a fresh create, want false")
	}
	if _, ok := mgr.Get(got.ID); !ok {
		t.Errorf("session %s not tracked live by the manager after create", got.ID)
	}
}

func TestCreateSessionEndpointResumesWhenBodyNamesID(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := strings.NewReader(`{"resume":"` + created.ID() + `"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID() {
		t.Errorf("resumed id = %q, want %q", got.ID, created.ID())
	}
	if !got.Resumed {
		t.Error("Resumed = false for a resume request, want true")
	}
}

func TestCreateSessionEndpointUnknownProjectReturns404(t *testing.T) {
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/doesnotexist/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCreateSessionEndpointRejectsForeignHost(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doForeignHost(t, http.MethodPost, ts.URL+"/api/projects/blog/sessions", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
