// Package codebase implements the codebase-reading eval suite
// (docs/eval-suite-codebase-reading.md): fixtures + mechanical metric
// extraction over dag_traces.jsonl and a captured answer text.
//
// Slice 1 lands the fixture loader, the metric extractor, and a thin
// runner; slices 2–5 author the rest of the fixture matrix, add an LLM
// judge for substantive correctness, and a baseline/regression
// dashboard.
package codebase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FixtureGroup is one of the three eval families in the suite.
type FixtureGroup string

const (
	GroupRead     FixtureGroup = "R" // Read mechanics
	GroupQuestion FixtureGroup = "Q" // Question shape
	GroupBehavior FixtureGroup = "B" // Budget / scope / honest-unknown behavior
)

// Fixture is the on-disk shape of one codebase-reading eval cell.
// Mirrors the schema documented in docs/eval-suite-codebase-reading.md.
//
// The `Project` field names a fixture project relative to a configured
// root (see ResolveFixturePath). The path is resolved at run time so
// fixtures stay portable across machines.
type Fixture struct {
	// Path is the absolute fixture file path; populated by Load.
	Path string `yaml:"-"`

	ID      string       `yaml:"id"`
	Group   FixtureGroup `yaml:"group"`
	Eval    string       `yaml:"eval"`    // R1, Q1, Q3, B2, …
	Project string       `yaml:"project"` // workdir name, e.g. "cortex"
	Prompt  string       `yaml:"prompt"`

	Expected Expectation `yaml:"expected"`

	// Language is the predominant language of the fixture project.
	// Used by slice-5 cross-cutting analysis (Go/JS-TS/Python/Rust/mixed).
	// Optional; missing means "not categorized".
	Language string `yaml:"language,omitempty"`

	// JudgeRubric is the slice-3 substantive-correctness rubric. When
	// non-empty, the runner invokes Judge() after mechanical extraction
	// and pass=false if the rubric judges the answer unsatisfactory.
	// Q-class fixtures use this to catch "mechanical pass, wrong
	// content" cases the regex extractors can't see.
	JudgeRubric string `yaml:"judge_rubric,omitempty"`
}

// Expectation enumerates the mechanical bounds slice-1 enforces.
// Every field is optional; a zero value (or nil slice) means "do not
// check this dimension". Slice-1 metrics that can't be computed (e.g.
// citation_rate on an empty answer) report 0 and a flag rather than
// silently passing.
type Expectation struct {
	HopCountMin int `yaml:"hop_count_min,omitempty"`
	HopCountMax int `yaml:"hop_count_max,omitempty"`

	ReadCountMin int `yaml:"read_count_min,omitempty"`
	ReadCountMax int `yaml:"read_count_max,omitempty"`

	// InspectCountMin / Max bound read_count + shell_count together. Use
	// this when the prompt could be satisfied by either act.read_file OR
	// a shell cat/grep — the model picks one tool; the fixture shouldn't
	// dictate which. R2/R3 fixtures rely on this.
	InspectCountMin int `yaml:"inspect_count_min,omitempty"`
	InspectCountMax int `yaml:"inspect_count_max,omitempty"`

	// CitationRateMin is the minimum fraction of "claims" (paragraph
	// or list-item) that must reference a file path. 0 disables the
	// check.
	CitationRateMin float64 `yaml:"citation_rate_min,omitempty"`

	// HedgeCountMax caps the number of hedge phrases ("not directly
	// confirmed", "appears to", "may be", …). 0 (the zero value)
	// disables the check; set to -1 to assert zero-hedges explicitly.
	HedgeCountMax int `yaml:"hedge_count_max,omitempty"`

	// UnverifiedTailCountMax caps items under a section literally
	// named "Unverified" / "Unverified Claims". -1 explicit zero, 0
	// disables. (Slice 1 implements as a count of bullets under
	// matching headings.)
	UnverifiedTailCountMax int `yaml:"unverified_tail_count_max,omitempty"`

	MustCitePaths  []string `yaml:"must_cite_paths,omitempty"`
	MustNotInvent  []string `yaml:"must_not_invent,omitempty"`
	BudgetTokenMin int      `yaml:"budget_token_min,omitempty"`
	BudgetTokenMax int      `yaml:"budget_token_max,omitempty"`
}

// Load parses one YAML fixture from path and validates it.
func Load(path string) (*Fixture, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("abs %s: %w", path, err)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	f := &Fixture{Path: abs}
	if err := yaml.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return f, nil
}

// LoadDir loads every *.yaml under dir (one level deep). Returns the
// fixtures sorted by ID for stable iteration. Errors from individual
// files are returned aggregated so a single typo doesn't mask the rest.
func LoadDir(dir string) ([]*Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var (
		fixtures []*Fixture
		errs     []string
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		f, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		fixtures = append(fixtures, f)
	}
	if len(errs) > 0 {
		return fixtures, fmt.Errorf("%d fixture load errors:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return fixtures, nil
}

func (f *Fixture) validate() error {
	if f.ID == "" {
		return fmt.Errorf("fixture %s: id is required", f.Path)
	}
	switch f.Group {
	case GroupRead, GroupQuestion, GroupBehavior:
	default:
		return fmt.Errorf("fixture %s: group must be R / Q / B (got %q)", f.ID, f.Group)
	}
	if f.Eval == "" {
		return fmt.Errorf("fixture %s: eval is required (e.g. R1, Q3, B2)", f.ID)
	}
	if f.Project == "" {
		return fmt.Errorf("fixture %s: project is required", f.ID)
	}
	if strings.TrimSpace(f.Prompt) == "" {
		return fmt.Errorf("fixture %s: prompt is required", f.ID)
	}
	if f.Expected.HopCountMin > 0 && f.Expected.HopCountMax > 0 &&
		f.Expected.HopCountMin > f.Expected.HopCountMax {
		return fmt.Errorf("fixture %s: hop_count_min(%d) > hop_count_max(%d)",
			f.ID, f.Expected.HopCountMin, f.Expected.HopCountMax)
	}
	if f.Expected.BudgetTokenMin > 0 && f.Expected.BudgetTokenMax > 0 &&
		f.Expected.BudgetTokenMin > f.Expected.BudgetTokenMax {
		return fmt.Errorf("fixture %s: budget_token_min(%d) > budget_token_max(%d)",
			f.ID, f.Expected.BudgetTokenMin, f.Expected.BudgetTokenMax)
	}
	return nil
}

// ResolveFixturePath returns the absolute workdir for a fixture's
// Project field. The lookup order:
//
//  1. <fixtureRoot>/<project>            (caller-provided override)
//  2. CORTEX_FIXTURE_<PROJECT> env var   (machine-local override per project)
//  3. <repoRoot>                         (project=="cortex" maps to repo root)
//  4. <repoRoot>/test/evals/fixtures/<project>   (slice-5 fixture projects)
//  5. <repoRoot>/../<project>            (sibling repos like ../leanjs)
//
// Returns the first path that exists. An empty fixtureRoot or repoRoot
// skips that branch. A relative path is returned unchanged when none of
// the candidates exists — runtime callers see the error at workdir use.
func ResolveFixturePath(fixtureRoot, repoRoot, project string) string {
	candidates := []string{}
	if fixtureRoot != "" {
		candidates = append(candidates, filepath.Join(fixtureRoot, project))
	}
	// Machine-local override: CORTEX_FIXTURE_LEANJS=/path/to/leanjs lets a
	// user point any fixture project at an arbitrary workdir without
	// editing fixtures. Project names with hyphens upper to underscore
	// (python-todo → PYTHON_TODO).
	envKey := "CORTEX_FIXTURE_" + envSlug(project)
	if v := os.Getenv(envKey); v != "" {
		candidates = append(candidates, v)
	}
	if repoRoot != "" {
		if project == "cortex" {
			candidates = append(candidates, repoRoot)
		}
		candidates = append(candidates, filepath.Join(repoRoot, "test", "evals", "fixtures", project))
		// Sibling-repo fallback: ../leanjs is the typical layout when
		// the user keeps both repos in one workspace.
		candidates = append(candidates, filepath.Join(repoRoot, "..", project))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return project
}

// envSlug uppercases and turns non-identifier chars into underscores so
// project names like "python-todo" become "PYTHON_TODO".
func envSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
