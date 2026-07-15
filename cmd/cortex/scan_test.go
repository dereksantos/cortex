package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/landscape"
	"github.com/dereksantos/cortex/internal/userhome"
)

// TestResolveScanRootsFlagOverrides pins that an explicit --root wins
// outright, even when persisted scan.roots config also exists.
func TestResolveScanRootsFlagOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"scan":{"roots":["/persisted/root"]}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	roots, err := resolveScanRoots(path, "/explicit/root")
	if err != nil {
		t.Fatalf("resolveScanRoots: %v", err)
	}
	if len(roots) != 1 || roots[0] != "/explicit/root" {
		t.Errorf("resolveScanRoots = %v, want [/explicit/root]", roots)
	}
}

// TestResolveScanRootsUsesPersisted pins that with no --root flag, the
// persisted scan.roots config key (M1.7's PersistScanRoots shape) is
// used verbatim.
func TestResolveScanRootsUsesPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := PersistScanRoots(path, []string{"/home/derek/eng", "/home/derek/code"}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}

	roots, err := resolveScanRoots(path, "")
	if err != nil {
		t.Fatalf("resolveScanRoots: %v", err)
	}
	want := []string{"/home/derek/eng", "/home/derek/code"}
	if len(roots) != len(want) || roots[0] != want[0] || roots[1] != want[1] {
		t.Errorf("resolveScanRoots = %v, want %v", roots, want)
	}
}

// TestResolveScanRootsRefusesWhenNeither pins GOAL.md M2.5's third
// path: no --root and no persisted config ⇒ a typed refusal, never a
// blind $HOME sweep.
func TestResolveScanRootsRefusesWhenNeither(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json") // never written

	_, err := resolveScanRoots(path, "")
	if !errors.Is(err, ErrNoScanRoots) {
		t.Errorf("resolveScanRoots error = %v, want ErrNoScanRoots", err)
	}

	// Also refuse when the config file exists but carries no scan.roots
	// key at all (e.g. only a backend was ever persisted).
	if err := os.WriteFile(path, []byte(`{"backend":{"type":"ollama"}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err = resolveScanRoots(path, "")
	if !errors.Is(err, ErrNoScanRoots) {
		t.Errorf("resolveScanRoots error (no scan key) = %v, want ErrNoScanRoots", err)
	}
}

// TestResolveAndPersistScanRootsPersistsExplicitFlag pins the fix for the
// "scan ran, roots never persisted" gap: an explicit --root is the user
// telling cortex where their code lives, so `cortex scan --root <path>`
// must land scan.roots in user config exactly like answering the
// greeting does — and other, unrelated top-level config fields must
// survive the read-modify-write untouched.
func TestResolveAndPersistScanRootsPersistsExplicitFlag(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	path := userConfigPath()
	if err := os.WriteFile(path, []byte(`{"tools": {"allow_delete": false}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	roots, err := resolveAndPersistScanRoots(path, "/explicit/root")
	if err != nil {
		t.Fatalf("resolveAndPersistScanRoots: %v", err)
	}
	if len(roots) != 1 || roots[0] != "/explicit/root" {
		t.Errorf("roots = %v, want [/explicit/root]", roots)
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if !jsonEqual(t, doc["tools"], []byte(`{"allow_delete": false}`)) {
		t.Errorf("tools field mutated: %s", doc["tools"])
	}
	var scan struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(doc["scan"], &scan); err != nil {
		t.Fatalf("unmarshal scan: %v", err)
	}
	if len(scan.Roots) != 1 || scan.Roots[0] != "/explicit/root" {
		t.Errorf("scan.roots = %v, want [/explicit/root]", scan.Roots)
	}
}

// TestResolveAndPersistScanRootsNoFlagDoesNotRewrite pins that resolving
// from already-persisted config (no --root given) is read-only: nothing
// new gets written when there's nothing new to say.
func TestResolveAndPersistScanRootsNoFlagDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := PersistScanRoots(path, []string{"/persisted/root"}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	roots, err := resolveAndPersistScanRoots(path, "")
	if err != nil {
		t.Fatalf("resolveAndPersistScanRoots: %v", err)
	}
	if len(roots) != 1 || roots[0] != "/persisted/root" {
		t.Errorf("roots = %v, want [/persisted/root]", roots)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("config file rewritten with no --root flag:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestBuildScanReportAggregatesAcrossRoots plants a harness/runtime
// fixture at homeDir and two separate project roots, and asserts
// buildScanReport aggregates project findings from BOTH roots while
// probing harnesses/runtimes against homeDir only once.
func TestBuildScanReportAggregatesAcrossRoots(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("plant harness fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(home, ".ollama"), 0o755); err != nil {
		t.Fatalf("plant runtime fixture: %v", err)
	}

	rootA := t.TempDir()
	projA := filepath.Join(rootA, "proj-a")
	mustMkdirAllScan(t, filepath.Join(projA, ".git"))
	mustWriteFileScan(t, filepath.Join(projA, "AGENTS.md"), "sentinel-a")

	rootB := t.TempDir()
	projB := filepath.Join(rootB, "proj-b")
	mustMkdirAllScan(t, filepath.Join(projB, ".git"))
	mustWriteFileScan(t, filepath.Join(projB, "CLAUDE.md"), "sentinel-b")

	report, err := buildScanReport(home, []string{rootA, rootB}, landscape.Caps{})
	if err != nil {
		t.Fatalf("buildScanReport: %v", err)
	}

	if len(report.Tools) != 1 || report.Tools[0].Name != "claude" {
		t.Errorf("report.Tools = %+v, want a single claude entry", report.Tools)
	}
	if len(report.Runtimes) != 1 || report.Runtimes[0].Name != "ollama" {
		t.Errorf("report.Runtimes = %+v, want a single ollama entry", report.Runtimes)
	}
	if len(report.Projects) != 2 {
		t.Fatalf("report.Projects = %+v, want 2 entries (one per root)", report.Projects)
	}
	foundA, foundB := false, false
	for _, p := range report.Projects {
		if p.Path == projA {
			foundA = true
		}
		if p.Path == projB {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("report.Projects = %+v, want entries for both %s and %s", report.Projects, projA, projB)
	}
	if report.Truncated {
		t.Errorf("report.Truncated = true, want false (no caps set)")
	}
}

// TestRenderScanReportGolden pins the exact text layout `cortex scan`
// prints by default. Any intentional layout change updates this
// literal alongside a Decisions Log entry — same convention as
// greetingPrompt's golden pin.
func TestRenderScanReportGolden(t *testing.T) {
	report := ScanReport{
		Roots: []string{"/home/derek/eng"},
		Tools: []landscape.Tool{
			{Name: "claude", Path: "/home/derek/.claude"},
		},
		Runtimes: []landscape.Runtime{
			{Name: "ollama", Path: "/home/derek/.ollama"},
		},
		Projects: []landscape.Project{
			{Path: "/home/derek/eng/cortex", Markers: []string{"AGENTS.md", "CLAUDE.md"}},
		},
		Truncated: false,
	}

	want := `Cortex landscape scan
Roots: /home/derek/eng

Harnesses & editors (1):
  - claude (/home/derek/.claude)

Local model runtimes (1):
  - ollama (/home/derek/.ollama)

Projects (1):
  - /home/derek/eng/cortex [AGENTS.md, CLAUDE.md]
`
	got := renderScanReport(report)
	if got != want {
		t.Errorf("renderScanReport() =\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderScanReportGoldenEmptyAndTruncated pins the "none found"
// and truncation-notice branches, which the happy-path golden above
// never exercises.
func TestRenderScanReportGoldenEmptyAndTruncated(t *testing.T) {
	report := ScanReport{Roots: []string{"/home/derek/eng"}, Truncated: true}

	want := `Cortex landscape scan
Roots: /home/derek/eng

Harnesses & editors (0):
  (none found)

Local model runtimes (0):
  (none found)

Projects (0):
  (none found)

(scan truncated by caps — results may be incomplete)
`
	got := renderScanReport(report)
	if got != want {
		t.Errorf("renderScanReport() =\n%s\nwant:\n%s", got, want)
	}
}

// TestScanReportJSONRoundTrip pins --json: a ScanReport marshals and
// unmarshals to an equivalent value. It also closes out M2.3's
// deferred --json content-non-leak leg (STATE.md Decisions Log) by
// planting a sentinel in a project marker file's BODY and asserting it
// never appears in the JSON bytes — buildScanReport's landscape.Project
// only ever carries marker filenames, never contents, so this is a
// second proof of the same struct-level guarantee through the new
// surface M2.5 adds.
func TestScanReportJSONRoundTrip(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	mustMkdirAllScan(t, filepath.Join(proj, ".git"))
	const sentinel = "TOP-SECRET-CONTENT-SENTINEL-19f2"
	mustWriteFileScan(t, filepath.Join(proj, "AGENTS.md"), sentinel)

	report, err := buildScanReport(home, []string{root}, landscape.Caps{})
	if err != nil {
		t.Fatalf("buildScanReport: %v", err)
	}

	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	if strings.Contains(string(b), sentinel) {
		t.Errorf("ScanReport JSON leaks file body content: %s", b)
	}

	var round ScanReport
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(round.Projects) != 1 || round.Projects[0].Path != proj {
		t.Errorf("round-tripped Projects = %+v, want one entry for %s", round.Projects, proj)
	}
	if len(round.Projects[0].Markers) != 1 || round.Projects[0].Markers[0] != "AGENTS.md" {
		t.Errorf("round-tripped Markers = %v, want [AGENTS.md]", round.Projects[0].Markers)
	}
}

// TestRecordLandscapeScanWritesJournalEvent pins M2.7's `cortex scan` leg:
// the exact write recordLandscapeScan performs (the call runScanCLI makes
// right after buildScanReport) lands a landscape.scan event in the
// user-level journal, resolved through internal/userhome — not any
// project's .cortex/journal.
func TestRecordLandscapeScanWritesJournalEvent(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	report := ScanReport{
		Roots:     []string{"/home/derek/eng"},
		Tools:     []landscape.Tool{{Name: "claude", Path: "/home/derek/.claude"}},
		Runtimes:  []landscape.Runtime{{Name: "ollama", Path: "/home/derek/.ollama"}},
		Projects:  []landscape.Project{{Path: "/home/derek/eng/cortex", Markers: []string{"AGENTS.md"}}},
		Truncated: false,
	}
	if err := recordLandscapeScan(report); err != nil {
		t.Fatalf("recordLandscapeScan: %v", err)
	}

	dir, err := userhome.Path("journal", "landscape")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	r, err := journal.NewReader(dir)
	if err != nil {
		t.Fatalf("journal.NewReader: %v", err)
	}
	defer r.Close()

	e, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	got, err := journal.ParseLandscapeScan(e)
	if err != nil {
		t.Fatalf("ParseLandscapeScan: %v", err)
	}
	if got.ToolCount != 1 || got.RuntimeCount != 1 || got.ProjectCount != 1 || got.Truncated {
		t.Errorf("payload = %+v, want counts 1/1/1, truncated=false", *got)
	}
	if len(got.Roots) != 1 || got.Roots[0] != "/home/derek/eng" {
		t.Errorf("payload.Roots = %v, want [/home/derek/eng]", got.Roots)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("second Next() error = %v, want io.EOF (exactly one entry)", err)
	}
}

// TestScanCLISourceNeverImportsMemory is a mechanical meta-test (same
// idiom as pkg/secret's TestNoRealSecurityBinaryInvokedByTests, M1.6):
// headless `cortex scan` must write NO memory note (GOAL.md M2.7) — only
// the coder-only scan_landscape tool does. Asserted by construction: the
// CLI's own source file never imports internal/memory, so there is no
// code path by which it could write one.
func TestScanCLISourceNeverImportsMemory(t *testing.T) {
	src, err := os.ReadFile("scan.go")
	if err != nil {
		t.Fatalf("ReadFile scan.go: %v", err)
	}
	if strings.Contains(string(src), `"github.com/dereksantos/cortex/internal/memory"`) {
		t.Error("scan.go imports internal/memory — headless cortex scan must never write a memory note (GOAL.md M2.7)")
	}
}

func mustMkdirAllScan(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("failed to plant fixture dir %s: %v", path, err)
	}
}

func mustWriteFileScan(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to plant fixture file %s: %v", path, err)
	}
}
