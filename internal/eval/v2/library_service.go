// Package eval — library-service multi-session eval (skeleton).
//
// See test/evals/library-service/SPEC.md for the design.
//
// This file is a stub. Implementation TODOs are noted inline.
package eval

import (
	"context"
	"fmt"
)

// LibraryServiceCondition identifies a comparison condition (baseline, cortex, etc.).
type LibraryServiceCondition string

const (
	ConditionBaseline LibraryServiceCondition = "baseline"
	ConditionCortex   LibraryServiceCondition = "cortex"
	ConditionFrontier LibraryServiceCondition = "frontier"
)

// LibraryServiceRun is a single end-to-end run of all sessions for one condition.
type LibraryServiceRun struct {
	Condition  LibraryServiceCondition
	Model      string // e.g., "qwen2.5-coder:1.5b"
	WorkDir    string // path to a fresh copy of library-service-seed
	SessionLog []SessionResult
	Score      LibraryServiceScore
}

// SessionResult captures what happened in one session.
type SessionResult struct {
	SessionID    string // "01-scaffold-and-books", etc.
	DurationMs   int64
	FilesChanged []string
	BuildOK      bool
	TestsOK      bool
}

// LibraryServiceScore aggregates the rubric metrics defined in rubric.md.
type LibraryServiceScore struct {
	ShapeSimilarity   float64 // 0..1, higher is better. Headline metric.
	NamingAdherence   float64 // 0..1, S2-S5 vs S1
	SmellDensity      float64 // smells per 100 LOC, lower is better
	TestParity        float64 // 0..1
	EndToEndPassRate  float64 // 0..1, fraction of 25 endpoints returning expected status
	RefactorDeltaPct  float64 // optional; -1 if not measured
}

// LibraryServiceEvaluator drives the full eval across conditions.
type LibraryServiceEvaluator struct {
	specDir       string // test/evals/library-service
	seedProject   string // test/evals/projects/library-service-seed
	verbose       bool
}

// NewLibraryServiceEvaluator constructs an evaluator pointing at the eval files on disk.
func NewLibraryServiceEvaluator(specDir, seedProject string) *LibraryServiceEvaluator {
	return &LibraryServiceEvaluator{
		specDir:     specDir,
		seedProject: seedProject,
	}
}

// Run executes all sessions for the given condition and returns the run with score.
//
// TODO(impl):
//  1. Copy seedProject to a fresh tempdir
//  2. For each session 01..05:
//     a. Read sessions/NN-*.md
//     b. Invoke the configured model (with or without Cortex injection per condition)
//     c. Apply the model's edits to the workdir (relies on the harness driving an
//        Edit/Write-tool-capable agent — likely Claude CLI for now)
//     d. Run go build ./... and go test ./... in the workdir
//     e. Capture SessionResult
//  3. After S5 completes, score the final repo per rubric.md (see Score below)
func (e *LibraryServiceEvaluator) Run(ctx context.Context, cond LibraryServiceCondition, model string) (*LibraryServiceRun, error) {
	return nil, fmt.Errorf("not implemented: see SPEC.md and rubric.md for design")
}

// Score computes LibraryServiceScore for a completed workdir.
//
// TODO(impl):
//   - ShapeSimilarity: pairwise cosine over AST-derived feature vectors of the 5
//     handler files. Use go/ast + go/parser; build feature vector per file
//     (function shapes, error-call sites, response patterns, etc.).
//   - NamingAdherence: extract identifier patterns from the books resource files;
//     scan S2-S5 files for adherence percentage.
//   - SmellDensity: traverse all generated .go files, count cyclomatic complexity
//     per function, function length, nesting depth, magic numbers; normalize per
//     100 LOC.
//   - TestParity: detect test files per resource, compare setup/assertion shape
//     to books test file.
//   - EndToEndPassRate: spin up the built binary, hit all 25 endpoints, count
//     pass rate. Reuse internal/web or just net/http.
//   - RefactorDeltaPct: optional; uses a frontier model to rewrite for cohesion
//     and diffs against actual.
func (e *LibraryServiceEvaluator) Score(ctx context.Context, workDir string) (LibraryServiceScore, error) {
	return LibraryServiceScore{RefactorDeltaPct: -1}, fmt.Errorf("not implemented: see rubric.md")
}

// CompareRuns produces the headline comparison: did Cortex move shape similarity
// from baseline toward the frontier ceiling?
//
// TODO(impl): emit a markdown report per SPEC.md "Pass criteria".
func CompareRuns(baseline, cortex *LibraryServiceRun, frontier *LibraryServiceRun) string {
	_ = baseline
	_ = cortex
	_ = frontier
	return "not implemented"
}
