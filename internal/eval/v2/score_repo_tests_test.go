//go:build !windows

package eval

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFile is a small test helper to drop a file at path with content.
func writeRepoTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunRepoTests_BuildOnly_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows only")
	}
	workdir, err := os.MkdirTemp("", "cortex-repo-tests-build-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(workdir)

	writeRepoTestFile(t, filepath.Join(workdir, "go.mod"), "module repotest\n\ngo 1.26\n")
	writeRepoTestFile(t, filepath.Join(workdir, "main.go"), `package main

func main() {}
`)

	res, err := RunRepoTests(context.Background(), workdir, RepoTestSpec{
		BuildCmd: []string{"go", "build", "-o", "out", "."},
		Timeout:  60 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunRepoTests: %v", err)
	}
	if !res.BuildOK {
		t.Fatalf("BuildOK=false, BuildOut=%q", res.BuildOut)
	}
	if !res.AllPassed {
		t.Errorf("AllPassed=false on build-only success")
	}
	if _, err := os.Stat(filepath.Join(workdir, "out")); err != nil {
		t.Errorf("expected built binary at out: %v", err)
	}
}

func TestRunRepoTests_BuildFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows only")
	}
	workdir, err := os.MkdirTemp("", "cortex-repo-tests-buildfail-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(workdir)

	writeRepoTestFile(t, filepath.Join(workdir, "go.mod"), "module repotest\n\ngo 1.26\n")
	writeRepoTestFile(t, filepath.Join(workdir, "main.go"), `package main

func main() { totally_undefined() }
`)

	res, err := RunRepoTests(context.Background(), workdir, RepoTestSpec{
		BuildCmd: []string{"go", "build", "-o", "out", "."},
		Timeout:  60 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunRepoTests: %v", err)
	}
	if res.BuildOK {
		t.Fatalf("BuildOK should be false on broken source")
	}
	if res.AllPassed {
		t.Errorf("AllPassed should be false when build fails")
	}
	if !strings.Contains(res.BuildOut, "undefined") {
		t.Errorf("BuildOut should mention undefined identifier, got: %q", res.BuildOut)
	}
}

func TestRunRepoTests_GoTestPassFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows only")
	}
	workdir, err := os.MkdirTemp("", "cortex-repo-tests-gotest-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(workdir)

	writeRepoTestFile(t, filepath.Join(workdir, "go.mod"), "module repotest\n\ngo 1.26\n")
	writeRepoTestFile(t, filepath.Join(workdir, "lib.go"), `package repotest

func Add(a, b int) int { return a + b }
`)
	writeRepoTestFile(t, filepath.Join(workdir, "lib_test.go"), `package repotest

import "testing"

func TestAddPasses(t *testing.T) { if Add(1, 2) != 3 { t.Fatal("bad") } }
func TestAddFails(t *testing.T)  { if Add(1, 2) == 3 { t.Fatal("intentional") } }
`)

	res, err := RunRepoTests(context.Background(), workdir, RepoTestSpec{
		// No BuildCmd: only run tests.
		TestCmd:      []string{"go", "test", "-v", "./..."},
		ExpectedPass: []string{"TestAddPasses", "TestAddFails"},
		Timeout:      90 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunRepoTests: %v", err)
	}
	if !res.BuildOK {
		t.Fatalf("BuildOK should be true when BuildCmd is empty")
	}
	if res.AllPassed {
		t.Errorf("AllPassed should be false when one test fails")
	}
	if !containsString(res.Passed, "TestAddPasses") {
		t.Errorf("Passed should include TestAddPasses, got %v", res.Passed)
	}
	if !containsString(res.Failed, "TestAddFails") {
		t.Errorf("Failed should include TestAddFails, got %v", res.Failed)
	}
}

func TestRunRepoTests_TimeoutOnBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows only")
	}
	workdir, err := os.MkdirTemp("", "cortex-repo-tests-timeout-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(workdir)

	// `sleep 5` will exceed a 200ms timeout. Use as a proxy for a
	// build that never returns.
	res, err := RunRepoTests(context.Background(), workdir, RepoTestSpec{
		BuildCmd: []string{"sleep", "5"},
		Timeout:  200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunRepoTests: %v", err)
	}
	if res.BuildOK {
		t.Errorf("BuildOK should be false after timeout")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
