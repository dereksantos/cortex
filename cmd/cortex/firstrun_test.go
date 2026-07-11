package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsFirstRunMarkerAbsentTrue pins the GOAL.md §3 P1 first-run
// contract's base case: neither config.json nor a greeting marker exists
// under the (isolated) user home ⇒ first run.
func TestIsFirstRunMarkerAbsentTrue(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	if !IsFirstRun() {
		t.Errorf("IsFirstRun() = false, want true (no config.json, no marker)")
	}
}

// TestIsFirstRunConfigPresentFalse pins that a config.json under the user
// home alone is sufficient to make IsFirstRun false, even with no
// greeting marker.
func TestIsFirstRunConfigPresentFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	t.Chdir(t.TempDir())

	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("failed to write fixture config.json: %v", err)
	}

	if IsFirstRun() {
		t.Errorf("IsFirstRun() = true, want false (config.json present)")
	}
}

// TestIsFirstRunMarkerPresentFalse pins that the greeting marker alone
// (no config.json) is sufficient to make IsFirstRun false.
func TestIsFirstRunMarkerPresentFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	t.Chdir(t.TempDir())

	if err := os.WriteFile(filepath.Join(home, greetingMarkerName), []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to write fixture greeting marker: %v", err)
	}

	if IsFirstRun() {
		t.Errorf("IsFirstRun() = true, want false (greeting marker present)")
	}
}

// TestIsFirstRunIgnoresSessions pins GOAL.md §3 P1's binding
// interpretation that sessions are project-scoped and play no part in
// first-run detection, overriding docs/cortex-web.md's older "no prior
// sessions" language: a session directory under the (project-scoped,
// CWD-isolated) workspace must not affect the verdict.
func TestIsFirstRunIgnoresSessions(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	workspace := t.TempDir()
	t.Chdir(workspace)

	sessDir := filepath.Join(workspace, ".cortex", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("failed to create fixture session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "abc123.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture session transcript: %v", err)
	}

	if !IsFirstRun() {
		t.Errorf("IsFirstRun() = false, want true (sessions must not affect first-run detection)")
	}
}
