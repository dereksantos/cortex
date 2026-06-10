package main

import (
	"context"
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

// studyEvalCell is one sweep point: k chunks of fill×window each. Cells with
// equal k×fill sample the same total data at different granularities, so the
// sweep separates "how much was read" from "how fragmented it was".
type studyEvalCell struct {
	Chunks int
	Fill   float64 // 0 → engine default (1/8)
}

// studyEvalSweep holds total data constant (k × fill = 1, the full sample
// budget) while shrinking fragment size 6×: 8×3072tok → 32×768tok → 48×512tok
// (the 2KB min-chunk clamp floors the last cell). If groundedness improves as
// fragments shrink, fragment incoherence — chunks cut mid-symbol — is the
// failure mode and boundary-aligned (AST) chunking is the fix.
var studyEvalSweep = []studyEvalCell{
	{Chunks: 8, Fill: 0}, // baseline: default 1/8 fill
	{Chunks: 32, Fill: 1.0 / 32},
	{Chunks: 48, Fill: 1.0 / 48},
}

// studyEvalRepeats: runs per cell. The sampler and backend latency are noisy, so
// we repeat and aggregate to tell signal (a density effect) from variance.
const studyEvalRepeats = 3

// studyEvalRow is the per-run measured result, emitted as JSONL.
type studyEvalRow struct {
	Path            string  `json:"path"`
	Goal            string  `json:"goal"`
	Model           string  `json:"model"`
	Chunks          int     `json:"chunks"`
	Fill            float64 `json:"fill,omitempty"` // per-chunk window fraction; 0 = default 1/8
	Rep             int     `json:"rep"`
	Stopped         string  `json:"stopped"`
	LatencyMS       int64   `json:"latency_ms"`
	CoveragePct     float64 `json:"coverage_pct"`
	Citations       int     `json:"citations"`
	Grounded        int     `json:"grounded"`
	Failed          int     `json:"failed"`
	Unscored        int     `json:"unscored"`
	GroundednessPct float64 `json:"groundedness_pct"` // grounded / (grounded+failed)
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

// scoreGroundedness classifies each citation three ways, so "ungrounded" splits
// into real model errors vs scorer limits:
//
//	grounded — a symbol named in the claim appears WITHIN the cited line range
//	failed   — the claim names a symbol, but it is NOT at the cited lines (wrong
//	           location or hallucinated): a genuine model error
//	unscored — the claim names no extractable identifier, so we can't verify it
//	           (a scorer limitation, not necessarily a model error)
//
// Checking the symbol is at the CITED lines (not merely somewhere in the file)
// is the stronger test — it catches a plausible claim pinned to the wrong place.
func scoreGroundedness(content string, res study.StudyLoopResult) (grounded, failed, unscored int) {
	fileLines := strings.Split(content, "\n")
	for _, c := range res.Citations {
		var idents []string
		for _, s := range identRe.FindAllString(c.Claim, -1) {
			if hasUpper(s) {
				idents = append(idents, s)
			}
		}
		if len(idents) == 0 {
			unscored++ // nothing identifier-shaped to verify
			continue
		}
		if c.LineStart < 1 || c.LineStart > c.LineEnd || c.LineEnd > len(fileLines) {
			failed++ // out-of-range line range is a clear error
			continue
		}
		cited := strings.Join(fileLines[c.LineStart-1:c.LineEnd], "\n")
		ok := false
		for _, s := range idents {
			if strings.Contains(cited, s) {
				ok = true
				break
			}
		}
		if ok {
			grounded++
		} else {
			failed++
		}
	}
	return grounded, failed, unscored
}

// measureCell runs study once over a case at a given sweep cell and scores it.
func measureCell(cs *CortexSession, c studyEvalCase, cell studyEvalCell) studyEvalRow {
	row := studyEvalRow{Path: c.Path, Goal: c.Goal, Model: cs.Study.Model, Chunks: cell.Chunks, Fill: cell.Fill}
	start := time.Now()
	res, err := cs.runStudy(context.Background(), c.Path, c.Goal, 1, cell.Chunks, cell.Fill)
	row.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Stopped = res.Stopped
	row.CoveragePct = 100 * res.CoveragePct
	row.DigestChars = len(strings.Join(res.Digests, ""))
	if data, derr := os.ReadFile(c.Path); derr == nil {
		row.Grounded, row.Failed, row.Unscored = scoreGroundedness(string(data), res)
		row.Citations = row.Grounded + row.Failed + row.Unscored
		if g := row.Grounded + row.Failed; g > 0 {
			row.GroundednessPct = 100 * float64(row.Grounded) / float64(g)
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

// runStudyEval sweeps density × fixtures, repeats each cell studyEvalRepeats
// times (emitting a JSONL row per run), then prints a per-cell aggregate so the
// median trend and the spread (signal vs variance) are both visible.
func runStudyEval() {
	session := NewCortexSession()
	var rows []studyEvalRow

	for _, cell := range studyEvalSweep {
		for _, c := range studyEvalCases {
			for rep := 0; rep < studyEvalRepeats; rep++ {
				row := measureCell(session, c, cell)
				row.Rep = rep
				rows = append(rows, row)
				b, _ := json.Marshal(row)
				fmt.Println(string(b)) // JSONL — one row per (cell, case, rep)
			}
		}
	}

	fmt.Printf("\n--- study-eval granularity sweep (model: %s, n=%d/cell, equal data per cell) ---\n", session.Study.Model, studyEvalRepeats)
	fmt.Printf("%-26s %3s %6s %7s %6s   %s\n", "file", "k", "fill", "lat(s)", "cov%", "citations summed across reps")
	for _, cell := range studyEvalSweep {
		for _, c := range studyEvalCases {
			var lat, cov []float64
			var g, f, u int
			read := false
			for _, r := range rows {
				if r.Path != c.Path || r.Chunks != cell.Chunks || r.Fill != cell.Fill || r.Error != "" {
					continue
				}
				if r.Stopped == "read" {
					read = true
				}
				lat = append(lat, float64(r.LatencyMS)/1000)
				cov = append(cov, r.CoveragePct)
				g, f, u = g+r.Grounded, f+r.Failed, u+r.Unscored
			}
			name := shortName(c.Path)
			fillLabel := "1/8"
			if cell.Fill > 0 {
				fillLabel = fmt.Sprintf("1/%.0f", 1/cell.Fill)
			}
			if read {
				fmt.Printf("%-26s %3d %6s %7.1f   read (fit, whole file)\n", name, cell.Chunks, fillLabel, median(lat))
				continue
			}
			gp := 0.0
			if g+f > 0 {
				gp = 100 * float64(g) / float64(g+f)
			}
			// grounded% = real-grounding rate; unscored = citations the scorer
			// couldn't verify (claim had no extractable identifier).
			fmt.Printf("%-26s %3d %6s %7.1f %5.0f%%   grounded=%d failed=%d unscored=%d  (%.0f%% grounded)\n",
				name, cell.Chunks, fillLabel, median(lat), median(cov), g, f, u, gp)
		}
	}
}

func shortName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
