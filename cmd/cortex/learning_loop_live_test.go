// learning_loop_live_test.go is the ø (agentic) layer of the learning-loop
// eval, implementing docs/think-dream-eval.md's needle-A/needle-B/noise
// design against THIS shipped implementation (docs/learning-loop.md): does
// a background Learn pass (internal/tools.Learn, `cortex learn`) produce
// curation a foreground-only session structurally cannot reach, without
// spamming the memory index or blowing an unbounded budget?
//
// No mocks: builds the real `cortex` binary and drives it across real
// subprocess turns (`cortex turn`) and real learn passes (`cortex learn`)
// against the live fleet, with a real .cortex/ on disk per arm per rep.
//
// SCENARIO (docs/think-dream-eval.md "The scenario")
//
//	One scripted session, run identically into TWO isolated workspaces per
//	rep (one per arm): a NEEDLE-A aside embedded inside an unrelated
//	debugging turn (the foreground coder has no task-shaped reason to save
//	it — the fact IS mentioned, never written), a NEEDLE-B fact stated where
//	the foreground coder's existing "remember that..." incentive already
//	covers it (both arms should catch this), and NOISE filler turns that
//	produce nothing save-worthy (the precision floor).
//
// ARMS
//
//	ARM-FOREGROUND: the scripted turns only — today's shipped behavior
//	  (memory tools + context tools, no background pass). Probed by a FRESH
//	  session turn asking the NEEDLE-A and NEEDLE-B questions.
//	ARM-BACKGROUND: the SAME scripted turns, PLUS one `cortex learn` pass
//	  run afterward (docs/learning-loop.md's one-shot CLI entry point) before
//	  the same fresh-session probes.
//
// GATES (docs/think-dream-eval.md's ø table, machine-graded)
//
//	G1 NEEDLE-B parity   — both arms answer the NEEDLE-B probe correctly, on
//	                       EVERY rep (anti-vacuity: if the background-only
//	                       arm fails this too, the scenario is broken, not
//	                       informative — see the doc's "Inconclusive" row).
//	G2 NEEDLE-A lift     — ARM-BACKGROUND answers NEEDLE-A strictly more
//	                       often than ARM-FOREGROUND across n>=3 reps (the
//	                       causal claim; a tie or loss is a real no-go
//	                       signal, not noise to explain away).
//	G3 Precision floor   — ARM-BACKGROUND's total memory-note count across
//	                       the run stays within 2x ARM-FOREGROUND's (a
//	                       background pass that "helps" via memory_write
//	                       spam is not a win).
//	G4 Bounded cost      — cortex learn's own wall-clock, summed across every
//	                       rep, stays under a pinned budget. (Token spend is
//	                       not separately measured here: `cortex learn`'s
//	                       report is deliberately plain-text-only per
//	                       docs/learning-loop.md's "smallest honest slice" —
//	                       there is no per-pass token telemetry to read
//	                       without adding a --json flag this slice doesn't
//	                       have. Wall-clock is the enforced budget; a later
//	                       slice can add token accounting if this gate proves
//	                       too coarse.)
//
// Reported, not gated (per the doc): which rep's learn pass actually wrote
// the winning note, latency distribution, and false-positive note content.
//
//	CORTEX_LIVE_FLEET=1 go test ./cmd/cortex/ -run LearningLoop_Live -v -timeout 2400s
//
// Tunables:
//
//	CORTEX_LEARNING_LOOP_REPS         n>=3 reps (default 3, the doc's floor)
//	CORTEX_LEARNING_LOOP_NOISE_TURNS  filler turns per arm per rep (default 3)
//	CORTEX_LEARNING_LOOP_BUDGET_MS    G4's pinned learn-pass wall-clock budget,
//	                                  summed across every rep (default 180000 = 3 min)
//
// No endpoint/model/study knobs: like memory_e2e_live_test.go, every turn and
// every learn pass goes through the real `cortex` subprocess, which resolves
// its own backend from the ambient config — the same resolution a real user
// gets.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// llNeedleA/llNeedleB are the scenario's two facts. Codewords, not
// re-derivable — staleness/recall only bite for facts the model can't
// recompute from the code (memory-tools.md's design rule, reused here).
const (
	llNeedleACodeword = "GLACIER-73" // aside inside an unrelated debugging turn
	llNeedleBCodeword = "PRISM-08"   // explicit "remember that..." — both arms should catch it
)

// llScriptedTurn is one turn in the shared scripted session, in order.
type llScriptedTurn struct {
	label string
	input string
}

// llScenario builds the shared scripted session both arms run identically:
// a debugging-shaped turn carrying the NEEDLE-A aside (off the turn's own
// task, so a foreground-only coder has no LOCAL reason to memory_write it),
// an explicit NEEDLE-B "remember this" turn, and noiseTurns filler turns
// that produce nothing save-worthy.
func llScenario(noiseTurns int) []llScriptedTurn {
	turns := []llScriptedTurn{
		{
			label: "debug-with-aside",
			input: "The retry helper in internal/foo is throwing a nil pointer when the queue is empty — can you " +
				"reason through why that would happen? By the way, totally separate note for later: we standardized " +
				"on the codeword " + llNeedleACodeword + " for this kind of transient-queue-empty situation in our " +
				"internal shorthand — anyway, back to the nil pointer, what's the likely cause?",
		},
		{
			label: "needle-b-explicit",
			input: "Please remember this for future sessions: our incident severity escalation marker is the codeword " +
				llNeedleBCodeword + " — that's what we page on-call with when a Sev1 breaches its SLA.",
		},
	}
	for i := 1; i <= noiseTurns; i++ {
		turns = append(turns, llScriptedTurn{
			label: fmt.Sprintf("noise-%d", i),
			input: fmt.Sprintf("Quick sanity check %d: does 2+2 still equal 4? Just confirm, nothing else needed.", i),
		})
	}
	return turns
}

const (
	llNeedleAProbe = "Is there a codeword we use as internal shorthand for a transient-queue-empty situation? " +
		"If you're not sure, say so rather than guessing."
	llNeedleBProbe = "What is the codeword for our incident severity escalation marker — what we page on-call with " +
		"when a Sev1 breaches its SLA?"
)

// llRunTurn drives one `cortex turn` subprocess against ws.
func llRunTurn(t *testing.T, bin, ws, session, input string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "turn", "--session", session, "--json", input)
	cmd.Dir = ws
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("turn %q: %v\n%s", oneLine(input), err, out)
	}
	return lastReply(t, out)
}

// llRunLearn drives one `cortex learn` subprocess against ws, returning its
// plain-text report and the wall-clock the subprocess took (G4's measured
// quantity).
func llRunLearn(t *testing.T, bin, ws string) (report string, elapsed time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, "learn")
	cmd.Dir = ws
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("cortex learn: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), elapsed
}

// llMemoryNoteCount counts the notes on disk under ws's memory store —
// G3's precision-floor measurement.
func llMemoryNoteCount(ws string) int {
	entries, err := os.ReadDir(filepath.Join(ws, ".cortex", "memory"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "INDEX.md" {
			n++
		}
	}
	return n
}

// llRepResult is one rep's measurements for one arm.
type llRepResult struct {
	needleAHit, needleBHit bool
	noteCount              int
	learnElapsed           time.Duration // zero for ARM-FOREGROUND
}

// llRunArm runs the shared scenario into a fresh workspace, optionally
// followed by a learn pass (background arm), then probes both needles from
// a FRESH session (the same .cortex, a new session id) — proving recall
// crosses session boundaries via the memory index injection, not
// same-session context.
func llRunArm(t *testing.T, bin string, background bool, noiseTurns int) llRepResult {
	t.Helper()
	ws := t.TempDir()

	for _, turn := range llScenario(noiseTurns) {
		llRunTurn(t, bin, ws, turn.label, turn.input)
	}

	var res llRepResult
	if background {
		_, elapsed := llRunLearn(t, bin, ws)
		res.learnElapsed = elapsed
	}

	aReply := llRunTurn(t, bin, ws, "probe-needle-a", llNeedleAProbe)
	bReply := llRunTurn(t, bin, ws, "probe-needle-b", llNeedleBProbe)
	res.needleAHit = strings.Contains(aReply, llNeedleACodeword)
	res.needleBHit = strings.Contains(bReply, llNeedleBCodeword)
	res.noteCount = llMemoryNoteCount(ws)
	return res
}

// llReportScoreboard logs the per-rep, per-arm table plus the four gates'
// verdicts.
func llReportScoreboard(t *testing.T, fg, bg []llRepResult) {
	t.Helper()
	t.Logf("--- learning-loop scoreboard (rep x arm) ---")
	t.Logf("%-4s %-12s %-10s %-10s %6s %10s", "rep", "arm", "needle-a", "needle-b", "notes", "learn_ms")
	for i := range fg {
		t.Logf("%-4d %-12s %-10v %-10v %6d %10s", i+1, "foreground", fg[i].needleAHit, fg[i].needleBHit, fg[i].noteCount, "-")
	}
	for i := range bg {
		t.Logf("%-4d %-12s %-10v %-10v %6d %10dms", i+1, "background", bg[i].needleAHit, bg[i].needleBHit, bg[i].noteCount, bg[i].learnElapsed.Milliseconds())
	}
}

func llCountHits(reps []llRepResult, pick func(llRepResult) bool) int {
	n := 0
	for _, r := range reps {
		if pick(r) {
			n++
		}
	}
	return n
}

func llTotalNotes(reps []llRepResult) int {
	n := 0
	for _, r := range reps {
		n += r.noteCount
	}
	return n
}

func TestLearningLoop_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run the live learning-loop eval")
	}

	bin := filepath.Join(t.TempDir(), "cortex")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build cortex: %v\n%s", err, out)
	}

	reps := liveEnvInt("CORTEX_LEARNING_LOOP_REPS", 3)
	if reps < 3 {
		reps = 3 // docs/think-dream-eval.md's G2 floor: n>=3
	}
	noiseTurns := liveEnvInt("CORTEX_LEARNING_LOOP_NOISE_TURNS", 3)
	budgetMS := int64(liveEnvInt("CORTEX_LEARNING_LOOP_BUDGET_MS", 180_000))

	var fgResults, bgResults []llRepResult
	for i := 0; i < reps; i++ {
		i := i
		t.Run(fmt.Sprintf("foreground/rep-%d", i+1), func(t *testing.T) {
			fgResults = append(fgResults, llRunArm(t, bin, false, noiseTurns))
		})
		t.Run(fmt.Sprintf("background/rep-%d", i+1), func(t *testing.T) {
			bgResults = append(bgResults, llRunArm(t, bin, true, noiseTurns))
		})
	}

	llReportScoreboard(t, fgResults, bgResults)

	// G1 — NEEDLE-B parity (anti-vacuity): every rep, both arms.
	t.Run("G1_needle_b_parity", func(t *testing.T) {
		for i, r := range fgResults {
			if !r.needleBHit {
				t.Errorf("rep %d: ARM-FOREGROUND missed NEEDLE-B — scenario invalid (see docs/think-dream-eval.md's Inconclusive row), not just a lift failure", i+1)
			}
		}
		for i, r := range bgResults {
			if !r.needleBHit {
				t.Errorf("rep %d: ARM-BACKGROUND missed NEEDLE-B — scenario invalid", i+1)
			}
		}
	})

	// G2 — NEEDLE-A lift: background strictly beats foreground across reps.
	t.Run("G2_needle_a_lift", func(t *testing.T) {
		fgHits := llCountHits(fgResults, func(r llRepResult) bool { return r.needleAHit })
		bgHits := llCountHits(bgResults, func(r llRepResult) bool { return r.needleAHit })
		t.Logf("NEEDLE-A hits: foreground %d/%d, background %d/%d", fgHits, len(fgResults), bgHits, len(bgResults))
		if bgHits <= fgHits {
			t.Errorf("NO-GO: background (%d/%d) did not strictly beat foreground (%d/%d) on NEEDLE-A — "+
				"per docs/think-dream-eval.md, a tie or loss here is a real no-go signal, not noise to explain away",
				bgHits, len(bgResults), fgHits, len(fgResults))
		}
	})

	// G3 — precision floor: background's total notes <= 2x foreground's.
	t.Run("G3_precision_floor", func(t *testing.T) {
		fgNotes := llTotalNotes(fgResults)
		bgNotes := llTotalNotes(bgResults)
		limit := 2 * fgNotes
		if fgNotes == 0 {
			limit = 2 // foreground wrote nothing extra; background still shouldn't spam
		}
		t.Logf("total notes: foreground %d, background %d (limit %d)", fgNotes, bgNotes, limit)
		if bgNotes > limit {
			t.Errorf("background wrote %d notes total, over the %dx-foreground precision floor (%d) — "+
				"a background pass that helps only by spamming memory_write is not a win", bgNotes, 2, limit)
		}
	})

	// G4 — bounded cost: total learn-pass wall-clock across every rep.
	t.Run("G4_bounded_cost", func(t *testing.T) {
		var totalMS int64
		for _, r := range bgResults {
			totalMS += r.learnElapsed.Milliseconds()
		}
		t.Logf("total learn-pass wall-clock across %d reps: %dms (budget %dms)", len(bgResults), totalMS, budgetMS)
		if totalMS > budgetMS {
			t.Errorf("learn-pass wall-clock %dms exceeds the pinned budget %dms — a background pass that wins G2 "+
				"by burning an unbounded budget fails the 'simplified' premise", totalMS, budgetMS)
		}
	})
}
