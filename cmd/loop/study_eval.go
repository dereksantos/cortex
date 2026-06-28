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

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/tools"
)

// study_eval is the ø layer of the verification gate (docs/eval-design-example.md):
// the LIVE behavioral acceptance test. It drives the real Study subagent over a
// frozen set of probes and scores both the digest (goal-hit) AND the mechanical
// run shape (stop-reason, per-tool counts, bytes, tokens) from the engine's
// accounting. It is a HARD gate — exit non-zero unless every probe passes — so an
// autonomous run reads pass/fail from the exit code.
//
// goal-hit alone under-discriminates: a flight check (2026-06-28) showed the old
// navigator passing needle/bounded probes by brute-reading to the answer (4 reads
// / ~800 lines / 0 greps on a single-region goal). The locate-then-read scorer
// (per-tool counts + bounded + clean-finalize) is what gives ø teeth, so it is a
// HARD acceptance criterion, not a report. Run with: `loop study-eval`.

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
// redesigned study has and the old navigator lacked or handled poorly. The two
// paths engine phase 3 MOVED are re-pointed here (path strings only — goals and
// gold facts stay frozen): tools.go → internal/tools/tools.go, and the multi-hop
// root widened to "." so Execute (now in internal/tools) stays reachable
// alongside parseXMLToolCalls (cmd/loop).
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
		Path:    ".",
		Goal:    "Name two things and explain how they connect: the function that parses tool calls out of the model's response, and the method that dispatches/executes a tool call. Trace a call from the model's output to execution and back.",
		Gold:    []string{"parseXMLToolCalls", "Execute"},
		MinGold: 2,
		Note:    "multi-hop: the answer spans cmd/loop (parseXMLToolCalls) and internal/tools (Execute) — needs grep to jump across packages + targeted reads in both. Root widened from cmd/loop to '.' after phase 3 moved Execute into internal/tools (a moved-path re-point, not a weakening).",
	},
	{
		Path:    "internal/tools/tools.go",
		Goal:    "What tools are available to the agent and how are they registered and dispatched?",
		Gold:    []string{"Execute", "All"},
		MinGold: 2,
		Note:    "smoke floor: kept as a no-regression check; path re-pointed from cmd/loop/tools/tools.go after the phase-3 rename",
	},
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

// studyEvalRow is one acceptance run over a probe: goal-hit AND the full
// mechanical block, so a study that over-reads to the answer still fails.
type studyEvalRow struct {
	Path             string  `json:"path"`
	Model            string  `json:"model"`
	Rep              int     `json:"rep"`
	LatencyMS        int64   `json:"latency_ms"`
	GoalHit          float64 `json:"goal_hit"`
	GoldPresent      int     `json:"gold_present"`
	GoldNeed         int     `json:"gold_need"`
	StopReason       string  `json:"stop_reason"`
	FinalizeForced   bool    `json:"finalize_forced"`
	Iterations       int     `json:"iterations"`
	Outlines         int     `json:"outlines"`
	Greps            int     `json:"greps"`
	Reads            int     `json:"reads"`
	ToolErrs         int     `json:"tool_errs"`
	ReadBytes        int     `json:"read_bytes"`
	Bounded          bool    `json:"bounded"`
	InputTokens      int     `json:"tokens_in"`
	OutputTokens     int     `json:"tokens_out"`
	PeakOutputTokens int     `json:"peak_output_tokens"`
	MaxTokensClamped bool    `json:"max_tokens_clamped"`
	GoalHitPer1k     float64 `json:"goal_hit_per_1k_out"`
	Pass             bool    `json:"pass"`
	DigestChars      int     `json:"digest_chars"`
	Note             string  `json:"note"`
	Error            string  `json:"error,omitempty"`
}

// runStudyEvalNav is the ø acceptance test: drive the Study subagent over each
// probe and score goal-hit AND the mechanical run shape. Reps overridable via
// CORTEX_STUDY_REPS. HARD gate: exits non-zero unless every probe passes.
func runStudyEvalNav() {
	session := NewCortexSession()
	reps := envInt("CORTEX_STUDY_REPS", envInt("CORTEX_NAV_REPS", 1)) // CORTEX_NAV_REPS kept as a deprecated alias
	// Per-probe wall-clock cap: a probe that thrashes must FAIL FAST, never hang
	// the gate. Override with CORTEX_STUDY_PROBE_TIMEOUT (seconds).
	probeTimeout := time.Duration(envInt("CORTEX_STUDY_PROBE_TIMEOUT", 120)) * time.Second

	type probeAgg struct {
		passes int
		n      int
		lat    []float64
	}
	aggs := make([]probeAgg, len(studyProbes))

	passes, total, errs := 0, 0, 0
	for pi, p := range studyProbes {
		for rep := 0; rep < reps; rep++ {
			row := studyEvalRow{Path: p.Path, Model: session.Study.Model, Rep: rep, GoldNeed: p.need(), Note: p.Note}
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			ol, _ := session.Outline(p.Path, tools.StudySeedBudget)
			digest, stats, err := session.runSubagentStats(ctx, tools.Study, tools.StudySeed(p.Goal, p.Path, ol))
			cancel()
			row.LatencyMS = time.Since(start).Milliseconds()
			fillMechanical(&row, stats)
			total++
			aggs[pi].n++
			aggs[pi].lat = append(aggs[pi].lat, float64(row.LatencyMS)/1000)

			if err != nil {
				row.Error = err.Error()
				errs++
			} else {
				row.DigestChars = len(digest)
				row.GoldPresent = countGoalHits(digest, p.Gold)
				row.GoalHit = float64(row.GoldPresent) / float64(max1(len(p.Gold)))
				if row.OutputTokens > 0 {
					row.GoalHitPer1k = row.GoalHit / (float64(row.OutputTokens) / 1000)
				}
				// Acceptance has TEETH: the gold-fact bar AND a clean, bounded,
				// unclamped run. A study that brute-reads to the answer (bound-forced
				// finalize, or over-budget) fails even at goal-hit 1.0.
				row.Pass = p.pass(digest) &&
					row.StopReason == "clean-finalize" &&
					row.Bounded && !row.MaxTokensClamped
				if row.Pass {
					passes++
					aggs[pi].passes++
				}
			}
			session.emitStudyResult(studyResultPayload(p, row))
			b, _ := json.Marshal(row)
			fmt.Println(string(b)) // JSONL — one row per (probe, rep)
		}
	}

	// model × probe table: per-probe k/n pass-rate + p50/p95 latency.
	fmt.Printf("\n--- study acceptance (model: %s, n=%d/probe, %d probes) ---\n",
		session.Study.Model, reps, len(studyProbes))
	for pi, p := range studyProbes {
		fmt.Printf("  %-26s %d/%d  p50 %.1fs  p95 %.1fs\n",
			truncate(p.Path, 26), aggs[pi].passes, aggs[pi].n,
			percentile(aggs[pi].lat, 50), percentile(aggs[pi].lat, 95))
	}
	fmt.Printf("pass %d/%d   errors %d\n", passes, total, errs)

	// The T gate: green only when EVERY probe passes (goal-hit + clean-finalize +
	// bounded + unclamped) with no errors — read straight from the exit code.
	if errs > 0 || passes != total {
		os.Exit(1)
	}
}

// fillMechanical copies the engine's run stats into the eval row.
func fillMechanical(row *studyEvalRow, stats loopStats) {
	row.StopReason = stats.StopReason
	row.FinalizeForced = stats.FinalizeForced
	row.Iterations = stats.Iterations
	row.Outlines = stats.Outlines
	row.Greps = stats.Greps
	row.Reads = stats.Reads
	row.ToolErrs = stats.ToolErrs
	row.ReadBytes = stats.ReadBytes
	row.Bounded = stats.ReadBytes <= tools.Study.Bounds.ReadBudgetBytes
	row.InputTokens = stats.InputTokens
	row.OutputTokens = stats.OutputTokens
	row.PeakOutputTokens = stats.PeakOutputTokens
	row.MaxTokensClamped = stats.MaxTokensClamped
}

// studyResultPayload maps an eval row to the canonical always-on telemetry shape.
func studyResultPayload(p StudyProbe, row studyEvalRow) journal.StudyResultPayload {
	return journal.StudyResultPayload{
		SchemaVersion:        "1",
		RunID:                "study-eval",
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		ScenarioID:           p.Path,
		Harness:              "study",
		Model:                row.Model,
		LatencyMs:            row.LatencyMS,
		TokensIn:             row.InputTokens,
		TokensOut:            row.OutputTokens,
		AgentTurnsTotal:      row.Iterations,
		TaskSuccess:          row.Pass,
		TaskSuccessCriterion: fmt.Sprintf("goal-hit≥%d/%d & clean-finalize & bounded & unclamped", p.need(), len(p.Gold)),
		GoalHit:              row.GoalHit,
		StopReason:           row.StopReason,
		FinalizeForced:       row.FinalizeForced,
		PeakOutputTokens:     row.PeakOutputTokens,
		MaxTokensClamped:     row.MaxTokensClamped,
		Outlines:             row.Outlines,
		Greps:                row.Greps,
		Reads:                row.Reads,
		ToolErrs:             row.ToolErrs,
		ReadBytes:            row.ReadBytes,
		Bounded:              row.Bounded,
		Error:                row.Error,
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// percentile returns the p-th percentile (0–100) of xs (nearest-rank), or 0 for
// an empty slice.
func percentile(xs []float64, p int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	idx := (p * len(s)) / 100
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
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
