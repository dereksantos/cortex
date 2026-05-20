//go:build !windows

// Package eval — library-service multi-session eval (skeleton).
//
// See test/evals/library-service/SPEC.md for the design.
//
// Score is implemented per Plan 01 — handler/test files are discovered in
// the workdir and scored against the rubric in
// test/evals/library-service/rubric.md.
//
// The Strategy field on LibraryServiceRun mirrors the v2 ContextStrategy
// values from cellresult.go ("baseline", "cortex", "frontier"). It's a
// label on the run, not a dispatch switch: under cortex-only (audit D1),
// the cross-harness Injector machinery is gone and library-service runs
// through whatever harness the caller wires in. Migrating library-service
// to drive CortexHarness directly is a follow-up under audit E.
package eval

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LibraryServiceRun is a single end-to-end run of all sessions for one
// strategy (baseline / cortex / frontier). Strategy is a label only — the
// caller wires whatever harness they want; this struct just records what
// happened.
type LibraryServiceRun struct {
	Strategy   string // baseline | cortex | frontier (see ContextStrategy* in cellresult.go)
	Model      string // e.g., "qwen2.5-coder:1.5b"
	WorkDir    string // path to a fresh copy of library-service-seed
	SessionLog []SessionResult
	Score      LibraryServiceScore
}

// Cleanup removes the run's workdir. The runner deliberately leaves the
// workdir intact on success so callers can re-Score it; this is the
// helper they call once they're done.
func (r *LibraryServiceRun) Cleanup() error {
	if r == nil || r.WorkDir == "" {
		return nil
	}
	return os.RemoveAll(r.WorkDir)
}

// Harness drives a single session: invoke the model with prompt against
// workdir, expecting the model to edit files in workdir directly.
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

// Run executes all sessions through ClaudeCLIHarness and returns the run
// with score. `strategy` is one of the v2 ContextStrategy values
// (baseline / cortex / frontier) and is recorded on the run as a label.
//
// Under cortex-only (audit D1) the cross-harness Injector machinery is
// gone. Every strategy currently runs the same code path; the lift signal
// will come back once library-service migrates to drive CortexHarness
// directly (audit E).
func (e *LibraryServiceEvaluator) Run(ctx context.Context, strategy, model string) (*LibraryServiceRun, error) {
	h, err := NewClaudeCLIHarness("", model)
	if err != nil {
		return nil, fmt.Errorf("init claude harness: %w", err)
	}
	return e.RunWithHarness(ctx, strategy, model, h)
}

// RunWithHarness drives the session loop using the provided harness.
// Tests use this to inject a fake harness; production callers go through
// Run().
func (e *LibraryServiceEvaluator) RunWithHarness(ctx context.Context, strategy, model string, h Harness) (*LibraryServiceRun, error) {
	return e.runSessions(ctx, strategy, model, h)
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

// CompareRuns produces the headline comparison: did Cortex move shape
// similarity from baseline toward the frontier ceiling?
//
// Output is markdown — a side-by-side table of the five MVP score fields
// with a delta column (cortex − baseline) plus a one-line headline lift
// callout. SmellDensity is "lower is better" so its delta is negated for
// readability ("Δ better"); every other metric is "higher is better".
//
// frontier may be nil; it's an optional ceiling. baseline and cortex are
// required.
func CompareRuns(baseline, cortex *LibraryServiceRun, frontier *LibraryServiceRun) string {
	if baseline == nil || cortex == nil {
		return "compare-runs: baseline and cortex are required, got nil"
	}

	type metric struct {
		name        string
		base, cor   float64
		front       float64
		hasFrontier bool
		// betterDelta returns the user-facing "improvement" delta given
		// raw cortex/baseline values (handles "lower is better" inversion).
		betterDelta func(base, cor float64) float64
	}
	higherBetter := func(base, cor float64) float64 { return cor - base }
	lowerBetter := func(base, cor float64) float64 { return base - cor }

	mk := func(name string, b, c, f LibraryServiceScore, get func(LibraryServiceScore) float64, delta func(float64, float64) float64) metric {
		m := metric{name: name, base: get(b), cor: get(c), betterDelta: delta}
		if frontier != nil {
			m.hasFrontier = true
			m.front = get(f)
		}
		return m
	}

	var fScore LibraryServiceScore
	if frontier != nil {
		fScore = frontier.Score
	}
	metrics := []metric{
		mk("Shape similarity (headline)", baseline.Score, cortex.Score, fScore,
			func(s LibraryServiceScore) float64 { return s.ShapeSimilarity }, higherBetter),
		mk("Naming adherence", baseline.Score, cortex.Score, fScore,
			func(s LibraryServiceScore) float64 { return s.NamingAdherence }, higherBetter),
		mk("Smell density (lower better)", baseline.Score, cortex.Score, fScore,
			func(s LibraryServiceScore) float64 { return s.SmellDensity }, lowerBetter),
		mk("Test parity", baseline.Score, cortex.Score, fScore,
			func(s LibraryServiceScore) float64 { return s.TestParity }, higherBetter),
		mk("End-to-end pass rate", baseline.Score, cortex.Score, fScore,
			func(s LibraryServiceScore) float64 { return s.EndToEndPassRate }, higherBetter),
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Library-service eval comparison\n\n")
	fmt.Fprintf(&b, "- baseline: strategy=%s model=%s\n", baseline.Strategy, baseline.Model)
	fmt.Fprintf(&b, "- cortex:   strategy=%s model=%s\n", cortex.Strategy, cortex.Model)
	if frontier != nil {
		fmt.Fprintf(&b, "- frontier: strategy=%s model=%s\n", frontier.Strategy, frontier.Model)
	}
	b.WriteString("\n")

	if frontier != nil {
		b.WriteString("| Metric | Baseline | Cortex | Frontier | Δ (cortex − baseline) |\n")
		b.WriteString("|---|---:|---:|---:|---:|\n")
		for _, m := range metrics {
			fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %+.3f |\n",
				m.name, m.base, m.cor, m.front, m.betterDelta(m.base, m.cor))
		}
	} else {
		b.WriteString("| Metric | Baseline | Cortex | Δ (cortex − baseline) |\n")
		b.WriteString("|---|---:|---:|---:|\n")
		for _, m := range metrics {
			fmt.Fprintf(&b, "| %s | %.3f | %.3f | %+.3f |\n",
				m.name, m.base, m.cor, m.betterDelta(m.base, m.cor))
		}
	}

	// Headline lift: shape similarity (cortex − baseline). The eval is
	// "good" when this is positive and large; per SPEC.md, the goal is
	// to move from <0.6 baseline toward ≥0.85 cortex.
	headline := cortex.Score.ShapeSimilarity - baseline.Score.ShapeSimilarity
	verdict := "no movement"
	switch {
	case headline >= 0.10:
		verdict = "lift"
	case headline > 0.0:
		verdict = "marginal lift"
	case headline < 0.0:
		verdict = "regression"
	}
	fmt.Fprintf(&b, "\n**Headline shape-similarity lift:** %+.3f (%s)\n", headline, verdict)

	return b.String()
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
