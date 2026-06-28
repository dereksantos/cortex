package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// study_eval is the ø layer of the verification gate (docs/eval-design-example.md):
// the LIVE behavioral acceptance test. It runs study over a frozen set of probes
// and checks the digest answers the goal. It is a HARD gate — exit non-zero unless
// every probe passes — so an autonomous run reads pass/fail from the exit code.
//
// The probes are chosen to DISCRIMINATE the redesigned study (outline/grep/
// targeted-read, bounded, journal-aware) from the old navigator: each one leans on
// a capability the old path lacks or does poorly, so a green ø means the new study
// actually works, not that both clear a low bar. The security/bounds discriminators
// (path confinement, read clamp) live in the Δ layer as deterministic unit tests
// (study-subagent.md phase 3); ø owns the live behavioral discriminators below.
//
// Scoring is mechanical (deterministic): a probe passes when the required number of
// its gold facts appear in the digest. Bounded/completed/read-byte scoring is wired
// onto the engine's budget accounting when the unified runLoop lands (phase 5); the
// probes that need it are already defined here as its target.
//
// IMPORTANT — goal-hit alone under-discriminates. A flight check (2026-06-28) showed
// the old navigator passing the needle/bounded probes by brute-reading to the answer
// (4 reads / ~800 lines on a single-region goal). Goal-hit can't see that; the
// ReadBytes/Reads/Bounded scorer is what gives ø teeth on those probes, so wiring it
// in phase 5 is a HARD requirement, not optional. Today the only probe that
// discriminates on goal-hit is journal-recall (the old path can't search JSONL).
//
// Run with: `loop study-eval`.

// StudyProbe is one frozen acceptance case: a path to study, a goal, and the gold
// facts a correct digest must contain. Note records what the probe discriminates.
type StudyProbe struct {
	Path    string
	Goal    string
	Gold    []string // facts that must appear in the digest
	MinGold int      // how many of Gold are required; 0 means all of them
	Note    string   // what this probe discriminates (telemetry + docs)
}

// need is how many gold facts a digest must contain to pass.
func (p StudyProbe) need() int {
	if p.MinGold > 0 {
		return p.MinGold
	}
	return len(p.Gold)
}

// pass reports whether the digest meets the probe's gold-fact bar.
func (p StudyProbe) pass(digest string) bool {
	return countGoalHits(digest, p.Gold) >= p.need()
}

// studyProbes is the discriminating ø set. Each probe targets a capability the
// redesigned study has and the old navigator lacks or handles poorly.
var studyProbes = []StudyProbe{
	{
		Path: "pkg/llm",
		Goal: "Which function turns a backend server error into the message the user sees, and what does it do with special cases like a context-overflow or an unreachable backend?",
		Gold: []string{"wrapServerError"},
		Note: "needle: locate one symbol in a package — new study greps to file:line, old must scan an outline",
	},
	{
		Path:    "cmd/loop/main.go",
		Goal:    "How does the agent's inner tool loop stop a model that keeps re-issuing the same tool call?",
		Gold:    []string{"maxRepeatedToolCalls", "nudge", "repeat"},
		MinGold: 2,
		Note:    "bounded: the answer sits in one region of a ~3k-line file — new outlines then reads a span, old over-reads",
	},
	{
		Path:    "internal/shellrisk",
		Goal:    "How does shellrisk fetch threat intelligence over the network to decide whether a command is dangerous?",
		Gold:    []string{"Safe", "Risky", "Blocked", "classif"},
		MinGold: 2,
		Note:    "false premise: shellrisk makes no network calls — a grounded answer describes the static Safe/Risky/Blocked classifier instead of inventing a fetch",
	},
	{
		Path: ".cortex/journal",
		Goal: "Across the recorded sessions in this journal, what file at the repository root does the agent read into its seed context?",
		Gold: []string{"AGENTS.md"},
		Note: "recall over JSONL — new study greps the journal, old projectindex can't outline it",
	},
	{
		Path:    "cmd/loop",
		Goal:    "Name two things and explain how they connect: the function that parses tool calls out of the model's response, and the method that dispatches/executes a tool call. Trace a call from the model's output to execution and back.",
		Gold:    []string{"parseXMLToolCalls", "Execute"},
		MinGold: 2,
		Note:    "multi-hop: the answer spans main.go (parseXMLToolCalls) and tools.go (Execute) — needs grep to jump + targeted reads in both. Goal asks for the symbols by role so naming them is fair (not keyword-brittle); the discriminator is the cross-file jump, which the grep-less navigator fails (flails, exhausts iterations, finalize errors).",
	},
	{
		Path:    "cmd/loop/tools/tools.go",
		Goal:    "What tools are available to the agent and how are they registered and dispatched?",
		Gold:    []string{"Execute", "All"},
		MinGold: 2,
		Note:    "smoke floor: kept from the original set as a no-regression check",
	},
}

// studyEvalRow is one acceptance run over a probe.
type studyEvalRow struct {
	Path        string `json:"path"`
	Goal        string `json:"goal"`
	Model       string `json:"model"`
	Rep         int    `json:"rep"`
	LatencyMS   int64  `json:"latency_ms"`
	GoldPresent int    `json:"gold_present"` // gold facts found in the digest
	GoldNeed    int    `json:"gold_need"`    // gold facts required to pass
	Pass        bool   `json:"pass"`
	DigestChars int    `json:"digest_chars"`
	Note        string `json:"note"`
	Error       string `json:"error,omitempty"`
}

// countGoalHits reports how many of want's keywords appear in text
// (case-insensitive) — the mechanical acceptance signal.
func countGoalHits(text string, want []string) int {
	lower := strings.ToLower(text)
	hits := 0
	for _, w := range want {
		if strings.Contains(lower, strings.ToLower(w)) {
			hits++
		}
	}
	return hits
}

// runStudyEvalNav is the ø acceptance test: run study over each probe and check it
// answered the goal (enough gold facts present), with latency. Reps overridable via
// CORTEX_NAV_REPS. HARD gate: exits non-zero unless every probe passes. Run:
// `loop study-eval`.
func runStudyEvalNav() {
	session := NewCortexSession()
	reps := envInt("CORTEX_NAV_REPS", 1)
	// Per-probe wall-clock cap. A probe that thrashes (e.g. study can't search
	// the corpus and burns its whole iteration budget) must FAIL FAST, never hang
	// the gate — a flight check saw the old navigator grind ~15min on the journal
	// probe. Override with CORTEX_STUDY_PROBE_TIMEOUT (seconds).
	probeTimeout := time.Duration(envInt("CORTEX_STUDY_PROBE_TIMEOUT", 120)) * time.Second

	var lat []float64
	passes, total, errs := 0, 0, 0
	for _, p := range studyProbes {
		for rep := 0; rep < reps; rep++ {
			row := studyEvalRow{Path: p.Path, Goal: p.Goal, Model: session.Study.Model, Rep: rep, GoldNeed: p.need(), Note: p.Note}
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			digest, err := session.runNavigator(ctx, p.Path, p.Goal, 0)
			cancel()
			row.LatencyMS = time.Since(start).Milliseconds()
			if err != nil {
				row.Error = err.Error()
			} else {
				row.DigestChars = len(digest)
				row.GoldPresent = countGoalHits(digest, p.Gold)
				row.Pass = p.pass(digest)
			}
			b, _ := json.Marshal(row)
			fmt.Println(string(b)) // JSONL — one row per (probe, rep)

			total++
			if row.Error != "" {
				errs++
				continue
			}
			lat = append(lat, float64(row.LatencyMS)/1000)
			if row.Pass {
				passes++
			}
		}
	}

	fmt.Printf("\n--- study acceptance (model: %s, n=%d/probe, %d probes) ---\n",
		session.Study.Model, reps, len(studyProbes))
	fmt.Printf("pass %d/%d   median latency %.1fs   errors %d\n", passes, total, median(lat), errs)

	// The T gate: this eval is a hard red/green check, not a report. Green only
	// when every probe passes (enough gold facts present) with no errors — so an
	// autonomous run reads pass/fail straight from the exit code.
	if errs > 0 || passes != total {
		os.Exit(1)
	}
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// envInt reads a positive int from an env var, falling back to def.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return def
}
