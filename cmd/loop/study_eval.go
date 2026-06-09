package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/study"
)

// study_eval: a small dial-and-measure runner for the study tool. Runs study
// over a fixture set and scores each case on latency, coverage, and
// groundedness (are the cited line ranges valid and do the cited symbols
// actually exist in the file?) — emitting a JSONL row per case so density,
// passes, and study-model changes become measured deltas instead of eyeballed
// one-offs. Run with: `loop study-eval`.

// studyEvalCase is one (file, goal) pair to measure.
type studyEvalCase struct {
	Path string
	Goal string
}

// studyEvalCases is the fixture set — files in this repo with grounded answers.
var studyEvalCases = []studyEvalCase{
	{"cmd/cortex/commands/repl.go", "how are slash commands dispatched"},
	{"cmd/cortex/commands/study.go", "what subcommands does the study command support"},
}

// studyEvalRow is the per-case measured result, emitted as JSONL.
type studyEvalRow struct {
	Path            string  `json:"path"`
	Goal            string  `json:"goal"`
	Model           string  `json:"model"`
	Stopped         string  `json:"stopped"`
	LatencyMS       int64   `json:"latency_ms"`
	CoveragePct     float64 `json:"coverage_pct"`
	Citations       int     `json:"citations"`
	Grounded        int     `json:"grounded"`
	GroundednessPct float64 `json:"groundedness_pct"`
	DigestChars     int     `json:"digest_chars"`
	Error           string  `json:"error,omitempty"`
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// scoreGroundedness checks each citation: its line range must lie inside the
// file, and its claim must reference at least one identifier-looking symbol that
// actually appears in the file. A rough but automatic hallucination check.
func scoreGroundedness(content string, res study.StudyLoopResult) (total, grounded int) {
	lines := strings.Count(content, "\n") + 1
	for _, c := range res.Citations {
		total++
		if c.LineStart < 1 || c.LineStart > c.LineEnd || c.LineEnd > lines {
			continue // line range out of bounds → not grounded
		}
		for _, sym := range identRe.FindAllString(c.Claim, -1) {
			if hasUpper(sym) && strings.Contains(content, sym) {
				grounded++
				break
			}
		}
	}
	return total, grounded
}

// runStudyEval runs every fixture, scores it, prints a JSONL row per case, then
// a human summary table.
func runStudyEval() {
	session := NewCortexSession()
	var rows []studyEvalRow

	for _, c := range studyEvalCases {
		row := studyEvalRow{Path: c.Path, Goal: c.Goal, Model: session.Study.Model}
		start := time.Now()
		res, err := session.runStudy(c.Path, c.Goal, 1)
		row.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Stopped = res.Stopped
			row.CoveragePct = 100 * res.CoveragePct
			row.DigestChars = len(strings.Join(res.Digests, ""))
			if data, derr := os.ReadFile(c.Path); derr == nil {
				row.Citations, row.Grounded = scoreGroundedness(string(data), res)
				if row.Citations > 0 {
					row.GroundednessPct = 100 * float64(row.Grounded) / float64(row.Citations)
				}
			}
		}
		rows = append(rows, row)
		b, _ := json.Marshal(row)
		fmt.Println(string(b)) // JSONL — one structured row per case
	}

	fmt.Printf("\n--- study-eval summary (model: %s) ---\n", session.Study.Model)
	fmt.Printf("%-42s %7s %6s %6s %s\n", "file", "lat(s)", "cov%", "cites", "grounded%")
	for _, r := range rows {
		switch {
		case r.Error != "":
			fmt.Printf("%-42s  ERROR: %s\n", r.Path, r.Error)
		case r.Stopped == "read":
			// File fit the budget → read whole. No sampling/citations to ground.
			fmt.Printf("%-42s %7.1f   read (fit, whole file)\n", r.Path, float64(r.LatencyMS)/1000)
		default:
			fmt.Printf("%-42s %7.1f %5.0f%% %6d %8.0f%%\n",
				r.Path, float64(r.LatencyMS)/1000, r.CoveragePct, r.Citations, r.GroundednessPct)
		}
	}
}
