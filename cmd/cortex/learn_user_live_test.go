// learn_user_live_test.go is the ø (agentic) layer of the user-scope
// cross-project promotion pass (docs/cross-source-learning.md piece 1's
// promotion half): does `cortex learn --user` (learn_user.go's
// RunLearnUserPass) actually move a fact learned independently in two
// projects into the user tier, and does a THIRD, previously untouched
// project then recall it — without over-promoting a noise project's own
// unrelated fact along the way?
//
// No mocks: builds the real `cortex` binary and drives it across real
// subprocess calls (`cortex project add`, `cortex turn`, `cortex learn
// --user`) against the live fleet, with real .cortex/ and $CORTEX_HOME
// state on disk per rep. Reuses learning_loop_live_test.go's subprocess
// patterns (llRunTurn's shape, the build-once-run-many binary, the
// scoreboard-then-gated-subtests structure) and
// memory_coldwarm_live_test.go's retry-then-fallback seeding discipline
// (a live small model can forget to save; one retry covers ordinary
// flakiness, a direct internal/memory.Store write — loudly logged — covers
// the rest).
//
// SCENARIO
//
//	Four projects share one isolated $CORTEX_HOME (registry + user-tier
//	memory) per rep:
//
//	  A, B  learn the SAME cross-project fact independently, in nearly the
//	        same words (see "needle design" below) — this is deliberate,
//	        not sloppy: detectCandidateGroups (learn_user.go) requires >=
//	        recurrenceMinSharedTerms (3) literal shared terms >=
//	        recurrenceMinTermLen (5) runes between two DIFFERENT projects'
//	        notes, and a live model paraphrasing "in its own words" is a
//	        real flakiness risk against a literal-string-overlap detector.
//	  C     learns an UNRELATED fact (its own codeword, zero vocabulary
//	        overlap with A/B) — the noise control: G2 asserts the pass
//	        promotes nothing sourced from C.
//	  D     stays completely empty until the grading turns below — the
//	        consumer: a fresh session asking the question only the
//	        promoted fact answers.
//
//	`cortex learn --user` then runs once against that $CORTEX_HOME.
//	Grading probes D two ways:
//
//	  ARM-ON  — same $CORTEX_HOME (the promoted note is present) — a fresh
//	            `cortex turn` in D's own root, never touched before.
//	  ARM-OFF — a COMPLETELY SEPARATE, pristine $CORTEX_HOME and a
//	            COMPLETELY SEPARATE D root, neither of which the promotion
//	            pass (or ARM-ON's turn) ever touched — see "ARM-OFF
//	            isolation" below for why this beats a copy-then-delete.
//
// NEEDLE DESIGN
//
//	The shared fact is "our fleet-wide gateway service's shared identifier
//	is codeword VERMILION-9" (luFactSentence). VERMILION-9 matches
//	floorGrade's codewordish pattern (\b[A-Z]{3,}-\d+\b), same convention
//	as every other codeword in this package. The prompt to A and B is
//	nearly VERBATIM (framing differs — "found while debugging" vs. "an
//	architecture review confirmed it" — the fact sentence itself does not)
//	and explicitly asks the model to keep "gateway", "fleet", and "shared"
//	in its note alongside the codeword. Those three words plus "vermilion"
//	(from the codeword) are the sentence's semantic core, not decoration —
//	a reasonable paraphrase can drop one but is unlikely to drop enough of
//	four candidates to fall under the 3-term floor. luEnsureProjectNoteOrFallback
//	then mechanically VERIFIES (not just hopes) that a saved note contains
//	all four guard terms, retrying once and falling back to a direct
//	internal/memory.Store write (bypassing the model, loudly logged) if a
//	live model still doesn't cooperate — this bounds the seeding
//	precondition's flakiness without faking the thing actually under test
//	(whether LearnUser's live promotion call decides to promote).
//
//	Project C's fact ("on-call escalation rotation pager alias is codeword
//	AZURE-3") shares no vocabulary with the gateway fact by construction —
//	the noise control needs zero accidental overlap, not a retry loop.
//
// ARM-OFF ISOLATION — "cannot leak" by construction, not by cleanup
//
//	The design doc floats two options: point $CORTEX_HOME at a copy
//	without the promoted note, or delete the note before the control turn.
//	Both start from the SAME state the promotion pass wrote into and then
//	try to subtract the leak — which only holds if the subtraction is
//	exactly right (miss one file — a regenerated INDEX.md entry, a second
//	promoted note from a prior candidate group, a lingering cache — and
//	the control is compromised silently). This file instead gives ARM-OFF
//	its own $CORTEX_HOME and its own D root, created fresh and NEVER
//	passed to `cortex learn --user` or to ARM-ON's turn at all. There is
//	no promoted note to fail to delete, because the promotion pass never
//	wrote into that directory tree in the first place — the guarantee
//	holds by construction, not by successfully executing a cleanup step.
//	The only cost is that ARM-OFF doesn't prove "the note was deleted
//	correctly"; it doesn't need to — the design doc's own goal is "the
//	cleanest honest control", and "never present" is cleaner than
//	"present, then removed".
//
// GATES (this task's G1-G4; note this renumbers
// docs/cross-source-learning.md's own ø table, which is not yet built out
// to live-test form — these mirror learning_loop_live_test.go's G1-G4
// naming CONVENTION, not that doc's specific G1-G4 text)
//
//	G1 Cross-project lift  — ARM-ON answers the needle probe correctly
//	                         strictly more often than ARM-OFF, across
//	                         n>=3 reps (a tie or loss is a real no-go
//	                         signal, matching learning_loop_live_test.go's
//	                         G2 posture).
//	G2 Promotion precision — the user tier gains <=2x the gold fact count
//	                         (1 gold fact -> max 2 notes) per rep, and no
//	                         promoted note is sourced from (or contains)
//	                         project C's noise fact.
//	G3 Provenance          — the gold note's "Promoted from:" line names
//	                         EXACTLY projects a and b (mechanically
//	                         enforced by preparePromotionWrite per Δ2 —
//	                         this is the live-fleet confirmation that the
//	                         mechanism actually fires end to end).
//	G4 Bounded cost        — `cortex learn --user`'s wall-clock per rep
//	                         stays under a pinned, generous budget.
//
// The deterministic seed->detector->promotion->injection chain is already
// covered without a model: learn_user_eval_test.go's
// TestRunLearnUserPassPromotesWithProvenanceLine (seed via writeProjectNote
// -> detectCandidateGroups -> a scripted-Sender LearnUser call ->
// provenance-stamped note landing on a REAL on-disk user-tier Store, read
// back via a fresh Store handle) plus
// TestRunLoopFiringLearnUserScopeRunsPromotionPassAndJournals (the same
// chain through the loop-firing entry point) cover promotion end to end;
// user_memory_test.go's TestMemoryIndexNoteInjectionOrderAndIndependentCaps
// covers injection (any user-tier note, regardless of how it got there,
// surfacing in memoryIndexNote). Together that's the full deterministic
// chain this scenario exercises live — no new Δ skeleton is added here to
// avoid duplicating it.
//
//	CORTEX_LIVE_FLEET=1 go test ./cmd/cortex/ -run LearnUser_Live -v -timeout 2400s
//
// Tunables:
//
//	CORTEX_LEARN_USER_LIVE_ENDPOINT   backend base URL (default http://localhost:4000)
//	CORTEX_LEARN_USER_LIVE_REPS       n>=3 reps (default 3, the G1 floor)
//	CORTEX_LEARN_USER_LIVE_BUDGET_MS  G4's pinned `learn --user` per-rep wall-clock
//	                                  budget (default 120000 = 120s/rep)
//
// An explicit endpoint knob is needed here (unlike learning_loop_live_test.go
// and memory_coldwarm_live_test.go, which resolve backend config from the
// ambient ~/.cortex/config.json because they never touch $CORTEX_HOME): this
// scenario MUST override $CORTEX_HOME per rep (and per arm, for ARM-OFF) to
// isolate the registry and user-tier memory from both the real developer
// machine and from each other — which also hides the ambient config.json
// backend config that lives under the SAME $CORTEX_HOME. Rather than copy
// that file into every isolated home (another place a copy could go subtly
// stale), every subprocess call gets CORTEX_BACKEND=<endpoint> directly —
// config.go's backendEndpoint() falls back to it whenever no config.json is
// present, the same zero-config path a real first-run user hits, and model
// selection falls through to the live fleet-discovery probe
// (discoverFleet/selectModel) exactly as it would for that user. No
// model/study knobs are added on top: pinning by role name would need an
// actual config.json (the thing this design deliberately avoids
// depending on), and fleet auto-select is the genuinely zero-config
// behavior under test.
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

	"github.com/dereksantos/cortex/internal/memory"
)

// luCodeword is the shared cross-project fact's needle (matches floorGrade's
// codewordish pattern). luNoiseCodewordC is project C's own, unrelated
// fact's needle — it must never appear in the promoted user tier.
const (
	luCodeword       = "VERMILION-9"
	luNoiseCodewordC = "AZURE-3"
)

// luGuardTerms are the terms luEnsureProjectNoteOrFallback requires present
// TOGETHER in one saved note before treating A's or B's seeding as
// successful — see the file header's "needle design" for why these four
// (not just the codeword) are checked: detectCandidateGroups needs >= 3
// shared terms between A's and B's notes, and checking only the codeword
// would let a heavily-paraphrased note pass seeding while still flunking
// the recurrence threshold downstream.
var luGuardTerms = []string{luCodeword, "gateway", "fleet", "shared"}

// luFactSentence is the literal cross-project fact both A and B are taught
// — deliberately near-identical wording (not just "in spirit"), and
// explicit about which words the note must keep, per the file header.
func luFactSentence() string {
	return fmt.Sprintf(
		"our fleet-wide gateway service has one shared identifier every project must use: "+
			"codeword %s. When you save this as a memory note, please keep the words "+
			"\"gateway\", \"fleet\", and \"shared\" in your note along with the codeword %s itself.",
		luCodeword, luCodeword)
}

func luSeedInputA() string {
	return "Please remember this for future sessions (we just ran into it while debugging a routing issue): " +
		luFactSentence()
}

func luSeedInputB() string {
	return "Please remember this for future sessions (our architecture review just confirmed the same thing independently): " +
		luFactSentence()
}

func luNoiseInputC() string {
	return "Please remember this for future sessions: our on-call escalation rotation's pager alias is the internal " +
		"codeword " + luNoiseCodewordC + " — nothing to do with any shared fleet service, just this project's own contact rotation."
}

const luProbeQuestion = "What is the codeword identifying our shared fleet-wide gateway service? " +
	"If you're not sure, say so rather than guessing."

// luEnv builds a subprocess environment isolated to home (registry +
// user-tier memory) with endpoint forced via CORTEX_BACKEND (see the file
// header's tunables note) — any ambient CORTEX_HOME/CORTEX_BACKEND from the
// test process's own environment is stripped first so there is never
// ambiguity about which value a subprocess sees.
func luEnv(home, endpoint string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CORTEX_HOME=") || strings.HasPrefix(e, "CORTEX_BACKEND=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "CORTEX_HOME="+home, "CORTEX_BACKEND="+endpoint)
	return env
}

// luProjectAdd registers name -> root in home's registry via a real `cortex
// project add` subprocess.
func luProjectAdd(t *testing.T, bin, home, name, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "project", "add", name, root)
	cmd.Env = luEnv(home, "")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("project add %s %s: %v\n%s", name, root, err, out)
	}
}

// luRunTurn drives one `cortex turn` subprocess in root, isolated to home.
func luRunTurn(t *testing.T, bin, home, endpoint, root, session, input string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "turn", "--session", session, "--json", input)
	cmd.Dir = root
	cmd.Env = luEnv(home, endpoint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("turn %q (root=%s): %v\n%s", oneLine(input), root, err, out)
	}
	return lastReply(t, out)
}

// luRunLearnUser drives `cortex learn --user` against home, returning its
// plain-text report and wall-clock (G4's measured quantity).
func luRunLearnUser(t *testing.T, bin, home, endpoint string) (report string, elapsed time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, "learn", "--user")
	cmd.Dir = home
	cmd.Env = luEnv(home, endpoint)
	out, err := cmd.CombinedOutput()
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("cortex learn --user: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), elapsed
}

// luProjectNoteHasTerms reports whether any SINGLE note file under memDir
// contains every term in terms (case-insensitive) — deliberately requiring
// co-location in one note, matching how candidateNote.terms is built from
// one note's own name+body in learn_user.go.
func luProjectNoteHasTerms(memDir string, terms ...string) bool {
	entries, err := os.ReadDir(memDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "INDEX.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(memDir, e.Name()))
		if err != nil {
			continue
		}
		body := strings.ToLower(string(b))
		ok := true
		for _, term := range terms {
			if !strings.Contains(body, strings.ToLower(term)) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// luEnsureProjectNoteOrFallback verifies root's project-tier memory holds a
// note carrying every term in guardTerms after the live seeding turn
// already run once; live small models can forget or over-paraphrase, so
// this retries once and, failing that, falls back to a direct
// internal/memory.Store write of the verbatim input (guaranteed to carry
// every guard term by construction) — loudly logged either way, mirroring
// memory_coldwarm_live_test.go's ensureNoteOrFallback/seedFallbackNote.
// This bounds the SEEDING precondition's flakiness only; it never touches
// the actual promotion call under test.
func luEnsureProjectNoteOrFallback(t *testing.T, bin, home, endpoint, root, label, input, fallbackName string, guardTerms []string) {
	t.Helper()
	memDir := filepath.Join(root, ".cortex", "memory")
	if luProjectNoteHasTerms(memDir, guardTerms...) {
		return
	}
	t.Logf("learn-user-live: %s note missing required terms %v after the first turn — retrying once", label, guardTerms)
	luRunTurn(t, bin, home, endpoint, root, label+"-retry", input)
	if luProjectNoteHasTerms(memDir, guardTerms...) {
		return
	}
	t.Logf("WARNING learn-user-live: live memory_write did not persist a %s note carrying %v after a retry — "+
		"falling back to a direct internal/memory.Store write (bypasses the model tool-call path)", label, guardTerms)
	store, err := memory.New(filepath.Join(root, ".cortex"))
	if err != nil {
		t.Fatalf("open project memory store under %s: %v", root, err)
	}
	if _, err := store.Write(fallbackName, input, time.Now()); err != nil {
		t.Fatalf("fallback write for %s: %v", label, err)
	}
}

// luOpenUserMemory opens the SAME on-disk user-tier store CortexSession.
// EnableMemory resolves (session_runtime.go: userhome.Path("memory") then
// memory.New on that — Store.New appends its own "memory" segment, so the
// real notes live at <home>/memory/memory; this reproduces that exact
// construction directly from a known home rather than round-tripping
// through internal/userhome + os.Getenv, since home is already known and
// exact here).
func luOpenUserMemory(t *testing.T, home string) *memory.Store {
	t.Helper()
	store, err := memory.New(filepath.Join(home, "memory"))
	if err != nil {
		t.Fatalf("open user memory store under %s: %v", home, err)
	}
	return store
}

// luRepResult is one rep's measurements.
type luRepResult struct {
	rep                         int
	userNoteCount               int
	goldNoteFound, provenanceOK bool
	noiseLeak                   bool
	onHit, offHit               bool
	learnElapsed                time.Duration
	learnReport                 string
}

// luProvenanceNames splits a "Promoted from: ..." value (provenanceValue's
// output, ", "-joined) back into its project names, trimmed.
func luProvenanceNames(prov string) []string {
	if strings.TrimSpace(prov) == "" {
		return nil
	}
	var out []string
	for _, n := range strings.Split(prov, ",") {
		out = append(out, strings.TrimSpace(n))
	}
	return out
}

func luContainsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// luRunRep drives one full rep: register+seed A/B/C/D under an isolated
// $CORTEX_HOME, run the real promotion pass, inspect the resulting user
// tier (G2/G3's raw material), then grade ARM-ON (same home) and ARM-OFF
// (a wholly separate, never-touched home+root — see the file header's
// "ARM-OFF isolation" section).
func luRunRep(t *testing.T, bin, endpoint string, rep int) luRepResult {
	t.Helper()
	res := luRepResult{rep: rep}

	onHome := t.TempDir()
	rootA, rootB, rootC, rootD := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()

	luProjectAdd(t, bin, onHome, "a", rootA)
	luProjectAdd(t, bin, onHome, "b", rootB)
	luProjectAdd(t, bin, onHome, "c", rootC)
	luProjectAdd(t, bin, onHome, "d", rootD) // registered but empty until the ARM-ON probe below

	luRunTurn(t, bin, onHome, endpoint, rootA, "seed-a", luSeedInputA())
	luEnsureProjectNoteOrFallback(t, bin, onHome, endpoint, rootA, "project-a", luSeedInputA(), "cross-src-gateway-fact", luGuardTerms)

	luRunTurn(t, bin, onHome, endpoint, rootB, "seed-b", luSeedInputB())
	luEnsureProjectNoteOrFallback(t, bin, onHome, endpoint, rootB, "project-b", luSeedInputB(), "cross-src-gateway-fact", luGuardTerms)

	luRunTurn(t, bin, onHome, endpoint, rootC, "seed-c", luNoiseInputC())
	luEnsureProjectNoteOrFallback(t, bin, onHome, endpoint, rootC, "project-c", luNoiseInputC(), "cross-src-noise-fact", []string{luNoiseCodewordC})

	report, elapsed := luRunLearnUser(t, bin, onHome, endpoint)
	res.learnReport = report
	res.learnElapsed = elapsed
	t.Logf("rep %d: cortex learn --user (%s): %s", rep, elapsed, report)

	userMem := luOpenUserMemory(t, onHome)
	metas, err := userMem.List()
	if err != nil {
		t.Fatalf("list user memory: %v", err)
	}
	res.userNoteCount = len(metas)
	for _, m := range metas {
		body, err := userMem.Read(m.Name)
		if err != nil {
			t.Logf("rep %d: read user note %s: %v", rep, m.Name, err)
			continue
		}
		prov := luProvenanceNames(findProvenanceLine(body))
		if strings.Contains(body, luCodeword) {
			res.goldNoteFound = true
			if luContainsName(prov, "a") && luContainsName(prov, "b") && len(prov) == 2 {
				res.provenanceOK = true
			}
		}
		if strings.Contains(body, luNoiseCodewordC) || luContainsName(prov, "c") {
			res.noiseLeak = true
		}
	}

	// ARM-ON: fresh session in D's own root, SAME $CORTEX_HOME — the
	// promoted note (if any) is visible via turn-start user-index injection.
	onReply := luRunTurn(t, bin, onHome, endpoint, rootD, "probe-on", luProbeQuestion)
	res.onHit = strings.Contains(onReply, luCodeword)
	floorGrade(t, fmt.Sprintf("rep %d ARM-ON", rep), onReply, luCodeword)

	// ARM-OFF: a wholly separate, never-touched $CORTEX_HOME and D root —
	// see the file header's "ARM-OFF isolation" section for why this beats
	// a copy-then-delete of onHome.
	offHome := t.TempDir()
	rootDOff := t.TempDir()
	luProjectAdd(t, bin, offHome, "d", rootDOff)
	offReply := luRunTurn(t, bin, offHome, endpoint, rootDOff, "probe-off", luProbeQuestion)
	res.offHit = strings.Contains(offReply, luCodeword)
	floorGrade(t, fmt.Sprintf("rep %d ARM-OFF", rep), offReply, luCodeword)

	return res
}

func luReportScoreboard(t *testing.T, results []luRepResult) {
	t.Helper()
	t.Logf("--- learn-user scoreboard (rep x measurement) ---")
	t.Logf("%-4s %-6s %-6s %-11s %-9s %-7s %-8s %10s", "rep", "on", "off", "user_notes", "gold", "prov_ok", "noise", "learn_ms")
	for _, r := range results {
		t.Logf("%-4d %-6v %-6v %-11d %-9v %-7v %-8v %10d",
			r.rep, r.onHit, r.offHit, r.userNoteCount, r.goldNoteFound, r.provenanceOK, r.noiseLeak, r.learnElapsed.Milliseconds())
	}
}

func luCountHits(results []luRepResult, pick func(luRepResult) bool) int {
	n := 0
	for _, r := range results {
		if pick(r) {
			n++
		}
	}
	return n
}

func TestLearnUser_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run the live learn --user cross-project promotion eval")
	}

	bin := filepath.Join(t.TempDir(), "cortex")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build cortex: %v\n%s", err, out)
	}

	endpoint := liveEnv("CORTEX_LEARN_USER_LIVE_ENDPOINT", "http://localhost:4000")
	reps := liveEnvInt("CORTEX_LEARN_USER_LIVE_REPS", 3)
	if reps < 3 {
		reps = 3 // G1's lift comparison needs n>=3, same floor as every sibling live eval
	}
	budgetMS := int64(liveEnvInt("CORTEX_LEARN_USER_LIVE_BUDGET_MS", 120_000))

	var results []luRepResult
	for i := 0; i < reps; i++ {
		rep := i + 1
		t.Run(fmt.Sprintf("rep-%d", rep), func(t *testing.T) {
			results = append(results, luRunRep(t, bin, endpoint, rep))
		})
	}

	luReportScoreboard(t, results)

	// G1 — cross-project lift: ARM-ON strictly beats ARM-OFF on needle recall.
	t.Run("G1_cross_project_lift", func(t *testing.T) {
		onHits := luCountHits(results, func(r luRepResult) bool { return r.onHit })
		offHits := luCountHits(results, func(r luRepResult) bool { return r.offHit })
		t.Logf("needle recall: ARM-ON %d/%d, ARM-OFF %d/%d", onHits, len(results), offHits, len(results))
		if onHits <= offHits {
			t.Errorf("NO-GO: ARM-ON (%d/%d) did not strictly beat ARM-OFF (%d/%d) on the cross-project needle — "+
				"a tie or loss here is a real no-go signal, not noise to explain away", onHits, len(results), offHits, len(results))
		}
	})

	// G2 — promotion precision: <=2x the gold fact count, nothing from C.
	t.Run("G2_promotion_precision", func(t *testing.T) {
		for _, r := range results {
			if r.userNoteCount > 2 {
				t.Errorf("rep %d: user tier gained %d notes, want <=2 (1 gold fact -> max 2x)", r.rep, r.userNoteCount)
			}
			if r.noiseLeak {
				t.Errorf("rep %d: a promoted note was sourced from (or contained) project C's noise fact", r.rep)
			}
		}
	})

	// G3 — provenance: the gold note names exactly a and b.
	t.Run("G3_provenance", func(t *testing.T) {
		for _, r := range results {
			if !r.goldNoteFound {
				t.Errorf("rep %d: no promoted note carried the gold codeword %s — nothing to check provenance on", r.rep, luCodeword)
				continue
			}
			if !r.provenanceOK {
				t.Errorf("rep %d: gold note's provenance did not name exactly [a b]", r.rep)
			}
		}
	})

	// G4 — bounded cost: learn --user wall-clock per rep stays under budget.
	t.Run("G4_bounded_cost", func(t *testing.T) {
		for _, r := range results {
			ms := r.learnElapsed.Milliseconds()
			t.Logf("rep %d: learn --user wall-clock %dms (budget %dms)", r.rep, ms, budgetMS)
			if ms > budgetMS {
				t.Errorf("rep %d: learn --user took %dms, over the pinned budget %dms", r.rep, ms, budgetMS)
			}
		}
	})
}
