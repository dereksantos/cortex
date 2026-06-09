package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
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

// studyEvalSweep is the density (chunk count) sweep the runner measures.
var studyEvalSweep = []int{4, 6, 8}

// studyEvalRepeats: runs per cell. The sampler and backend latency are noisy, so
// we repeat and aggregate to tell signal (a density effect) from variance.
const studyEvalRepeats = 3

// studyEvalRow is the per-run measured result, emitted as JSONL.
type studyEvalRow struct {
	Path            string  `json:"path"`
	Goal            string  `json:"goal"`
	Model           string  `json:"model"`
	Chunks          int     `json:"chunks"`
	Rep             int     `json:"rep"`
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

// measureCell runs study once over a case at a given chunk count and scores it.
func measureCell(cs *CortexSession, c studyEvalCase, chunks int) studyEvalRow {
	row := studyEvalRow{Path: c.Path, Goal: c.Goal, Model: cs.Study.Model, Chunks: chunks}
	start := time.Now()
	res, err := cs.runStudy(c.Path, c.Goal, 1, chunks)
	row.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Stopped = res.Stopped
	row.CoveragePct = 100 * res.CoveragePct
	row.DigestChars = len(strings.Join(res.Digests, ""))
	if data, derr := os.ReadFile(c.Path); derr == nil {
		row.Citations, row.Grounded = scoreGroundedness(string(data), res)
		if row.Citations > 0 {
			row.GroundednessPct = 100 * float64(row.Grounded) / float64(row.Citations)
		}
	}
	return row
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func minMax(xs []float64) (lo, hi float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	lo, hi = xs[0], xs[0]
	for _, x := range xs[1:] {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return lo, hi
}

// runStudyEval sweeps density × fixtures, repeats each cell studyEvalRepeats
// times (emitting a JSONL row per run), then prints a per-cell aggregate so the
// median trend and the spread (signal vs variance) are both visible.
func runStudyEval() {
	session := NewCortexSession()
	var rows []studyEvalRow

	for _, chunks := range studyEvalSweep {
		for _, c := range studyEvalCases {
			for rep := 0; rep < studyEvalRepeats; rep++ {
				row := measureCell(session, c, chunks)
				row.Rep = rep
				rows = append(rows, row)
				b, _ := json.Marshal(row)
				fmt.Println(string(b)) // JSONL — one row per (chunks, case, rep)
			}
		}
	}

	fmt.Printf("\n--- study-eval density sweep (model: %s, n=%d/cell) ---\n", session.Study.Model, studyEvalRepeats)
	fmt.Printf("%-30s %4s %8s %7s %7s %s\n", "file", "k", "lat(s)", "cov%", "cites", "grounded% (med [min-max])")
	for _, chunks := range studyEvalSweep {
		for _, c := range studyEvalCases {
			var lat, cov, cites, grnd []float64
			read := false
			for _, r := range rows {
				if r.Path != c.Path || r.Chunks != chunks || r.Error != "" {
					continue
				}
				if r.Stopped == "read" {
					read = true
				}
				lat = append(lat, float64(r.LatencyMS)/1000)
				cov = append(cov, r.CoveragePct)
				cites = append(cites, float64(r.Citations))
				grnd = append(grnd, r.GroundednessPct)
			}
			name := shortName(c.Path)
			if read {
				fmt.Printf("%-30s %4d %8.1f   read (fit, whole file)\n", name, chunks, median(lat))
				continue
			}
			glo, ghi := minMax(grnd)
			fmt.Printf("%-30s %4d %8.1f %6.0f%% %7.0f %5.0f%% [%.0f-%.0f]\n",
				name, chunks, median(lat), median(cov), median(cites), median(grnd), glo, ghi)
		}
	}
}

func shortName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
