// Package eval — library-service multi-session eval (skeleton).
//
// See test/evals/library-service/SPEC.md for the design.
//
// The Run pipeline is still a stub (Plans 02–04). Score is implemented per
// Plan 01 — handler/test files are discovered in the workdir and scored
// against the rubric in test/evals/library-service/rubric.md.
package eval

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// Cleanup removes the run's workdir. The runner deliberately leaves the
// workdir intact on success so callers can re-Score it; this is the helper
// they call once they're done.
func (r *LibraryServiceRun) Cleanup() error {
	if r == nil || r.WorkDir == "" {
		return nil
	}
	return os.RemoveAll(r.WorkDir)
}

// Harness drives a single session: invoke the model with prompt against
// workdir, expecting the model to edit files in workdir directly.
//
// Implementations:
//   - ClaudeCLIHarness        — Plan 02 (this file's runner)
//   - CortexInjectingHarness  — Plan 03 will wrap a base harness to prepend
//     Cortex-mined patterns before calling through.
//
// Hard errors (binary missing, model unreachable, ctx cancellation) MUST
// be returned. Soft outcomes (build/test broken after the session) are the
// runner's concern and are recorded in SessionResult, not returned here.
type Harness interface {
	RunSession(ctx context.Context, prompt string, workdir string) error
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
	ShapeSimilarity  float64 // 0..1, higher is better. Headline metric.
	NamingAdherence  float64 // 0..1, S2-S5 vs S1
	SmellDensity     float64 // weighted smells per 100 LOC, lower is better
	TestParity       float64 // 0..1
	EndToEndPassRate float64 // 0..1, fraction of 25 endpoints returning expected status
	RefactorDeltaPct float64 // optional; -1 if not measured
}

// LibraryServiceEvaluator drives the full eval across conditions.
type LibraryServiceEvaluator struct {
	specDir     string // test/evals/library-service
	seedProject string // test/evals/projects/library-service-seed
	verbose     bool
}

// NewLibraryServiceEvaluator constructs an evaluator pointing at the eval files on disk.
func NewLibraryServiceEvaluator(specDir, seedProject string) *LibraryServiceEvaluator {
	return &LibraryServiceEvaluator{
		specDir:     specDir,
		seedProject: seedProject,
	}
}

// SetVerbose toggles per-session start/end log lines.
func (e *LibraryServiceEvaluator) SetVerbose(v bool) {
	e.verbose = v
}

// Run executes all sessions for the given condition and returns the run with score.
//
// Plan 02 (this implementation): only ConditionBaseline is wired. The default
// Claude CLI harness is constructed from PATH; the runner copies the seed,
// `git init`s the workdir, drives sessions 01–05 sequentially via the harness,
// records files-changed / build / test outcomes per session, and finally
// calls Score against the workdir.
//
// Plan 03 territory (NOT handled here): ConditionCortex / ConditionFrontier
// — those plug a different Harness in (e.g. CortexInjectingHarness wrapping
// the baseline). Callers writing tests or experimenting with custom harnesses
// should use RunWithHarness directly. See plans/02-session-runner.md and
// plans/03-cortex-injection.md for the split.
func (e *LibraryServiceEvaluator) Run(ctx context.Context, cond LibraryServiceCondition, model string) (*LibraryServiceRun, error) {
	if cond != ConditionBaseline {
		return nil, fmt.Errorf("condition %q not implemented yet (Plan 03 will add Cortex/Frontier harnesses)", cond)
	}
	h, err := NewClaudeCLIHarness("", model)
	if err != nil {
		return nil, fmt.Errorf("init claude harness: %w", err)
	}
	return e.RunWithHarness(ctx, cond, model, h)
}

// RunWithHarness drives the session loop using the provided harness. It is
// the seam Plan 03 hooks into to swap in a Cortex-injecting harness without
// touching the runner.
func (e *LibraryServiceEvaluator) RunWithHarness(ctx context.Context, cond LibraryServiceCondition, model string, h Harness) (*LibraryServiceRun, error) {
	return e.runSessions(ctx, cond, model, h)
}

// Score computes LibraryServiceScore for a completed workdir per rubric.md.
//
// Implements the 5 MVP rubric metrics defined in plans/01-scorer.md and
// plans/04-integration-test.md:
//   - ShapeSimilarity  (headline) — pairwise cosine over AST-derived feature vectors
//   - NamingAdherence  — S1's identifier templates vs S2–S5
//   - SmellDensity     — weighted cyclomatic / length / nesting / magic-literal smells
//   - TestParity       — setup/table-driven/assertion-idiom match against S1's tests
//   - EndToEndPassRate — fraction of 25 endpoints returning expected status class
//     against the built cmd/server. An e2e error never aborts the rubric;
//     the metric records 0 and other metrics remain valid.
//
// RefactorDeltaPct stays at -1 (optional rubric metric, deferred).
func (e *LibraryServiceEvaluator) Score(ctx context.Context, workDir string) (LibraryServiceScore, error) {
	score := LibraryServiceScore{RefactorDeltaPct: -1}

	discovered, err := discoverResourceFiles(workDir)
	if err != nil {
		return score, fmt.Errorf("discover files: %w", err)
	}

	// S1 by convention is the first resource (books) — it establishes the patterns
	// that subsequent sessions are expected to follow.
	s1Resource := LibraryResources[0]

	var (
		handlers       []string // one handler file per resource, in LibraryResources order
		allGoFiles     []string // every handler + test file we found, for smell density
		s1Handler      string
		s1TestFile     string
		otherHandlers  []string
		otherTestFiles []string
	)

	for _, r := range LibraryResources {
		rf := discovered[r]
		if rf == nil {
			continue
		}
		allGoFiles = append(allGoFiles, rf.Handlers...)
		allGoFiles = append(allGoFiles, rf.Tests...)
		if len(rf.Handlers) == 0 {
			continue
		}
		// Pick the first handler/test file. With multiple matches per resource
		// (e.g., a storage layer file also containing the resource name), this
		// is a best-effort heuristic. Refine once real session output exists.
		picked := rf.Handlers[0]
		handlers = append(handlers, picked)
		if r == s1Resource {
			s1Handler = picked
			if len(rf.Tests) > 0 {
				s1TestFile = rf.Tests[0]
			}
			continue
		}
		otherHandlers = append(otherHandlers, picked)
		if len(rf.Tests) > 0 {
			otherTestFiles = append(otherTestFiles, rf.Tests[0])
		}
	}

	if len(handlers) >= 2 {
		s, err := shapeSimilarity(handlers)
		if err != nil {
			return score, fmt.Errorf("shape similarity: %w", err)
		}
		score.ShapeSimilarity = s
	}
	if s1Handler != "" && len(otherHandlers) > 0 {
		s, err := namingAdherence(s1Handler, otherHandlers)
		if err != nil {
			return score, fmt.Errorf("naming adherence: %w", err)
		}
		score.NamingAdherence = s
	}
	if len(allGoFiles) > 0 {
		s, err := smellDensity(allGoFiles)
		if err != nil {
			return score, fmt.Errorf("smell density: %w", err)
		}
		score.SmellDensity = s
	}
	if s1TestFile != "" && len(otherTestFiles) > 0 {
		s, err := testParity(s1TestFile, otherTestFiles)
		if err != nil {
			return score, fmt.Errorf("test parity: %w", err)
		}
		score.TestParity = s
	}

	// e2e is best-effort: a build/start failure must not poison the rubric.
	// Other metrics already reflect cohesion regardless of whether the binary
	// happens to run.
	if rate, err := endToEndPassRate(workDir); err == nil {
		score.EndToEndPassRate = rate
	}

	return score, nil
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

// resourceFiles groups the handler and test files associated with one resource.
type resourceFiles struct {
	Handlers []string
	Tests    []string
}

// discoverResourceFiles walks workDir and groups .go files by which library
// resource they belong to. Membership is determined by basename or parent
// directory name containing the resource word (singular or plural).
//
// Heuristic, not authoritative. Real session output may locate files in
// unexpected layouts (e.g., a storage file named books.go in addition to a
// handler books.go). The first file matched per resource wins downstream.
func discoverResourceFiles(workDir string) (map[string]*resourceFiles, error) {
	out := make(map[string]*resourceFiles, len(LibraryResources))
	for _, r := range LibraryResources {
		out[r] = &resourceFiles{}
	}

	skipDirs := map[string]bool{
		"vendor": true, "node_modules": true, ".git": true,
		"testdata": true, ".cortex": true, ".claude": true,
	}

	err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		base := strings.ToLower(filepath.Base(path))
		dir := strings.ToLower(filepath.Base(filepath.Dir(path)))
		isTest := strings.HasSuffix(base, "_test.go")
		for _, r := range LibraryResources {
			singular := singularize(r)
			if !containsWord(base, r) && !containsWord(base, singular) &&
				!containsWord(dir, r) && !containsWord(dir, singular) {
				continue
			}
			if isTest {
				out[r].Tests = append(out[r].Tests, path)
			} else {
				out[r].Handlers = append(out[r].Handlers, path)
			}
			return nil
		}
		return nil
	})
	return out, err
}

// containsWord is a small wrapper so the discovery heuristic can be tightened
// later without touching every call site (e.g., to require word boundaries).
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(haystack, needle)
}
