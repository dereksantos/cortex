package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/pkg/cliout"
)

// TestGenerateManifestDeterministic verifies that two back-to-back
// generator calls produce byte-identical output. Determinism is a hard
// requirement: the CI check (`cortex tools --check`) compares the
// committed file byte-for-byte against the generator's output.
func TestGenerateManifestDeterministic(t *testing.T) {
	a := GenerateManifest("test-1.0.0")
	b := GenerateManifest("test-1.0.0")

	ja, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	jb, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if string(ja) != string(jb) {
		t.Fatalf("generator output is non-deterministic\nfirst:\n%s\n\nsecond:\n%s", ja, jb)
	}
}

// TestGenerateManifestShape spot-checks invariants every entry must
// satisfy: name and description non-empty, version stamped, schema
// version current.
func TestGenerateManifestShape(t *testing.T) {
	m := GenerateManifest("0.1.0")
	if m.SchemaVersion != cliout.ManifestVersion {
		t.Errorf("SchemaVersion = %q, want %q", m.SchemaVersion, cliout.ManifestVersion)
	}
	if len(m.Tools) == 0 {
		t.Fatal("manifest has no tools")
	}
	for _, e := range m.Tools {
		if e.Name == "" {
			t.Errorf("entry with empty name: %+v", e)
		}
		if e.Description == "" {
			t.Errorf("%s: empty description", e.Name)
		}
		if e.Version == "" {
			t.Errorf("%s: empty version", e.Name)
		}
	}
}

// TestGenerateManifestFlagsSurface verifies that commands that
// implement FlagDescriber actually contribute flag specs. Catches a
// regression where the interface assertion silently breaks.
func TestGenerateManifestFlagsSurface(t *testing.T) {
	m := GenerateManifest("0.1.0")
	want := map[string][]string{
		"search":        {"json", "limit", "mode", "type", "workdir"},
		"embed":         {"bulk", "content-type", "doc-id", "store", "text", "workdir"},
		"search-vector": {"content-type", "text", "threshold", "top-k", "vector", "workdir"},
		"tools":         {"check", "out", "version"},
	}
	got := make(map[string][]string)
	for _, e := range m.Tools {
		if _, ok := want[e.Name]; !ok {
			continue
		}
		for _, f := range e.Flags {
			got[e.Name] = append(got[e.Name], f.Name)
		}
	}
	for name, expected := range want {
		actual := got[name]
		if !equalStringSlice(actual, expected) {
			t.Errorf("%s flags = %v, want %v", name, actual, expected)
		}
	}
}

// TestToolsJSONUpToDate fails when the committed tools.json file diverges
// from the generator's output. Runs in CI; locally, fix with:
//
//	go run ./cmd/cortex tools --out tools.json
//
// The test walks upward to find the repo root (the directory containing
// go.mod) so it works regardless of which subdirectory `go test` is run
// from.
func TestToolsJSONUpToDate(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("repo root not found: %v", err)
	}
	committedPath := filepath.Join(repoRoot, "tools.json")
	committed, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("read %s: %v (run `go run ./cmd/cortex tools` to generate)", committedPath, err)
	}

	manifest := GenerateManifest(BinaryVersion)
	expected, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	expected = append(expected, '\n')

	if string(committed) != string(expected) {
		t.Fatalf("tools.json is out of date — run `go run ./cmd/cortex tools --out tools.json`")
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
