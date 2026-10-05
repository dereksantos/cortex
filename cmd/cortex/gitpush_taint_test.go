package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/shellrisk"
)

// TestGitPushTaintFloor is the issue #102 git-push floor at the gate: a
// Safe verdict on `git push` runs on a clean turn (today's behavior — the
// classifier's safe list covers ordinary pushes) and PROMPTS on a tainted
// one, with the floor's reason in the prompt line and the taint note
// appended; the headless twin reads the taint-blocked message. The floor
// RAISES and never Blocks: an approving human still runs the push.
// Detection is git-scoped like EffectClass — other tools' pushes are the
// classifier's business, not this floor's.
func TestGitPushTaintFloor(t *testing.T) {
	// judgeSafeEverywhere: the judge waves everything through, so on a
	// tainted turn the floor is the ONLY thing that catches a push (and
	// the only thing that could over-reach, which the last sub-test pins
	// against).
	judgeSafeEverywhere := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test: safe", nil
	}
	yes := true

	t.Run("clean turn runs a judge-held-safe push", func(t *testing.T) {
		cs := &CortexSession{turnNo: 1, classifyShell: judgeSafeEverywhere, quiet: true}
		if msg, ok := cs.gateShell(context.Background(), "git push origin main"); !ok {
			t.Fatalf("a clean-turn safe push must run, got %q", msg)
		}
	})

	t.Run("tainted turn prompts instead of running", func(t *testing.T) {
		for _, cmd := range []string{
			"git push origin main",        // gray-zone: judged Safe, floor raises
			"git push",                    // gray-zone, bare form
			"git commit -m x && git push", // chained: floor sees the push leg
		} {
			cs := &CortexSession{turnNo: 2, classifyShell: judgeSafeEverywhere}
			var question string
			cs.confirmRisky = func(q string) bool { question = q; return false }
			cs.recordUntrustedContent("fetch_url")
			msg, ok := cs.gateShell(context.Background(), cmd)
			if ok {
				t.Errorf("%q: a tainted-turn push must not auto-run", cmd)
			}
			if !strings.Contains(msg, "declined") {
				t.Errorf("%q: expected the decline message, got %q", cmd, msg)
			}
			if !strings.Contains(question, shellrisk.GitPushTaintReason) {
				t.Errorf("%q: prompt must carry the floor's reason: %q", cmd, question)
			}
			if !strings.Contains(question, "untrusted web content entered this turn (fetch_url)") {
				t.Errorf("%q: prompt must name the taint: %q", cmd, question)
			}
		}
	})

	t.Run("tainted headless push reads the taint block, not an ordinary refuse", func(t *testing.T) {
		cs := &CortexSession{turnNo: 3, classifyShell: judgeSafeEverywhere, quiet: true}
		cs.recordUntrustedContent("web_search")
		msg, ok := cs.gateShell(context.Background(), "git push")
		if ok {
			t.Fatal("a headless tainted-turn push must not run")
		}
		if !strings.Contains(msg, "blocked (untrusted content this turn: web_search)") {
			t.Errorf("expected the taint blocked message, got %q", msg)
		}
		// The headless refusal is the shared taint message, which names the
		// CAUSE (the turn's web sources), not the individual floor — the
		// floor's GitPushTaintReason surfaces in the interactive prompt
		// line. Assert the message shape, not a floor-specific reason.
		if strings.Contains(msg, "blocked (risk:") {
			t.Errorf("a floored push must not read as the ordinary risk block: %q", msg)
		}
	})

	t.Run("the floor raises to Risky, never Blocks: an approving human runs it", func(t *testing.T) {
		cs := &CortexSession{turnNo: 4, classifyShell: judgeSafeEverywhere}
		cs.confirmRisky = func(string) bool { return yes }
		cs.recordUntrustedContent("fetch_url")
		if msg, ok := cs.gateShell(context.Background(), "git push origin main"); !ok {
			t.Errorf("an explicitly approved tainted push must run, got %q", msg)
		}
	})

	t.Run("non-push commands are untouched by the floor", func(t *testing.T) {
		cs := &CortexSession{turnNo: 5, classifyShell: judgeSafeEverywhere, quiet: true}
		cs.recordUntrustedContent("fetch_url")
		// docker/npm are not git-scoped (the floor's detector is git-only,
		// like EffectClass), and the judge holds them Safe, so they run —
		// pinning that the FLOOR claims only git pushes and cannot balloon
		// into blocking every outbound command on a taint.
		for _, cmd := range []string{"ls", "git status", "docker push img", "npm publish", "git log push"} {
			if msg, ok := cs.gateShell(context.Background(), cmd); !ok {
				t.Errorf("%q: a judge-held-safe non-push must still run on a tainted turn, got %q", cmd, msg)
			}
		}
	})
}

// TestClassifierNoteThreading pins that the session threads the taint's
// source list into every classifier call: the gray-zone judge sees the
// shellrisk.TaintNote on a tainted turn and "" on a clean one, on the
// first pass and on the tainted re-examination alike.
func TestClassifierNoteThreading(t *testing.T) {
	var notes []string
	recordJudge := func(_ context.Context, _, note string) (shellrisk.Level, string, error) {
		notes = append(notes, note)
		return shellrisk.Safe, "test: safe", nil
	}

	cs := &CortexSession{turnNo: 1, classifyShell: recordJudge, quiet: true}
	if _, ok := cs.gateShell(context.Background(), "mv a.txt b.txt"); !ok {
		t.Fatal("clean gray-zone safe command should run")
	}
	if len(notes) != 1 || notes[0] != "" {
		t.Fatalf("clean turn must pass one empty note, got %q", notes)
	}

	notes = nil
	cs.recordUntrustedContent("fetch_url")
	cs.recordUntrustedContent("web_search")
	if _, ok := cs.gateShell(context.Background(), "mv a.txt b.txt"); !ok {
		t.Fatal("tainted gray-zone command still runs while the judge holds it Safe")
	}
	want := shellrisk.TaintNote([]string{"fetch_url", "web_search"})
	if len(notes) < 2 {
		t.Fatalf("a tainted turn re-examines the gray-zone verdict: want at least 2 classifier calls, got %d", len(notes))
	}
	for i, n := range notes {
		if n != want {
			t.Errorf("classifier call %d on the tainted turn saw note %q, want %q", i, n, want)
		}
	}
}
