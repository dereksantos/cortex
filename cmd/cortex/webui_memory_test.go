// webui_memory_test.go — Go-side coverage for buildMemoryViewModel
// (webui_memory.go, docs/cross-source-learning.md piece 4): tier tagging,
// order (project first, then user — matching MemorySearch's own render
// order), and the "Notes never null" empty-registry precedent every other
// view-model builder in this package follows. The HTTP-layer behavior
// (tier routing, shadowed names, delete, auth) is serve_memory_test.go's
// job; this file is the pure data-assembly layer underneath it.
package main

import (
	"encoding/json"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

func TestBuildMemoryViewModelGolden(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog": {Name: "blog", Root: root},
	}}

	seedProjectNote(t, root, "rss-fix", "fixed the RSS feed")
	seedUserNote(t, "linear-backoff", "use linear backoff everywhere")

	vm, err := buildMemoryViewModel(reg, "blog")
	if err != nil {
		t.Fatalf("buildMemoryViewModel: %v", err)
	}
	if vm.Project != "blog" {
		t.Errorf("Project = %q, want blog", vm.Project)
	}
	if len(vm.Notes) != 2 {
		t.Fatalf("len(Notes) = %d, want 2", len(vm.Notes))
	}
	if vm.Notes[0].Tier != scopeProject || vm.Notes[0].Name != "rss-fix" {
		t.Errorf("Notes[0] = %+v, want project-tier rss-fix first", vm.Notes[0])
	}
	if vm.Notes[1].Tier != scopeUser || vm.Notes[1].Name != "linear-backoff" {
		t.Errorf("Notes[1] = %+v, want user-tier linear-backoff second", vm.Notes[1])
	}
	if vm.Notes[0].Hook == "" || vm.Notes[1].Hook == "" {
		t.Error("every note row must carry a hook (one-line description)")
	}
	if vm.Notes[0].Updated == "" || vm.Notes[1].Updated == "" {
		t.Error("every note row must carry an updated timestamp")
	}
}

func TestBuildMemoryViewModelEmptyTiersReturnEmptyNotesNotNull(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog": {Name: "blog", Root: root},
	}}

	vm, err := buildMemoryViewModel(reg, "blog")
	if err != nil {
		t.Fatalf("buildMemoryViewModel: %v", err)
	}
	got, err := json.Marshal(vm.Notes)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != "[]" {
		t.Errorf("Notes JSON = %s, want [] (never null, matching every other view-model builder's precedent)", got)
	}
}

func TestBuildMemoryViewModelUnregisteredProjectErrors(t *testing.T) {
	reg := &fakeRegistry{}
	if _, err := buildMemoryViewModel(reg, "nope"); err == nil {
		t.Error("buildMemoryViewModel(unregistered project) = nil error, want registry.ErrProjectNotFound")
	}
}
