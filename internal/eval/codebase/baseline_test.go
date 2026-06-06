package codebase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleRow(id string, pass bool, hop int, citationRate float64) BaselineRow {
	return BaselineRow{
		FixtureID:    id,
		Group:        "Q",
		Eval:         "Q1",
		Project:      "cortex",
		Language:     "go",
		Timestamp:    "2026-05-26T03:00:00Z",
		GitCommitSHA: "abc123",
		Model:        "coder",
		WallTimeMs:   42,
		Metrics: Metrics{
			HopCount:     hop,
			CitationRate: citationRate,
			ClaimCount:   4,
			BudgetTokens: 5000,
		},
		Pass: pass,
	}
}

func TestWriteAndLoadBaseline(t *testing.T) {
	wd := t.TempDir()
	rows := []BaselineRow{
		sampleRow("r1-cortex", true, 1, 0.25),
		sampleRow("q1-cortex", true, 1, 0.50),
		sampleRow("q3-cortex", false, 4, 0.30),
	}
	path, err := WriteBaseline(wd, "deadbeef", rows)
	if err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(wd, ".cortex", "db", "eval_baselines", "deadbeef")) {
		t.Errorf("path = %s; want under eval_baselines/deadbeef/", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}

	loaded, lp, err := LoadBaseline(wd, "deadbeef")
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if lp != path {
		t.Errorf("loaded from %s, want %s", lp, path)
	}
	if len(loaded) != 3 {
		t.Errorf("loaded %d rows, want 3", len(loaded))
	}
	if loaded[2].FixtureID != "q3-cortex" || loaded[2].Pass {
		t.Errorf("q3 row unexpected: %+v", loaded[2])
	}
}

func TestLoadBaselineLatest(t *testing.T) {
	wd := t.TempDir()
	if _, err := WriteBaseline(wd, "older", []BaselineRow{sampleRow("a", true, 1, 0.5)}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteBaseline(wd, "newer", []BaselineRow{sampleRow("b", false, 2, 0.3)}); err != nil {
		t.Fatal(err)
	}
	loaded, path, err := LoadBaseline(wd, "latest")
	if err != nil {
		t.Fatalf("LoadBaseline latest: %v", err)
	}
	if !strings.Contains(path, "newer") {
		t.Errorf("latest path = %s; expected from newer commit dir", path)
	}
	if len(loaded) != 1 || loaded[0].FixtureID != "b" {
		t.Errorf("loaded latest = %+v", loaded)
	}
}

func TestLoadBaselineEmpty(t *testing.T) {
	wd := t.TempDir()
	rows, path, err := LoadBaseline(wd, "latest")
	if err != nil {
		t.Fatalf("LoadBaseline on empty workdir: %v", err)
	}
	if len(rows) != 0 || path != "" {
		t.Errorf("empty load = %v %s; want nil + empty path", rows, path)
	}
}

func TestCompareRegressionImprovement(t *testing.T) {
	prev := []BaselineRow{
		sampleRow("r1", true, 1, 0.25),
		sampleRow("q3", true, 4, 0.65),
		sampleRow("dropped", true, 1, 0.5),
	}
	curr := []BaselineRow{
		sampleRow("r1", true, 1, 0.25),
		sampleRow("q3", false, 6, 0.40), // regressed: pass→fail, hop +50%
		sampleRow("new-fixture", true, 2, 0.7),
	}
	diffs := Compare(prev, curr)
	if len(diffs) != 4 {
		t.Fatalf("got %d diffs, want 4 (r1, q3, dropped, new-fixture)", len(diffs))
	}
	byID := map[string]Diff{}
	for _, d := range diffs {
		byID[d.FixtureID] = d
	}
	if !byID["q3"].Regressed {
		t.Errorf("q3 should be marked Regressed")
	}
	if byID["r1"].Regressed || byID["r1"].Improved {
		t.Errorf("r1 unchanged; should be neither regressed nor improved")
	}
	if byID["dropped"].Curr != nil {
		t.Errorf("dropped fixture should have nil Curr")
	}
	if byID["new-fixture"].Prev != nil {
		t.Errorf("new fixture should have nil Prev")
	}

	// q3 metrics should surface big changes
	if len(byID["q3"].BigChanges) == 0 {
		t.Errorf("q3 BigChanges empty; expected hop_count and citation_rate to flag")
	}
}

func TestFormatCompareReportSmoke(t *testing.T) {
	prev := []BaselineRow{sampleRow("r1", true, 1, 0.25)}
	curr := []BaselineRow{sampleRow("r1", false, 2, 0.10)}
	out := FormatCompareReport(Compare(prev, curr))
	if !strings.Contains(out, "REGRESSED") {
		t.Errorf("expected REGRESSED in report:\n%s", out)
	}
	if !strings.Contains(out, "r1") {
		t.Errorf("expected fixture id in report")
	}
}

func TestSummarize(t *testing.T) {
	rows := []BaselineRow{
		sampleRow("a", true, 1, 0.2),
		sampleRow("b", true, 2, 0.4),
		sampleRow("c", false, 3, 0.6),
	}
	agg := Summarize(rows)
	if agg.Total != 3 || agg.Passing != 2 {
		t.Errorf("agg = %+v; want total=3 passing=2", agg)
	}
	if agg.CitationP50 != 0.4 {
		t.Errorf("p50 citation = %v, want 0.4", agg.CitationP50)
	}
}

func TestSummarizeExcludesInvalid(t *testing.T) {
	rows := []BaselineRow{
		sampleRow("a", true, 1, 0.2),
		sampleRow("b", false, 2, 0.4),
		sampleRow("c", false, 3, 0.6),
		sampleRow("d", false, 1, 0.1),
	}
	// c and d were harness failures, not quality fails.
	rows[2].Invalid = true
	rows[3].Invalid = true

	agg := Summarize(rows)
	if agg.Total != 4 {
		t.Errorf("Total = %d, want 4", agg.Total)
	}
	if agg.Invalid != 2 {
		t.Errorf("Invalid = %d, want 2", agg.Invalid)
	}
	// Passing counts only scoreable cells: a passes, b fails → 1.
	if agg.Passing != 1 {
		t.Errorf("Passing = %d, want 1 (invalid cells excluded, not counted as fail)", agg.Passing)
	}
	if agg.Scoreable() != 2 {
		t.Errorf("Scoreable = %d, want 2 (4 total - 2 invalid)", agg.Scoreable())
	}
	// 2/4 invalid = 50% > 15% threshold → compromised.
	if !agg.Compromised() {
		t.Error("Compromised = false, want true (50% invalid)")
	}
}

func TestCompareInvalidIsNotRegression(t *testing.T) {
	prev := []BaselineRow{sampleRow("a", true, 1, 0.2)}  // passed before
	curr := []BaselineRow{sampleRow("a", false, 1, 0.2)} // "failed" now…
	curr[0].Invalid = true                               // …but it was a harness failure

	diffs := Compare(prev, curr)
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	if diffs[0].Regressed {
		t.Error("Regressed = true, want false (pass→invalid is a harness failure, not a model regression)")
	}
	if !diffs[0].Invalid {
		t.Error("Invalid = false, want true")
	}
}
