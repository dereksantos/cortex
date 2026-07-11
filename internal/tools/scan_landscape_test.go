package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFixtureHome points homeDirFunc at a temp fixture tree for the duration
// of the test, restoring the real resolver afterward.
func withFixtureHome(t *testing.T, home string) {
	t.Helper()
	old := homeDirFunc
	homeDirFunc = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDirFunc = old })
}

func TestScanLandscapeIsRegisteredForCoderOnly(t *testing.T) {
	if !toolListContains(All, FunctionScanLandscape) {
		t.Fatal("scan_landscape missing from coder tool set")
	}
	if toolListContains(Study.Tools, FunctionScanLandscape) {
		t.Fatal("scan_landscape must not be available to the study subagent")
	}
}

func TestScanLandscapeReportsHarnessesAndRuntimesUnderHome(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ollama"), 0755); err != nil {
		t.Fatal(err)
	}
	withFixtureHome(t, home)

	got, err := scanLandscape()
	if err != nil {
		t.Fatalf("scanLandscape: %v", err)
	}
	for _, want := range []string{"claude", "ollama"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
}

func TestScanLandscapeReportsNoneWhenNothingDetected(t *testing.T) {
	withFixtureHome(t, t.TempDir())

	got, err := scanLandscape()
	if err != nil {
		t.Fatalf("scanLandscape: %v", err)
	}
	if !strings.Contains(got, "none detected") {
		t.Errorf("result should report nothing detected:\n%s", got)
	}
}

func TestScanLandscapeNeverWalksProjectsUnderHome(t *testing.T) {
	// A project-shaped tree (.git + AGENTS.md) sits directly under the fixture
	// home. scan_landscape is home-scoped to well-known harness/runtime paths
	// only — it must never walk arbitrary subtrees for projects (that would be
	// the blind-$HOME-sweep GOAL.md's D3 forbids); this proves the project
	// never shows up in its output.
	home := t.TempDir()
	proj := filepath.Join(home, "some-repo")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# agents"), 0644); err != nil {
		t.Fatal(err)
	}
	withFixtureHome(t, home)

	got, err := scanLandscape()
	if err != nil {
		t.Fatalf("scanLandscape: %v", err)
	}
	if strings.Contains(got, "some-repo") {
		t.Errorf("result should never mention a project path:\n%s", got)
	}
}
