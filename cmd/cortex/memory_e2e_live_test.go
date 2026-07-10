package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memory_e2e_live_test.go is the end-to-end behavioral acceptance for
// model-driven memory (docs/memory-tools.md). No mocks: it builds the real loop
// binary and drives it across real sessions against the live fleet, with a real
// .cortex/memory/ on disk. It asserts the *behavior* the pivot promises — the
// model saves durable facts, recalls them in a fresh session, updates a stale
// note, and reaches for the journal when no note holds a detail.
//
//	CORTEX_LIVE_FLEET=1 go test ./cmd/cortex/ -run Memory.*_Live -v -timeout 1800s
//
// Each scenario runs in its OWN isolated workspace/.cortex, so one scenario's
// notes never perturb another's prompts (acceptance tests one behavior at a
// time). The facts are deliberately NON-re-derivable (policies, decisions) —
// staleness and recall only bite for facts the model can't recompute from the
// code; a doc-count, by contrast, the model just re-derives and is never misled
// by (the lesson from the first iteration of this eval).
func TestMemoryEndToEnd_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run the live memory end-to-end eval")
	}

	bin := filepath.Join(t.TempDir(), "loop")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build loop: %v\n%s", err, out)
	}

	// scenario returns a fresh isolated workspace and a runTurn bound to it.
	scenario := func(t *testing.T) (memDir string, runTurn func(session, input string) (reply, raw string)) {
		ws := t.TempDir()
		memDir = filepath.Join(ws, ".cortex", "memory")
		runTurn = func(session, input string) (string, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "turn", "--session", session, "--json", input)
			cmd.Dir = ws
			cmd.Env = os.Environ()
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("turn %q: %v\n%s", oneLine(input), err, out)
			}
			return lastReply(t, out), string(out)
		}
		return memDir, runTurn
	}

	// --- (a) cross-session recall of a non-re-derivable fact ------------------
	t.Run("recall_across_sessions", func(t *testing.T) {
		memDir, runTurn := scenario(t)
		runTurn("a-write",
			"Please remember this for future sessions: we deploy to production only on Tuesdays, "+
				"never on Fridays — it's our change-freeze policy.")
		if !memoryMentions(t, memDir, "tuesday") {
			t.Fatalf("FAILURE: the model did not save a note recording the deploy policy "+
				"(nothing in %s mentions Tuesday) — cross-session recall is impossible without it", memDir)
		}
		reply, _ := runTurn("a-recall",
			"On which day of the week are we allowed to deploy to production?")
		if !containsFold(reply, "tuesday") {
			t.Fatalf("FAILURE: a fresh session could not recall the saved deploy policy. "+
				"Reply: %s", oneLine(reply))
		}
		t.Logf("PASS: fresh session recalled the policy from memory. Reply: %s", oneLine(reply))
	})

	// --- (b) updating a note that has gone stale ------------------------------
	t.Run("stale_note_update", func(t *testing.T) {
		memDir, runTurn := scenario(t)
		runTurn("b-write",
			"Remember for later: the staging database is reset nightly at 2am UTC.")
		// Precondition is topic-level (a staging note exists), not phrasing-level —
		// the behavior under test is the UPDATE, asserted via recall below.
		if !memoryMentions(t, memDir, "staging") {
			t.Fatalf("precondition: the model did not save any staging note in %s", memDir)
		}
		runTurn("b-update",
			"Update your memory: we moved the staging database reset to 4am UTC. It is no longer 2am.")
		reply, _ := runTurn("b-recall",
			"What time is the staging database reset each night?")
		fresh := mentionsHour(reply, "4")
		stale := mentionsHour(reply, "2") && !fresh
		switch {
		case fresh:
			t.Logf("PASS: model updated the stale note and recalled 4am. Reply: %s", oneLine(reply))
		case stale:
			t.Fatalf("FAILURE: model recalled the STALE time (2am) after being told it moved to 4am — "+
				"the update did not take. Reply: %s", oneLine(reply))
		default:
			t.Fatalf("AMBIGUOUS: reply resolved to neither 4am (fresh) nor 2am (stale): %s", oneLine(reply))
		}
	})

	// --- (c) study(journal) for a detail no note holds ------------------------
	// The fact is stated conversationally and never written as a note or a file,
	// so the ONLY durable record is the per-turn journal capture. Recalling it in
	// a fresh session requires studying .cortex/journal (or .cortex/sessions).
	t.Run("journal_for_unnoted_detail", func(t *testing.T) {
		_, runTurn := scenario(t)
		runTurn("c-state",
			"Just thinking out loud, no need to save anything: I picked 1999 (in cents) as the base "+
				"price for the starter tier because it tested better than 2499. Anyway, carry on.")
		reply, raw := runTurn("c-recall",
			"Earlier in this project I mentioned the base price I chose for the starter tier, but I "+
				"didn't write it down as a note. What value was it? Check the project journal if you need to.")
		recovered := strings.Contains(reply, "1999")
		consultedJournal := strings.Contains(raw, ".cortex/journal") || strings.Contains(raw, ".cortex/sessions")
		switch {
		case recovered:
			t.Logf("PASS: model recovered the unnoted detail (1999) from the journal. Reply: %s", oneLine(reply))
		case consultedJournal:
			t.Errorf("FAILURE: model consulted the journal but did not recover the value 1999. Reply: %s", oneLine(reply))
		default:
			t.Fatalf("FAILURE: model neither recovered 1999 nor consulted the journal for an unnoted "+
				"prior-session detail. Reply: %s", oneLine(reply))
		}
	})
}

// mentionsHour reports whether s names the given hour as a time-of-day, tolerant
// of the formats a model emits: "4am", "4 am", "4 a.m.", "4:00", "4am UTC".
func mentionsHour(s, h string) bool {
	s = strings.ToLower(s)
	for _, suffix := range []string{"am", " am", "a.m", ":00", " utc"} {
		if strings.Contains(s, h+suffix) {
			return true
		}
	}
	return false
}

// memoryMentions reports whether any note file under dir contains substr
// (case-insensitive) — the on-disk proof that the model actually saved a fact.
func memoryMentions(t *testing.T, dir, substr string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	want := strings.ToLower(substr)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "INDEX.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && strings.Contains(strings.ToLower(string(b)), want) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// lastReply extracts the {"reply":...} JSON the `cortex turn --json` path prints on
// its final stdout line (tool-action chatter, if any, precedes it).
func lastReply(t *testing.T, out []byte) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var m struct {
			Reply string `json:"reply"`
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &m) == nil && (m.Reply != "" || m.Error != "") {
			if m.Error != "" {
				t.Fatalf("turn returned error: %s", m.Error)
			}
			return m.Reply
		}
	}
	t.Fatalf("no JSON reply found in turn output:\n%s", out)
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
