// webui_dashboard_test.go — M5.2a: the project dashboard view-model (GOAL.md
// §6 M5.2, split into M5.2a/b/c/d per STATE.md). Golden-pins the exact JSON
// shape buildDashboardViewModel produces over a fixed registry+session+git
// fixture, following the "golden-pinned = literal string in a test"
// convention established at M1.5/M2.5 (no on-disk golden-file mechanism
// exists in this repo).
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/registry"
)

// buildDashboardGoldenJSON returns the exact expected JSON for the fixture
// this test constructs, with the two temp-dir roots substituted in — the
// only non-deterministic piece of an otherwise fully pinned shape (fixed
// session mtime via os.Chtimes, fixed git branches, fixed dirty/clean
// state, alphabetical project order).
func buildDashboardGoldenJSON(blogRoot, cortexRoot string) string {
	return `{"projects":[` +
		`{"name":"blog","root":"` + jsonEscape(blogRoot) + `",` +
		`"branch":"main","active_change":false,"clean":true,` +
		`"sessions":[{"id":"20260101-000000","mod_time":"2026-01-01T00:00:00Z","messages":1,"first":"hello"}]},` +
		`{"name":"cortex","root":"` + jsonEscape(cortexRoot) + `",` +
		`"branch":"cortex/fix-login","active_change":true,"clean":false,` +
		`"sessions":[]}` +
		`]}`
}

// jsonEscape round-trips a string through json.Marshal to escape it exactly
// as encoding/json would inside a larger document (temp dir paths can
// contain characters like backslashes on some platforms).
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func TestBuildDashboardViewModelGolden(t *testing.T) {
	fixedMTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	blogRoot := initGitFixture(t, "main", false)
	sessDir := filepath.Join(blogRoot, ".cortex", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeTestSession(t, sessDir, "20260101-000000",
		`{"kind":"message","role":"user","content":"hello"}`,
	)
	sessFile := filepath.Join(sessDir, "20260101-000000.jsonl")
	if err := os.Chtimes(sessFile, fixedMTime, fixedMTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	cortexRoot := initGitFixture(t, "cortex/fix-login", true) // dirty change branch, no sessions dir at all

	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog":   {Name: "blog", Root: blogRoot},
		"cortex": {Name: "cortex", Root: cortexRoot},
	}}

	vm, err := buildDashboardViewModel(reg)
	if err != nil {
		t.Fatalf("buildDashboardViewModel: %v", err)
	}
	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := buildDashboardGoldenJSON(blogRoot, cortexRoot)
	if string(got) != want {
		t.Errorf("dashboard view-model JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildDashboardViewModelEmptyRegistryReturnsEmptyProjectsNotNull(t *testing.T) {
	reg := &fakeRegistry{}
	vm, err := buildDashboardViewModel(reg)
	if err != nil {
		t.Fatalf("buildDashboardViewModel: %v", err)
	}
	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"projects":[]}` {
		t.Errorf("got %s, want empty projects array", got)
	}
}

func TestBuildDashboardViewModelNonGitProjectSurfacesChangeError(t *testing.T) {
	root := t.TempDir() // never git-initialized
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"plain": {Name: "plain", Root: root},
	}}
	vm, err := buildDashboardViewModel(reg)
	if err != nil {
		t.Fatalf("buildDashboardViewModel: %v", err)
	}
	if len(vm.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(vm.Projects))
	}
	p := vm.Projects[0]
	if p.ChangeError == "" {
		t.Error("ChangeError = \"\", want a non-empty error for a non-git project root")
	}
	if p.Branch != "" || p.Clean || p.ActiveChange {
		t.Errorf("non-git project got branch=%q clean=%v active=%v, want all zero values", p.Branch, p.Clean, p.ActiveChange)
	}
	if p.Sessions == nil {
		t.Error("Sessions = nil, want an empty (non-nil) slice")
	}
}

// TestBuildDashboardViewModelMissingRootIsFlaggedNotRawError pins that a
// registered project whose root directory no longer exists on disk renders
// as Missing=true with everything else zeroed — not a raw
// "change status: git rev-parse ...: chdir ...: no such file or directory"
// ChangeError, which is what changeStatusFor would otherwise surface (the
// webui bug this guards: that raw string rendering straight into the card/
// row UI).
func TestBuildDashboardViewModelMissingRootIsFlaggedNotRawError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"gone": {Name: "gone", Root: root},
	}}
	vm, err := buildDashboardViewModel(reg)
	if err != nil {
		t.Fatalf("buildDashboardViewModel: %v", err)
	}
	if len(vm.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(vm.Projects))
	}
	p := vm.Projects[0]
	if !p.Missing {
		t.Error("Missing = false, want true for a nonexistent project root")
	}
	if p.ChangeError != "" {
		t.Errorf("ChangeError = %q, want empty — a missing root must skip git entirely, not surface its raw error", p.ChangeError)
	}
	if p.Branch != "" || p.Clean || p.ActiveChange {
		t.Errorf("missing-root project got branch=%q clean=%v active=%v, want all zero values", p.Branch, p.Clean, p.ActiveChange)
	}
	if p.Sessions == nil {
		t.Error("Sessions = nil, want an empty (non-nil) slice")
	}
	if len(p.Sessions) != 0 {
		t.Errorf("Sessions = %v, want empty — a missing root must skip session listing entirely", p.Sessions)
	}
}

// A root-less project (any Registry implementation could hand one back —
// the legacy daemon-era projects.json schema had no "root" field) must be
// skipped, never resolved: NewWorkspace("") lands on the process CWD, so a
// ghost entry would run git and read sessions against whatever directory
// hosts the serve process.
func TestBuildDashboardViewModelSkipsRootlessEntries(t *testing.T) {
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"ghost": {Name: "ghost", Root: ""},
	}}
	vm, err := buildDashboardViewModel(reg)
	if err != nil {
		t.Fatalf("buildDashboardViewModel: %v", err)
	}
	if len(vm.Projects) != 0 {
		t.Errorf("root-less entries must be skipped, got %+v", vm.Projects)
	}
}
