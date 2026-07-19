package main

// memory_coldwarm_live_test.go is Eval 6b (docs/completion-roadmap.md Track
// B3, docs/cortex-production-harness.md Part 6): the learning-loop runner
// that measures the project's core thesis directly — does accumulated
// memory (notes + journal) make the agent measurably better on later,
// related tasks?
//
// WHAT'S MEASURED
//
//	Two arms drive the SAME task set through the real `cortex turn` binary,
//	each over its own fresh, isolated .cortex/:
//
//	  COLD: a brand-new .cortex — no memory notes, no journal history. The
//	    probe for each task is the FIRST turn ever run in that workspace.
//	  WARM: a .cortex seeded by actually RUNNING a scripted prior "learning"
//	    session through the real harness first — turns worded to make the
//	    live model call memory_write and to leave per-turn journal capture
//	    behind (internal/capture, via captureTurn) — then the same probes
//	    run as fresh sessions over that now-populated .cortex.
//
//	If the thesis holds, WARM recalls the planted facts and COLD does not
//	(it has nothing to recall from and must not confabulate). This is a
//	between-state comparison (cold .cortex vs warm .cortex), not a
//	between-toolset comparison like context_pivot_eval_live_test.go's
//	ARM-ON/ARM-OFF — both arms get the full toolset; only the PRIOR STATE
//	differs, which is exactly the variable the thesis is about.
//
// TASK SET (mirrors memory_e2e_live_test.go's a/b/c structure)
//
//	task "a" (note-direct):   one plainly "remember this" fact → memory_write
//	                          → probe asks for it directly (index + memory_read).
//	task "b" (memory_search): three facts land in the same learning turn (two
//	                          distractor decisions + the target, itself carrying
//	                          the codeword) so recall can't be a lucky title
//	                          match — the model has to search/read among
//	                          several notes to surface the right one.
//	task "c" (journal-only):  a fact stated conversationally with an explicit
//	                          "no need to save it" — never a note, so the ONLY
//	                          durable record is captureTurn's per-turn journal
//	                          entry (.cortex/journal/capture/). Recovering it
//	                          requires study(.cortex/journal).
//
//	Each task carries a distinct codeword needle (regex-checkable, see
//	codewordish in context_eval_live_test.go) so a confabulated guess can
//	never coincidentally pass.
//
// SEEDING
//
//	WARM's prior state comes from actually running the learning turns through
//	the built binary (subprocess `cortex turn`, exactly memory_e2e_live_test.go's
//	pattern) — the real memory_write tool-call path, not hand-authored files.
//	Because a live small model can be flaky about *choosing* to save, each
//	note-worthy learning turn is retried once if the codeword doesn't land in
//	.cortex/memory/; only if it STILL doesn't land does the test fall back to
//	writing the note directly via internal/memory.Store.Write (bypassing the
//	model). Every fallback is t.Logf'd as a WARNING so a flaky live write is
//	reported honestly, never silently masked as "the real thing worked."
//
// GRADING (mechanical — no judge model)
//
//	WARM tasks a/b are graded hard: the probe reply must contain the planted
//	codeword, or the task fails. WARM task c is graded the same three-way the
//	existing live eval uses: recovered the value (pass), consulted the journal
//	but got it wrong (fail, loud), or neither (fail, loud — the record exists
//	and was reachable). COLD tasks (all three) are graded via the shared
//	floorGrade helper (context_eval_live_test.go): a correct answer is
//	accepted (the model would have to guess an unguessable codeword, so this
//	essentially never triggers) and an honest miss passes, but a
//	codeword-shaped confabulation fails — COLD is a confabulation-guard floor,
//	not a "must fail" gate, matching the accepted-floor convention the pivot
//	eval uses for its A-dead needle.
//
//	Each (arm, task) probe runs as its own t.Run subtest, so one grading
//	failure never prevents the remaining tasks or the other arm from running
//	and reporting.
//
// WHAT A PASS/FAIL MEANS
//
//	All WARM subtests passing + all COLD subtests passing (staying on the
//	accepted floor) is the thesis holding for this run: memory measurably
//	helped, and nothing was fabricated when it wasn't there to help with.
//	A WARM failure means accumulated memory did NOT pay off this run
//	(flakiness, a write that didn't land even after fallback, or a retrieval
//	miss) — informative, not necessarily a harness bug. A COLD failure
//	(a confabulated codeword) is a hallucination-guard regression and is
//	always worth investigating regardless of the WARM result.
//
// BOUNDED RUN
//
//	Small, fixed task count (3, or 1 via CORTEX_COLDWARM_TASKS=1 for a
//	sanity probe) plus a 6-minute per-turn subprocess timeout (matching every
//	other live eval in this package) keep a full run minutes-scale. The
//	engine's own default per-turn tool-call Bounds (cmd/cortex/loop.go) apply
//	unchanged to every turn — no extra iteration cap is threaded through here.
//
//	CORTEX_LIVE_FLEET=1 go test ./cmd/cortex/ -run MemoryColdVsWarm_Live -v -timeout 1800s
//
// Tunables:
//
//	CORTEX_COLDWARM_TASKS  1..3 tasks to run (default 3; 1 = quick sanity probe)
//
// No endpoint/model/study knobs: like memory_e2e_live_test.go (whose seeding
// pattern this reuses), every turn goes through the real `cortex turn`
// subprocess, which resolves its own backend from the ambient
// ~/.cortex/config.json / ./.cortex/config.json / CORTEX_BACKEND — the same
// resolution a real user gets. There is no in-process ModelSpec to override,
// so there's nothing analogous to CORTEX_CTX_EVAL_ENDPOINT/MODEL to add here.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/memory"
)

const (
	coldwarmArmCold = "cold"
	coldwarmArmWarm = "warm"
)

// coldwarmTask is one probe in the shared task set. learnInput is the
// scripted "learning session" turn that plants the fact for the WARM arm;
// expectsNote gates the memory_write retry/fallback logic (true for a/b,
// false for c — the journal-only task must NOT end up as a note, so its
// seeding is fire-and-forget).
type coldwarmTask struct {
	id          string
	label       string
	codeword    string
	learnInput  string
	probeInput  string
	expectsNote bool
	gradeWarm   func(t *testing.T, reply, raw string)
}

// coldwarmTasks returns the shared task set, in probe order. Facts are
// deliberately non-re-derivable (arbitrary policy numbers/tokens), same
// design rule memory_e2e_live_test.go documents: staleness/recall only bite
// for facts the model can't recompute from the code.
func coldwarmTasks() []coldwarmTask {
	const (
		codewordA = "TUNDRA-41" // task a: note-direct
		codewordB = "OPAL-19"   // task b: memory_search among several notes
		codewordC = "EMBER-64"  // task c: journal-only, never a note
	)

	taskA := coldwarmTask{
		id:       "a",
		label:    "note-direct (memory_write + index/read)",
		codeword: codewordA,
		learnInput: "Please remember this for future sessions: our public API rate-limit policy carries the codeword " +
			codewordA + " as its ceiling marker — that's the requests-per-minute-per-key hard ceiling, never raise it without security sign-off.",
		probeInput:  "What is the codeword marking our public API rate-limit ceiling (the requests-per-minute-per-key cap)?",
		expectsNote: true,
		gradeWarm: func(t *testing.T, reply, raw string) {
			t.Helper()
			if !strings.Contains(reply, codewordA) {
				t.Fatalf("FAILURE: warm arm did not recall the rate-limit ceiling codeword. Reply: %s", oneLine(reply))
			}
			t.Logf("PASS: warm arm recalled the rate-limit ceiling codeword. Reply: %s", oneLine(reply))
		},
	}

	taskB := coldwarmTask{
		id:       "b",
		label:    "memory_search (target among distractor notes)",
		codeword: codewordB,
		learnInput: "Please remember these three architecture decisions for future reference (separate notes are fine): " +
			"1) We chose Redis over Memcached for the session cache, valuing native TTL eviction. " +
			"2) We settled on gRPC over REST for internal service calls, valuing streaming support. " +
			"3) For the search index refresh cadence, the codeword " + codewordB +
			" marks the interval we settled on — that marker lets you confirm later you found the right note.",
		probeInput:  "Among the infrastructure decisions we recorded, what is the codeword marking the search-index refresh interval we settled on?",
		expectsNote: true,
		gradeWarm: func(t *testing.T, reply, raw string) {
			t.Helper()
			if !strings.Contains(reply, codewordB) {
				t.Fatalf("FAILURE: warm arm did not recall the search-index refresh interval codeword among the distractor notes. Reply: %s", oneLine(reply))
			}
			t.Logf("PASS: warm arm found the target note among distractors. Reply: %s", oneLine(reply))
		},
	}

	taskC := coldwarmTask{
		id:       "c",
		label:    "journal-only (unnoted detail, study(journal))",
		codeword: codewordC,
		learnInput: "Just thinking out loud, no need to save anything: for the Q3 experiment we picked the codeword " +
			codewordC + " as our sample-size-target marker. Anyway, carry on.",
		probeInput: "Earlier in this project I mentioned a codeword marking the sample-size target for the Q3 experiment, but I " +
			"don't think I asked you to save it as a note. What was that codeword? Check the project journal if you need to.",
		expectsNote: false,
		gradeWarm: func(t *testing.T, reply, raw string) {
			t.Helper()
			recovered := strings.Contains(reply, codewordC)
			consultedJournal := strings.Contains(raw, ".cortex/journal") || strings.Contains(raw, ".cortex/sessions")
			switch {
			case recovered:
				t.Logf("PASS: warm arm recovered the unnoted Q3 sample size from the journal. Reply: %s", oneLine(reply))
			case consultedJournal:
				t.Errorf("FAILURE: warm arm consulted the journal but did not recover %s. Reply: %s", codewordC, oneLine(reply))
			default:
				t.Fatalf("FAILURE: warm arm neither recovered %s nor consulted the journal for the unnoted detail. Reply: %s", codewordC, oneLine(reply))
			}
		},
	}

	return []coldwarmTask{taskA, taskB, taskC}
}

// coldwarmResult is one (arm, task) cell's measurements — the raw material
// for the final scoreboard and the emitted eval.cell_result row.
type coldwarmResult struct {
	arm, taskID, label  string
	reply               string
	pass                bool
	tokensIn, tokensOut int
	turns               int
	latencyMs           int64
}

// turnSessionID extracts the "session" field from the same last-JSON-line
// `cortex turn --json` prints that lastReply (memory_e2e_live_test.go)
// parses for "reply". Needed because the --session label passed on the
// command line is only a RESUME hint: when no transcript exists under that
// exact name yet (every call this file makes — each learn/probe turn is
// deliberately its own fresh session), cli.go's runTurnCLI falls through to
// StartTranscript, which mints its OWN timestamp-based id (session.go) and
// ignores the requested label entirely. The real id — not the label — is
// what emitSessionMetrics used as RunID, so it's what readLatestCellResult
// must look up.
func turnSessionID(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var m struct {
			Session string `json:"session"`
			Reply   string `json:"reply"`
			Error   string `json:"error"`
		}
		line := strings.TrimSpace(lines[i])
		if json.Unmarshal([]byte(line), &m) == nil && (m.Reply != "" || m.Error != "" || m.Session != "") {
			return m.Session
		}
	}
	return ""
}

// runColdWarmTurn drives one turn of the real `cortex turn` binary against
// ws, returning the reply, the full stdout (needed for task c's
// journal-consultation check), and the model-assigned session id actually
// used (see turnSessionID — it need not match the requested label).
// Mirrors memory_e2e_live_test.go's scenario closure, generalized to a
// caller-supplied workspace so learn and probe turns for the SAME arm share
// the SAME .cortex/.
func runColdWarmTurn(t *testing.T, bin, ws, session, input string) (reply, raw, actualSessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "turn", "--session", session, "--json", input)
	cmd.Dir = ws
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("coldwarm turn %q (session %s): %v\n%s", oneLine(input), session, err, out)
	}
	return lastReply(t, out), string(out), turnSessionID(out)
}

// seedFallbackNote writes task's fact directly via internal/memory.Store,
// bypassing the model — the documented fallback for when the live write
// proves too flaky even after a retry. Uses the real storage layer (not a
// hand-crafted file), so the note's shape (frontmatter, index regeneration)
// matches what memory_write would have produced.
func seedFallbackNote(t *testing.T, ws string, task coldwarmTask) {
	t.Helper()
	contextDir := filepath.Join(ws, ".cortex")
	store, err := memory.New(contextDir)
	if err != nil {
		t.Fatalf("coldwarm fallback: open memory store under %s: %v", contextDir, err)
	}
	if _, err := store.Write("coldwarm-"+task.id+"-fallback", task.learnInput, time.Now()); err != nil {
		t.Fatalf("coldwarm fallback: write note for task %s: %v", task.id, err)
	}
}

// ensureNoteOrFallback checks that task's codeword landed in memDir after
// the live learning turn. Live small models can forget to save; one retry
// covers ordinary flakiness, and a direct store write (loudly logged) covers
// the rest — see the file header's SEEDING section.
func ensureNoteOrFallback(t *testing.T, bin, ws, memDir string, task coldwarmTask) {
	t.Helper()
	if memoryMentions(t, memDir, task.codeword) {
		return
	}
	t.Logf("coldwarm: task %s codeword not found in memory after the first learn turn — retrying once", task.id)
	runColdWarmTurn(t, bin, ws, "learn-"+task.id+"-retry", task.learnInput)
	if memoryMentions(t, memDir, task.codeword) {
		return
	}
	t.Logf("WARNING coldwarm: live memory_write did not persist a note for task %s after a retry — "+
		"falling back to a direct internal/memory.Store write (bypasses the model tool-call path)", task.id)
	seedFallbackNote(t, ws, task)
}

// seedWarmMemory runs every task's learning turn once (planting task c's
// fact too, so it lands only in the journal — never checked against
// memDir) and retries/falls back for note-worthy tasks per
// ensureNoteOrFallback.
func seedWarmMemory(t *testing.T, bin, ws string, tasks []coldwarmTask) {
	t.Helper()
	memDir := filepath.Join(ws, ".cortex", "memory")
	for _, task := range tasks {
		runColdWarmTurn(t, bin, ws, "learn-"+task.id, task.learnInput)
		if task.expectsNote {
			ensureNoteOrFallback(t, bin, ws, memDir, task)
		}
	}
}

// readLatestCellResult scans ws's eval.cell_result journal class for the
// row emitSessionMetrics auto-wrote for the given session (RunID ==
// sessionID, the model-assigned id turnSessionID returned — NOT the
// requested --session label, see turnSessionID) — a best-effort read of
// real tokens/turns/model/backend for one probe, reusing the exact
// vocabulary session_runtime.go writes rather than re-deriving it.
func readLatestCellResult(ws, sessionID string) (journal.EvalCellResultPayload, bool) {
	classDir := filepath.Join(ws, ".cortex", "journal", "eval")
	r, err := journal.NewReader(classDir)
	if err != nil {
		return journal.EvalCellResultPayload{}, false
	}
	defer r.Close()
	var found journal.EvalCellResultPayload
	ok := false
	for {
		e, err := r.Next()
		if err != nil {
			break // io.EOF (normal) or a torn line — either way, stop
		}
		if e.Type != journal.TypeEvalCellResult {
			continue
		}
		p, err := journal.ParseEvalCellResult(e)
		if err != nil || p.RunID != sessionID {
			continue
		}
		found, ok = *p, true
	}
	return found, ok
}

// emitColdWarmCellResult writes ONE eval.cell_result row for one (arm,
// task) cell, ContextStrategy set to the arm ("cold"/"warm") per
// docs/completion-roadmap.md's B3 field mapping. Mirrors emitSessionMetrics
// (session_runtime.go) exactly: same journal class dir, same FsyncPerBatch
// posture (a coldwarm row, like a session summary, is regeneratable by
// re-running the probe).
func emitColdWarmCellResult(ws, runID, sessionID, arm, model, backend string, task coldwarmTask, reply string, pass bool, tokensIn, tokensOut, turns int, latencyMs int64) error {
	criterion := "confabulation_guard_floor"
	if arm == coldwarmArmWarm {
		criterion = "codeword_recall_hard"
	}
	p := journal.EvalCellResultPayload{
		SchemaVersion:        "1",
		RunID:                runID,
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		ScenarioID:           "coldwarm-task-" + task.id,
		SessionID:            sessionID,
		Harness:              "coldwarm-eval",
		Provider:             "openai-compat",
		Model:                model,
		Backend:              backend,
		ContextStrategy:      arm,
		CortexVersion:        version(),
		TokensIn:             tokensIn,
		TokensOut:            tokensOut,
		LatencyMs:            latencyMs,
		AgentTurnsTotal:      turns,
		TaskSuccess:          pass,
		TaskSuccessCriterion: criterion,
		Notes:                fmt.Sprintf("task=%s label=%q", task.id, task.label),
	}
	entry, err := journal.NewEvalCellResultEntry(p)
	if err != nil {
		return fmt.Errorf("build coldwarm eval.cell_result: %w", err)
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: filepath.Join(ws, ".cortex", "journal", "eval"),
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return fmt.Errorf("open coldwarm journal writer: %w", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("append coldwarm eval.cell_result: %w", err)
	}
	return nil
}

// runColdWarmArm drives every task's probe for one arm and returns the
// per-task results for the final scoreboard. For WARM, the learning session
// runs first so the probes' .cortex already carries the accumulated
// memory+journal state; for COLD, each probe is the very first turn ever
// run in its (freshly created) workspace.
func runColdWarmArm(t *testing.T, bin, arm, runID string, tasks []coldwarmTask) []coldwarmResult {
	t.Helper()
	ws := t.TempDir()
	if arm == coldwarmArmWarm {
		seedWarmMemory(t, bin, ws, tasks)
	}

	var results []coldwarmResult
	for _, task := range tasks {
		task := task
		label := arm + "-probe-" + task.id // request label only; see turnSessionID
		var reply, raw, actualSessionID string
		var latencyMs int64
		pass := t.Run(fmt.Sprintf("%s/task-%s", arm, task.id), func(t *testing.T) {
			start := time.Now()
			reply, raw, actualSessionID = runColdWarmTurn(t, bin, ws, label, task.probeInput)
			latencyMs = time.Since(start).Milliseconds()
			if arm == coldwarmArmWarm {
				task.gradeWarm(t, reply, raw)
				return
			}
			floorGrade(t, fmt.Sprintf("[%s] task-%s", arm, task.id), reply, task.codeword)
			if strings.Contains(reply, task.codeword) {
				t.Logf("NOTE [%s] task-%s: cold arm produced the correct codeword with no prior state — "+
					"either a lucky guess or the scenario leaks the fact some other way; worth a second look "+
					"if this recurs", arm, task.id)
			}
		})

		row, found := readLatestCellResult(ws, actualSessionID)
		model, backend, tokensIn, tokensOut, turns := "", "", 0, 0, 0
		if found {
			model, backend = row.Model, row.Backend
			tokensIn, tokensOut, turns = row.TokensIn, row.TokensOut, row.AgentTurnsTotal
		}
		if err := emitColdWarmCellResult(ws, runID, actualSessionID, arm, model, backend, task, reply, pass, tokensIn, tokensOut, turns, latencyMs); err != nil {
			// Best-effort — a journal write hiccup never fails the eval itself,
			// same posture as emitSessionMetrics/emitStudyResult.
			t.Logf("coldwarm: journal emit failed for %s/task-%s: %v", arm, task.id, err)
		}
		results = append(results, coldwarmResult{
			arm: arm, taskID: task.id, label: task.label, reply: reply, pass: pass,
			tokensIn: tokensIn, tokensOut: tokensOut, turns: turns, latencyMs: latencyMs,
		})
	}
	return results
}

// reportColdWarmScoreboard logs the compact task × arm → pass/fail/tokens
// table the roadmap asks for.
func reportColdWarmScoreboard(t *testing.T, warm, cold []coldwarmResult) {
	t.Helper()
	t.Logf("--- cold-vs-warm scoreboard (task x arm) ---")
	t.Logf("%-6s %-6s %-6s %10s %10s %6s %10s", "task", "arm", "pass", "tokens_in", "tokens_out", "turns", "latency")
	type cell struct {
		cold, warm *coldwarmResult
	}
	byTask := map[string]*cell{}
	get := func(id string) *cell {
		c, ok := byTask[id]
		if !ok {
			c = &cell{}
			byTask[id] = c
		}
		return c
	}
	for i := range cold {
		get(cold[i].taskID).cold = &cold[i]
	}
	for i := range warm {
		get(warm[i].taskID).warm = &warm[i]
	}
	logRow := func(r *coldwarmResult) {
		if r == nil {
			return
		}
		t.Logf("%-6s %-6s %-6v %10d %10d %6d %8dms", r.taskID, r.arm, r.pass, r.tokensIn, r.tokensOut, r.turns, r.latencyMs)
	}
	for _, task := range coldwarmTasks() {
		if c, ok := byTask[task.id]; ok {
			logRow(c.warm)
			logRow(c.cold)
		}
	}
}

func TestMemoryColdVsWarm_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run the live cold-vs-warm learning-loop eval")
	}

	bin := filepath.Join(t.TempDir(), "loop")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build loop: %v\n%s", err, out)
	}

	n := liveEnvInt("CORTEX_COLDWARM_TASKS", 3)
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	tasks := coldwarmTasks()[:n]

	runID := "coldwarm-" + time.Now().UTC().Format("20060102-150405")

	var warmResults, coldResults []coldwarmResult
	t.Run(coldwarmArmWarm, func(t *testing.T) {
		warmResults = runColdWarmArm(t, bin, coldwarmArmWarm, runID, tasks)
	})
	t.Run(coldwarmArmCold, func(t *testing.T) {
		coldResults = runColdWarmArm(t, bin, coldwarmArmCold, runID, tasks)
	})

	reportColdWarmScoreboard(t, warmResults, coldResults)
}
