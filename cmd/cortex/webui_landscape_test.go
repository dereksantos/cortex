// webui_landscape_test.go — M5.2c: the landscape report view-model
// (GOAL.md §6 M5.2, split into M5.2a/b/c/d per STATE.md). Golden-pins the
// exact JSON shape buildLandscapeViewModel produces over a fixed
// scan-roots+home fixture, following the "golden-pinned = literal string in
// a test" convention established at M1.5/M2.5/M5.2a/M5.2b.
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildLandscapeViewModelGolden(t *testing.T) {
	homeDir := t.TempDir()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")

	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("# fixture, sentinel body never leaks"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := PersistScanRoots(configPath, []string{projectRoot}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}

	vm, err := buildLandscapeViewModel(configPath, homeDir)
	if err != nil {
		t.Fatalf("buildLandscapeViewModel: %v", err)
	}
	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"roots":["` + jsonEscape(projectRoot) + `"],"tools":null,"runtimes":null,` +
		`"projects":[{"Path":"` + jsonEscape(projectRoot) + `","Markers":["AGENTS.md"]}],"truncated":false}`
	if string(got) != want {
		t.Errorf("landscape view-model JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildLandscapeViewModelNoRootsConfiguredReturnsTypedRefusal(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json") // never written

	_, err := buildLandscapeViewModel(configPath, homeDir)
	if !errors.Is(err, ErrNoScanRoots) {
		t.Errorf("err = %v, want ErrNoScanRoots", err)
	}
}
