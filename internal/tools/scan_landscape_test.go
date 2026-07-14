package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/userhome"
)

// withFixtureHome points homeDirFunc at a temp fixture tree for the duration
// of the test, restoring the real resolver afterward.
func withFixtureHome(t *testing.T, home string) {
	t.Helper()
	old := homeDirFunc
	homeDirFunc = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDirFunc = old })
}

// withIsolatedUserHome redirects internal/userhome (and so the
// scan_landscape tool's best-effort landscape.scan journal write) at a temp
// dir, so tests never touch the real machine's ~/.cortex/journal.
func withIsolatedUserHome(t *testing.T) {
	t.Helper()
	t.Setenv("CORTEX_HOME", t.TempDir())
}

// fakeMemoryStore is a minimal MemoryStore test double. Only MemoryWrite is
// exercised by scanLandscape; the rest satisfy the interface without being
// meaningfully used here.
type fakeMemoryStore struct {
	notes     map[string]string
	writeErr  error
	writeCall int
}

func (f *fakeMemoryStore) MemoryWrite(name, content string) (string, error) {
	f.writeCall++
	if f.writeErr != nil {
		return "", f.writeErr
	}
	if f.notes == nil {
		f.notes = map[string]string{}
	}
	f.notes[name] = content
	return "saved note \"" + name + "\"", nil
}
func (f *fakeMemoryStore) MemoryRead(name string) (string, error) {
	if body, ok := f.notes[name]; ok {
		return body, nil
	}
	return "", errors.New("not found")
}
func (f *fakeMemoryStore) MemorySearch(string) (string, error) { return "", nil }
func (f *fakeMemoryStore) MemoryForget(name string) (string, error) {
	delete(f.notes, name)
	return "", nil
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
	withIsolatedUserHome(t)

	got, err := scanLandscape(&fakeMemoryStore{})
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
	withIsolatedUserHome(t)

	got, err := scanLandscape(&fakeMemoryStore{})
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
	withIsolatedUserHome(t)

	got, err := scanLandscape(&fakeMemoryStore{})
	if err != nil {
		t.Fatalf("scanLandscape: %v", err)
	}
	if strings.Contains(got, "some-repo") {
		t.Errorf("result should never mention a project path:\n%s", got)
	}
}

// TestScanLandscapeWritesLandscapeMemoryNote pins M2.7's memory-note leg:
// the coder tool writes a fixed-name "landscape" note to the CURRENT
// project's memory store (here, the fakeMemoryStore standing in for it),
// via the same MemoryWrite path memory_write itself uses, and the note body
// carries a fixture-derived string (proving it's the real scan result, not
// a placeholder).
func TestScanLandscapeWritesLandscapeMemoryNote(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	withFixtureHome(t, home)
	withIsolatedUserHome(t)

	deps := &fakeMemoryStore{}
	if _, err := scanLandscape(deps); err != nil {
		t.Fatalf("scanLandscape: %v", err)
	}

	note, ok := deps.notes[landscapeMemoryNoteName]
	if !ok {
		t.Fatalf("no %q note written; notes = %v", landscapeMemoryNoteName, deps.notes)
	}
	if !strings.Contains(note, "claude") {
		t.Errorf("landscape note missing fixture-derived %q:\n%s", "claude", note)
	}
	if deps.writeCall != 1 {
		t.Errorf("MemoryWrite called %d times, want exactly 1", deps.writeCall)
	}
}

// TestScanLandscapeMemoryWriteFailurePropagates: a failing MemoryWrite
// (e.g. no memory store available) surfaces as a tool error rather than
// being silently swallowed — matching the memory_write tool's own
// error-propagation contract (internal/tools/tools.go's memoryWrite).
func TestScanLandscapeMemoryWriteFailurePropagates(t *testing.T) {
	withFixtureHome(t, t.TempDir())
	withIsolatedUserHome(t)

	deps := &fakeMemoryStore{writeErr: errors.New("memory unavailable: no session")}
	if _, err := scanLandscape(deps); err == nil {
		t.Fatal("scanLandscape returned nil error despite a failing MemoryWrite")
	}
}

// TestScanLandscapeWritesLandscapeScanJournalEvent pins M2.7's journal leg
// for the coder-tool path: calling scanLandscape appends a landscape.scan
// event to the user-level journal (userhome-rooted), independent of
// whatever the current project's own .cortex/journal holds.
func TestScanLandscapeWritesLandscapeScanJournalEvent(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ollama"), 0755); err != nil {
		t.Fatal(err)
	}
	withFixtureHome(t, home)
	withIsolatedUserHome(t)

	if _, err := scanLandscape(&fakeMemoryStore{}); err != nil {
		t.Fatalf("scanLandscape: %v", err)
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
	if got.ToolCount != 1 || got.RuntimeCount != 1 {
		t.Errorf("payload = %+v, want ToolCount=1 RuntimeCount=1", *got)
	}
}
