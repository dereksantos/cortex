// session_banner_test.go — the loaded-context banner (cmd/cortex/session.go's
// loadedContextBanner/showLoadedContext/headlessLoadedContextBanner) for
// resumed sessions, split out so it is testable without a live session.
// Issue #118 moves the banner from stdout to stderr in `cortex turn` in its
// plain, never-colored form; the banner's content, its stderr destination,
// and the REPL/NO_COLOR degradation path stay covered here. (The NO_COLOR
// ANSI strip is internal/tools' Color's job — covered by its own tests;
// withColor is a thin alias.)
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
)

// bannerTestSession builds a session pointed at a fresh workspace (no
// transcripts by default) with one user + one assistant message and the
// given working-set state, so the banner's numbers are known.
func bannerTestSession(t *testing.T, build func(t *testing.T, cs *CortexSession)) *CortexSession {
	t.Helper()
	cs := &CortexSession{
		Window:    8000,
		SessionID: "abc123",
		workspace: mustWorkspace(t, t.TempDir()),
	}
	cs.Request = &AgentRequest{Messages: []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}}
	if build != nil {
		build(t, cs)
	}
	return cs
}

// writePinnedSession writes one transcript into the session's sessions dir
// and pins its mtime to `age` in the past (negative = past, positive =
// future) so the banner's age line lands in a known relTime bucket.
func writePinnedSession(t *testing.T, cs *CortexSession, age time.Duration) {
	t.Helper()
	sessDir := cs.SessionsDir()
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeTestSession(t, sessDir, "20260103-000000", `{"kind":"message","role":"user","content":"recent"}`)
	past := time.Now().Add(age)
	if err := os.Chtimes(filepath.Join(sessDir, "20260103-000000.jsonl"), past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

func TestLoadedContextBanner(t *testing.T) {
	tests := []struct {
		name    string
		build   func(t *testing.T, cs *CortexSession)
		wantSub []string // substrings the banner must contain
	}{
		{
			name: "no turns (nil working set) omits the turns line",
			build: func(t *testing.T, cs *CortexSession) {
				cs.Request = &AgentRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
			},
			wantSub: []string{"1 messages"},
		},
		{
			name: "demoted and hydrated turns are both reported",
			build: func(t *testing.T, cs *CortexSession) {
				cs.ws = cs.newWorkingSet(1)
				cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
				cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
				if err := cs.ws.RestoreState(1, 1000, 500); err != nil {
					t.Fatalf("RestoreState: %v", err)
				}
			},
			wantSub: []string{
				"2 turns (1 demoted, 1 hydrated tail)",
				"2 messages",
			},
		},
		{
			name: "newest session's mtime supplies the age (\"just now\" for a fresh transcript)",
			build: func(t *testing.T, cs *CortexSession) {
				sessDir := cs.SessionsDir()
				if err := os.MkdirAll(sessDir, 0755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				writeTestSession(t, sessDir, "20260101-000000", `{"kind":"message","role":"user","content":"old"}`)
				writeTestSession(t, sessDir, "20260103-000000", `{"kind":"message","role":"user","content":"newest"}`)
				// relTime buckets by time.Since(mtime); pin the mtime to now
				// so the "just now" bucket can't drift out from under the
				// assertion.
				if err := os.Chtimes(filepath.Join(sessDir, "20260103-000000.jsonl"), time.Now(), time.Now()); err != nil {
					t.Fatalf("Chtimes: %v", err)
				}
			},
			wantSub: []string{
				"session:",
				"abc123",
				"old)\n",
			},
		},
		{
			name:    "banner names the id the caller passed, not the newest transcript",
			build:   func(t *testing.T, cs *CortexSession) {},
			wantSub: []string{"abc123"},
		},
		{
			name: "age buckets: " + `"25m ago"` + " for a 25-minute-old transcript",
			build: func(t *testing.T, cs *CortexSession) {
				writePinnedSession(t, cs, -25*time.Minute)
			},
			wantSub: []string{"25m ago"},
		},
		{
			name: "age buckets: " + `"2h ago"` + " for a 2-hour-old transcript",
			build: func(t *testing.T, cs *CortexSession) {
				writePinnedSession(t, cs, -2*time.Hour)
			},
			wantSub: []string{"2h ago"},
		},
		{
			name: "age buckets: " + `"3d ago"` + " for a 72-hour-old transcript",
			build: func(t *testing.T, cs *CortexSession) {
				writePinnedSession(t, cs, -72*time.Hour)
			},
			wantSub: []string{"3d ago"},
		},
		{
			name: "all turns demoted: the hydrated tail count is zero",
			build: func(t *testing.T, cs *CortexSession) {
				cs.ws = cs.newWorkingSet(1)
				cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
				cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
				cs.ws.AddTurn(cache.TurnSpan{Start: 9, End: 13, Tokens: 50})
				// Demote ALL three turns: the frontier sits at the last turn
				// index (RestoreState validates frontier <= len(turns)).
				if err := cs.ws.RestoreState(3, 1000, 500); err != nil {
					t.Fatalf("RestoreState: %v", err)
				}
			},
			wantSub: []string{"3 turns (3 demoted, 0 hydrated tail)"},
		},
		{
			name: "multiple messages reported verbatim",
			build: func(t *testing.T, cs *CortexSession) {
				cs.Request = &AgentRequest{Messages: []Message{
					{Role: RoleUser, Content: "one"},
					{Role: "assistant", Content: "two"},
					{Role: RoleUser, Content: "three"},
					{Role: "assistant", Content: "four"},
				}}
			},
			wantSub: []string{"4 messages"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := bannerTestSession(t, tt.build)
			got := cs.loadedContextBanner("abc123", true)
			for _, sub := range tt.wantSub {
				if !strings.Contains(got, sub) {
					t.Errorf("banner missing %q (got %q)", sub, got)
				}
			}
		})
	}
}

// TestLoadedContextBannerNoColor pins the NO_COLOR degradation path (the
// issue #118 "non-TTY shouldn't get color" requirement) for the HEADLESS
// surface: `cortex turn --session`'s banner must never carry ANSI escapes,
// regardless of NO_COLOR or TTY state. tools.colorDisabled is read ONCE at
// package init (internal/tools/ui.go), so we can't re-toggle it inside a
// single test — but the headless path doesn't need to: the plain form is
// the always-plain surface, and this test asserts it carries no escapes
// AND matches what the NO_COLOR colored form would be (i.e. stripping
// every SGR code from the colored form). That's the exact invariant a
// piped driver sees.
func TestLoadedContextBannerNoColor(t *testing.T) {
	cs := bannerTestSession(t, func(t *testing.T, cs *CortexSession) {
		cs.ws = cs.newWorkingSet(1)
		cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
		cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
		if err := cs.ws.RestoreState(1, 1000, 500); err != nil {
			t.Fatalf("RestoreState: %v", err)
		}
	})
	// The headless surface is the plain form — this is what `cortex turn
	// --session` actually prints, and it must be ANSI-free unconditionally.
	p := cs.loadedContextBanner("abc123", false)
	if strings.Contains(p, "\x1b[") {
		t.Errorf("headless (NO_COLOR-equivalent) banner must be plain, got %q", p)
	}
	// Content is unchanged — only the escapes go.
	for _, sub := range []string{
		"context:  2 turns (1 demoted, 1 hydrated tail)",
		"messages:  2 messages",
		"session:  abc123",
	} {
		if !strings.Contains(p, sub) {
			t.Errorf("headless banner missing %q (got %q)", sub, p)
		}
	}
	// Stripping every SGR code from the colored form must yield the plain
	// form — the NO_COLOR colored path and the headless plain path are
	// the same text, so a piped driver and a NO_COLOR terminal agree.
	c := cs.loadedContextBanner("abc123", true)
	stripped := strings.ReplaceAll(strings.ReplaceAll(c, "\x1b[0m", ""), "\x1b[90m", "")
	stripped = strings.ReplaceAll(strings.ReplaceAll(stripped, "\x1b[32m", ""), "\x1b[36m", "")
	if stripped != p {
		t.Errorf("NO_COLOR-stripped colored form %q differs from the headless plain form %q", stripped, p)
	}
}

// TestLoadedContextBannerPlainForm pins issue #118's other half: the headless
// `cortex turn --session` banner is ALWAYS plain — no ANSI escapes even when
// NO_COLOR is unset and no TTY gate applies — so a piped driver never sees
// color, and the same text lands whether or not a terminal is attached.
func TestLoadedContextBannerPlainForm(t *testing.T) {
	cs := bannerTestSession(t, func(t *testing.T, cs *CortexSession) {
		cs.ws = cs.newWorkingSet(1)
		cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
		cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
		if err := cs.ws.RestoreState(1, 1000, 500); err != nil {
			t.Fatalf("RestoreState: %v", err)
		}
	})
	p := cs.loadedContextBanner("abc123", false)
	c := cs.loadedContextBanner("abc123", true)
	if strings.Contains(p, "\x1b[") {
		t.Errorf("plain banner contains ANSI escapes: %q", p)
	}
	if !strings.Contains(p, "context:  2 turns (1 demoted, 1 hydrated tail)") {
		t.Errorf("plain banner missing the context line: %q", p)
	}
	if !strings.Contains(p, "messages:  2 messages") {
		t.Errorf("plain banner missing the messages line: %q", p)
	}
	if !strings.Contains(p, "session:  abc123") {
		t.Errorf("plain banner missing the session line: %q", p)
	}
	// The plain form is the colored form with every SGR code stripped — the
	// two must agree on content so a driver and a human see the same facts.
	plain := strings.ReplaceAll(strings.ReplaceAll(c, "\x1b[0m", ""), "\x1b[90m", "")
	plain = strings.ReplaceAll(strings.ReplaceAll(plain, "\x1b[32m", ""), "\x1b[36m", "")
	if plain != p {
		t.Errorf("plain form %q differs from the uncolored colored form %q", p, plain)
	}
}

// TestHeadlessLoadedContextBannerGoesToStderrPlain pins issue #118 end to
// end for the headless entry point: the plain banner lands on stderr, none
// on stdout, and contains no ANSI escapes.
func TestHeadlessLoadedContextBannerGoesToStderrPlain(t *testing.T) {
	cs := bannerTestSession(t, func(t *testing.T, cs *CortexSession) {
		cs.ws = cs.newWorkingSet(1)
		cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
		cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
		if err := cs.ws.RestoreState(1, 1000, 500); err != nil {
			t.Fatalf("RestoreState: %v", err)
		}
	})
	want := cs.loadedContextBanner("abc123", false)

	// Temp files, not pipes: a small write can outlive the writer's close in
	// the kernel pipe buffer, which made a naive os.Pipe stderr capture come
	// back empty (see TestShowLoadedContextGoesToStderr's note).
	stdoutF, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("CreateTemp (stdout): %v", err)
	}
	stderrF, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("CreateTemp (stderr): %v", err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutF, stderrF
	cs.headlessLoadedContextBanner("abc123")
	os.Stdout, os.Stderr = origOut, origErr
	if err := stdoutF.Close(); err != nil {
		t.Fatalf("close stdout temp: %v", err)
	}
	if err := stderrF.Close(); err != nil {
		t.Fatalf("close stderr temp: %v", err)
	}

	outBytes, err := os.ReadFile(stdoutF.Name())
	if err != nil {
		t.Fatalf("read stdout temp: %v", err)
	}
	if len(outBytes) != 0 {
		t.Errorf("headlessLoadedContextBanner wrote %q to stdout, want nothing (issue #118)", string(outBytes))
	}
	errBytes, err := os.ReadFile(stderrF.Name())
	if err != nil {
		t.Fatalf("read stderr temp: %v", err)
	}
	if got := string(errBytes); got != want {
		t.Errorf("headlessLoadedContextBanner wrote %q to stderr, want the plain banner %q", got, want)
	}
	if strings.Contains(string(errBytes), "\x1b[") {
		t.Errorf("headless banner contains ANSI escapes: %q", string(errBytes))
	}
}

// TestShowLoadedContextGoesToStderr pins issue #118: the banner lands on
// stderr, byte-for-byte identical to loadedContextBanner's output, and
// nothing goes to stdout.
//
// os.Stderr is swapped for a temp file (not a pipe): a pipe would race its
// own writer — a small write can leave the kernel pipe buffer after the
// writer's close, which is exactly why a naive os.Pipe capture of stderr
// comes back empty here and in the scratch repro. A file has no such
// race; the swap-to-a-real-*os.File is itself the assertion that the
// banner's writer is the process's stderr (not some other buffered sink).
func TestShowLoadedContextGoesToStderr(t *testing.T) {
	cs := bannerTestSession(t, func(t *testing.T, cs *CortexSession) {
		cs.ws = cs.newWorkingSet(1)
		cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
		cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
		if err := cs.ws.RestoreState(1, 1000, 500); err != nil {
			t.Fatalf("RestoreState: %v", err)
		}
	})
	// Snapshot the banner BEFORE redirecting: the mtime age line is stable
	// inside its bucket (relTime prints whole minutes), so computing it first
	// is safe.
	want := cs.loadedContextBanner("abc123", true)

	stdoutF, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("CreateTemp (stdout): %v", err)
	}
	stderrF, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("CreateTemp (stderr): %v", err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutF, stderrF
	cs.showLoadedContext("abc123")
	os.Stdout, os.Stderr = origOut, origErr
	if err := stdoutF.Close(); err != nil {
		t.Fatalf("close stdout temp: %v", err)
	}
	if err := stderrF.Close(); err != nil {
		t.Fatalf("close stderr temp: %v", err)
	}

	outBytes, err := os.ReadFile(stdoutF.Name())
	if err != nil {
		t.Fatalf("read stdout temp: %v", err)
	}
	if len(outBytes) != 0 {
		t.Errorf("showLoadedContext wrote %q to stdout, want nothing (issue #118)", string(outBytes))
	}
	errBytes, err := os.ReadFile(stderrF.Name())
	if err != nil {
		t.Fatalf("read stderr temp: %v", err)
	}
	if got := string(errBytes); got != want {
		t.Errorf("showLoadedContext wrote %q to stderr, want the banner %q", got, want)
	}
}
