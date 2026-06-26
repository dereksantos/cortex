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

// study_eval: the navigator acceptance test. Study is a subagent — prefill it
// with the map + a goal, let it read, and check it answered. The acceptance
// criterion is deliberately simple and verifiable: does the answer state the
// facts a correct answer must contain (Want)? Pass = every Want fact present.
// Run with: `loop study-eval`.

// navCase is a fixture: the path, the goal, and the must-mention facts a correct
// answer contains (the acceptance criteria).
type navCase struct {
	Path string
	Goal string
	Want []string // every keyword must appear in the answer to pass
}

var navEvalCases = []navCase{
	{"cmd/loop/main.go", "how is REPL input dispatched to commands and the agent", []string{"Resolve", "tool"}},
	{"cmd/loop/tools/tools.go", "what tools are available to the agent and how are they registered", []string{"Execute", "All"}},
	{"docs/loop-production-harness.md", "what is the plan to make cmd/loop a production harness", []string{"harness"}},
}

// navEvalRow is one acceptance run over a case.
type navEvalRow struct {
	Path        string `json:"path"`
	Goal        string `json:"goal"`
	Model       string `json:"model"`
	Rep         int    `json:"rep"`
	LatencyMS   int64  `json:"latency_ms"`
	GoalHits    int    `json:"goal_hits"` // Want keywords present
	GoalWant    int    `json:"goal_want"`
	Pass        bool   `json:"pass"` // all Want present
	DigestChars int    `json:"digest_chars"`
	Error       string `json:"error,omitempty"`
}

// countGoalHits reports how many of want's keywords appear in text
// (case-insensitive) — the acceptance signal.
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

// runStudyEvalNav is the study acceptance test: run the subagent over each
// fixture and check it answered the goal (all Want facts present), with latency.
// Reps overridable via CORTEX_NAV_REPS. Run: `loop study-eval`.
func runStudyEvalNav() {
	session := NewCortexSession()
	reps := envInt("CORTEX_NAV_REPS", 1)

	var lat []float64
	passes, total, errs := 0, 0, 0
	for _, c := range navEvalCases {
		for rep := 0; rep < reps; rep++ {
			row := navEvalRow{Path: c.Path, Goal: c.Goal, Model: session.Study.Model, Rep: rep, GoalWant: len(c.Want)}
			start := time.Now()
			digest, err := session.runNavigator(context.Background(), c.Path, c.Goal, 0)
			row.LatencyMS = time.Since(start).Milliseconds()
			if err != nil {
				row.Error = err.Error()
			} else {
				row.DigestChars = len(digest)
				row.GoalHits = countGoalHits(digest, c.Want)
				row.Pass = row.GoalHits == len(c.Want)
			}
			b, _ := json.Marshal(row)
			fmt.Println(string(b)) // JSONL — one row per (case, rep)

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

	fmt.Printf("\n--- study acceptance (model: %s, n=%d/case, %d cases) ---\n",
		session.Study.Model, reps, len(navEvalCases))
	fmt.Printf("pass %d/%d   median latency %.1fs   errors %d\n", passes, total, median(lat), errs)
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
