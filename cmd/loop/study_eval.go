package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// studyEvalCases is the fixture set — one case per format family, so the
// coherence-unit law (boundary.go) is tested beyond code. The NDJSON case is
// appended at runtime by ensureStudyEvalJSONL (deterministic synthetic
// telemetry; repo files of that shape aren't stable fixtures).
var studyEvalCases = []studyEvalCase{
	{"cmd/cortex/commands/repl.go", "how are slash commands dispatched"},
	{"cmd/cortex/commands/study.go", "what subcommands does the study command support"},
	{"docs/eval.md", "what are the tree eval types and what does each one test"},
}

// ensureStudyEvalJSONL writes the deterministic NDJSON fixture (synthetic
// service telemetry, ~220KB) and returns its path. Errors are seeded into
// exactly two services so the goal has grounded answers: billing → "timeout",
// auth → "token_expired"; every other record is error-free. Content depends
// only on the constants below, so reps and re-runs score against identical
// bytes.
func ensureStudyEvalJSONL() (string, error) {
	path := filepath.Join(os.TempDir(), "cortex-study-eval-events.jsonl")
	services := []string{"billing", "checkout", "inventory", "auth", "search", "shipping"}
	routes := []string{"/v1/charge", "/v1/cart", "/v1/stock", "/v1/token", "/v1/query", "/v1/label"}
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		svc := services[i%len(services)]
		status, errField := 200, ""
		switch {
		case svc == "billing" && i%96 == 0:
			status, errField = 500, "timeout"
		case svc == "auth" && i%129 == 0:
			status, errField = 401, "token_expired"
		}
		fmt.Fprintf(&b, `{"id":%d,"service":%q,"route":%q,"status":%d,"latency_ms":%d`,
			10000+i, svc, routes[i%len(routes)], status, 20+(i*37)%480)
		if errField != "" {
			fmt.Fprintf(&b, `,"error":%q`, errField)
		}
		b.WriteString("}\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// studyEvalCell is one sweep point: k chunks of fill×window each. Cells with
// equal k×fill sample the same total data at different granularities, so the
// sweep separates "how much was read" from "how fragmented it was".
type studyEvalCell struct {
	Chunks   int
	Fill     float64 // 0 → engine default (1/8)
	Numbered *bool   // per-line snippet numbering; nil → format default
}

// studyEvalSweep compares the two regimes at equal total data (the full
// sample budget): coarse window/8 fragments cut anywhere, vs the zero-knob
// auto path (Chunks=0, Fill=0) where the engine sizes fragments to the
// format's coherence unit, snaps them to boundaries, and derives k from the
// budget. The 2026-06-10 granularity sweep established the law on Go
// (57% → 100% grounded); this pairing tests it per format.
var studyEvalSweep = []studyEvalCell{
	{Chunks: 8, Fill: 1.0 / 8}, // coarse baseline: 8 × window/8, arbitrary cuts
	{Chunks: 0, Fill: 0},       // auto: unit-sized, boundary-snapped, k = budget/unit
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
	Fill            float64 `json:"fill,omitempty"`     // per-chunk window fraction; 0 = default 1/8
	Numbered        *bool   `json:"numbered,omitempty"` // line-numbering override; nil = format default
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
var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z_-]{4,}`)

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// claimAnchors extracts the verifiable tokens from a citation's claim, per
// format, plus how many must appear in the cited range for the citation to
// count as grounded. Code claims name symbols (CamelCase/underscored idents —
// one suffices; it's near-unique). Prose and data claims use ordinary words,
// individually generic, so two distinct hits are required before crediting
// the location.
func claimAnchors(claim, lang string) (anchors []string, need int) {
	switch lang {
	case "md", "txt", "rst", "json", "yaml", "csv":
		seen := map[string]bool{}
		for _, w := range wordRe.FindAllString(claim, -1) {
			w = strings.ToLower(w)
			if !seen[w] {
				seen[w] = true
				anchors = append(anchors, w)
			}
		}
		return anchors, 2
	default:
		for _, s := range identRe.FindAllString(claim, -1) {
			if hasUpper(s) {
				anchors = append(anchors, s)
			}
		}
		return anchors, 1
	}
}

// scoreGroundedness classifies each citation three ways, so "ungrounded" splits
// into real model errors vs scorer limits:
//
//	grounded — enough claim anchors (per-format, see claimAnchors) appear
//	           WITHIN the cited line range
//	failed   — the claim has verifiable anchors, but they are NOT at the cited
//	           lines (wrong location or hallucinated): a genuine model error
//	unscored — the claim has no extractable anchors, so we can't verify it
//	           (a scorer limitation, not necessarily a model error)
//
// Checking anchors are at the CITED lines (not merely somewhere in the file)
// is the stronger test — it catches a plausible claim pinned to the wrong place.
func scoreGroundedness(content, lang string, res study.StudyLoopResult) (grounded, failed, unscored int) {
	fileLines := strings.Split(content, "\n")
	for _, c := range res.Citations {
		anchors, need := claimAnchors(c.Claim, lang)
		if len(anchors) < need {
			unscored++ // not enough verifiable material in the claim
			continue
		}
		if c.LineStart < 1 || c.LineStart > c.LineEnd || c.LineEnd > len(fileLines) {
			failed++ // out-of-range line range is a clear error
			continue
		}
		cited := strings.Join(fileLines[c.LineStart-1:c.LineEnd], "\n")
		if need > 1 {
			cited = strings.ToLower(cited) // prose/data anchors are case-normalized
		}
		hits := 0
		for _, a := range anchors {
			if strings.Contains(cited, a) {
				hits++
				if hits >= need {
					break
				}
			}
		}
		if hits >= need {
			grounded++
		} else {
			failed++
		}
	}
	return grounded, failed, unscored
}

// measureCell runs study once over a case at a given sweep cell and scores it.
func measureCell(cs *CortexSession, c studyEvalCase, cell studyEvalCell) studyEvalRow {
	row := studyEvalRow{Path: c.Path, Goal: c.Goal, Model: cs.Study.Model, Chunks: cell.Chunks, Fill: cell.Fill, Numbered: cell.Numbered}
	start := time.Now()
	res, err := cs.runStudy(context.Background(), c.Path, c.Goal, 1, cell.Chunks, cell.Fill, cell.Numbered, 0, false)
	row.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Stopped = res.Stopped
	row.CoveragePct = 100 * res.CoveragePct
	row.DigestChars = len(strings.Join(res.Digests, ""))
	if data, derr := os.ReadFile(c.Path); derr == nil {
		lang := langForPath(c.Path)
		row.Grounded, row.Failed, row.Unscored = scoreGroundedness(string(data), lang, res)
		row.Citations = row.Grounded + row.Failed + row.Unscored
		if g := row.Grounded + row.Failed; g > 0 {
			row.GroundednessPct = 100 * float64(row.Grounded) / float64(g)
		}
	}
	return row
}

// langForPath maps a fixture path to the scorer's format family.
func langForPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".markdown":
		return "md"
	case ".txt":
		return "txt"
	case ".json", ".jsonl", ".ndjson":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".csv", ".tsv":
		return "csv"
	}
	return "code"
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

	cases := studyEvalCases
	if jsonl, err := ensureStudyEvalJSONL(); err == nil {
		cases = append(cases, studyEvalCase{jsonl, "which services report errors and what kinds of errors appear"})
	} else {
		fmt.Fprintf(os.Stderr, "study-eval: jsonl fixture skipped: %v\n", err)
	}

	for _, cell := range studyEvalSweep {
		for _, c := range cases {
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
	fmt.Printf("%-26s %4s %6s %7s %6s   %s\n", "file", "k", "fill", "lat(s)", "cov%", "citations summed across reps")
	for _, cell := range studyEvalSweep {
		for _, c := range cases {
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
			kLabel, fillLabel := fmt.Sprintf("%d", cell.Chunks), "unit"
			if cell.Chunks == 0 {
				kLabel = "auto"
			}
			if cell.Fill > 0 {
				fillLabel = fmt.Sprintf("1/%.0f", 1/cell.Fill)
			}
			if read {
				fmt.Printf("%-26s %4s %6s %7.1f   read (fit, whole file)\n", name, kLabel, fillLabel, median(lat))
				continue
			}
			gp := 0.0
			if g+f > 0 {
				gp = 100 * float64(g) / float64(g+f)
			}
			// grounded% = real-grounding rate; unscored = citations the scorer
			// couldn't verify (claim had no extractable anchors).
			fmt.Printf("%-26s %4s %6s %7.1f %5.0f%%   grounded=%d failed=%d unscored=%d  (%.0f%% grounded)\n",
				name, kLabel, fillLabel, median(lat), median(cov), g, f, u, gp)
		}
	}
}

func shortName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// wmEvalRow is one multi-pass working-memory run (findings on or off).
type wmEvalRow struct {
	Path            string  `json:"path"`
	Goal            string  `json:"goal"`
	Model           string  `json:"model"`
	WorkingMemory   bool    `json:"working_memory"`
	Passes          int     `json:"passes"`
	Rep             int     `json:"rep"`
	Stopped         string  `json:"stopped"`
	LatencyMS       int64   `json:"latency_ms"`
	CoveragePct     float64 `json:"coverage_pct"`
	Grounded        int     `json:"grounded"`
	Failed          int     `json:"failed"`
	Unscored        int     `json:"unscored"`
	GroundednessPct float64 `json:"groundedness_pct"`
	Relays          int     `json:"finding_relays"`  // continuity: cites through to a prior finding
	Synthesis       int     `json:"synthesis_terms"` // continuity: terms carried from prior findings
	DigestChars     int     `json:"digest_chars"`
	Error           string  `json:"error,omitempty"`
}

// measureWM runs one multi-pass study with working memory on/off and scores it.
func measureWM(cs *CortexSession, c studyEvalCase, passes, rep int, wm bool) wmEvalRow {
	row := wmEvalRow{Path: c.Path, Goal: c.Goal, Model: cs.Study.Model, WorkingMemory: wm, Passes: passes, Rep: rep}
	start := time.Now()
	res, err := cs.runStudy(context.Background(), c.Path, c.Goal, passes, 0, 0, nil, 0, !wm)
	row.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Stopped = res.Stopped
	row.CoveragePct = 100 * res.CoveragePct
	row.Relays = res.FindingRelays
	row.Synthesis = res.SynthesisTerms
	row.DigestChars = len(strings.Join(res.Digests, ""))
	if data, derr := os.ReadFile(c.Path); derr == nil {
		row.Grounded, row.Failed, row.Unscored = scoreGroundedness(string(data), langForPath(c.Path), res)
		if g := row.Grounded + row.Failed; g > 0 {
			row.GroundednessPct = 100 * float64(row.Grounded) / float64(g)
		}
	}
	return row
}

// runStudyEvalWM is the P1 working-memory eval: a multi-pass study run with the
// findings prefix OFF (today's independent passes — the baseline) vs ON, over
// the same fixture and budget. It reads out the two P1 claims:
//
//	continuity         — finding-relays (citations that cite THROUGH to a prior
//	                     pass's evidence). 0 by construction with WM off; >0 with
//	                     WM on means later passes built on earlier ones.
//	coverage at equal  — coverage% and groundedness% on vs off. The findings
//	  budget             prefix spends sample budget, so the question is whether
//	                     continuity costs net coverage/grounding.
//
// Passes/reps are overridable via CORTEX_WM_PASSES / CORTEX_WM_REPS to bound
// runtime on slow local models. Run with: `loop study-eval wm`.
func runStudyEvalWM() {
	session := NewCortexSession()
	jsonl, err := ensureStudyEvalJSONL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "study-eval wm: jsonl fixture: %v\n", err)
		return
	}
	c := studyEvalCase{jsonl, "which services report errors and what kinds of errors appear"}

	passes := envInt("CORTEX_WM_PASSES", 3)
	reps := envInt("CORTEX_WM_REPS", 2)

	type agg struct {
		cov, gp        []float64
		relays, synth  int
		g, f           int
		u, errs, dchar int
	}
	sums := map[bool]*agg{false: {}, true: {}}

	for _, wm := range []bool{false, true} {
		for rep := 0; rep < reps; rep++ {
			row := measureWM(session, c, passes, rep, wm)
			b, _ := json.Marshal(row)
			fmt.Println(string(b)) // JSONL — one row per (wm, rep)
			a := sums[wm]
			if row.Error != "" {
				a.errs++
				continue
			}
			a.cov = append(a.cov, row.CoveragePct)
			if row.Grounded+row.Failed > 0 {
				a.gp = append(a.gp, row.GroundednessPct)
			}
			a.relays += row.Relays
			a.synth += row.Synthesis
			a.g, a.f, a.u = a.g+row.Grounded, a.f+row.Failed, a.u+row.Unscored
			a.dchar += row.DigestChars
		}
	}

	fmt.Printf("\n--- study-eval working memory (model: %s, %d passes, n=%d/cell, %s) ---\n",
		session.Study.Model, passes, reps, shortName(c.Path))
	fmt.Printf("%-8s %6s %7s %7s %7s %9s %5s\n", "findings", "cov%", "ground%", "relays", "synth", "digestKB", "errs")
	for _, wm := range []bool{false, true} {
		a := sums[wm]
		label := "off"
		if wm {
			label = "on"
		}
		fmt.Printf("%-8s %5.0f%% %6.0f%% %7d %7d %8.1fK %5d\n",
			label, median(a.cov), median(a.gp), a.relays, a.synth, float64(a.dchar)/1024, a.errs)
	}
	fmt.Println("\nP1 reads: synth>0 (on) = cross-pass continuity; cov%/ground% on≈off = ~free.")
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

// runStudyEvalCodeGrid is the 2×2 isolation experiment on the code fixture:
// {coarse, unit} × {numbered, unnumbered}, n=10/cell. It separates three
// candidate explanations for noisy code groundedness — fragment coherence,
// coordinate availability, and plain model variance. Run with:
// `loop study-eval code-grid`.
func runStudyEvalCodeGrid() {
	session := NewCortexSession()
	c := studyEvalCase{"cmd/cortex/commands/repl.go", "how are slash commands dispatched"}
	on, off := true, false
	cells := []studyEvalCell{
		{Chunks: 8, Fill: 1.0 / 8, Numbered: &off},
		{Chunks: 8, Fill: 1.0 / 8, Numbered: &on},
		{Chunks: 0, Fill: 0, Numbered: &off},
		{Chunks: 0, Fill: 0, Numbered: &on},
	}
	const reps = 10

	var rows []studyEvalRow
	for _, cell := range cells {
		for rep := 0; rep < reps; rep++ {
			row := measureCell(session, c, cell)
			row.Rep = rep
			rows = append(rows, row)
			b, _ := json.Marshal(row)
			fmt.Println(string(b))
		}
	}

	fmt.Printf("\n--- study-eval code 2x2 (model: %s, n=%d/cell, %s) ---\n", session.Study.Model, reps, shortName(c.Path))
	fmt.Printf("%4s %9s %7s   %s\n", "k", "numbered", "lat(s)", "citations summed across reps")
	for _, cell := range cells {
		var lat []float64
		var g, f, u, errs int
		for _, r := range rows {
			if r.Chunks != cell.Chunks || r.Numbered == nil || cell.Numbered == nil || *r.Numbered != *cell.Numbered {
				continue
			}
			if r.Error != "" {
				errs++
				continue
			}
			lat = append(lat, float64(r.LatencyMS)/1000)
			g, f, u = g+r.Grounded, f+r.Failed, u+r.Unscored
		}
		kLabel := fmt.Sprintf("%d", cell.Chunks)
		if cell.Chunks == 0 {
			kLabel = "auto"
		}
		gp := 0.0
		if g+f > 0 {
			gp = 100 * float64(g) / float64(g+f)
		}
		fmt.Printf("%4s %9v %7.1f   grounded=%d failed=%d unscored=%d errs=%d  (%.0f%% grounded)\n",
			kLabel, *cell.Numbered, median(lat), g, f, u, errs, gp)
	}
}
