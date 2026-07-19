package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDecideBootstrap pins the wiring predicate itself: every combination
// of config-file presence x CORTEX_BACKEND env x TTY-ness that the four
// call sites in main.go/bootstrap_wire.go can produce. This is the "true
// first-run" question (config x env x TTY) — deliberately narrower than
// IsFirstRun (firstrun.go's config-or-greeting-marker predicate), which
// answers a different question (has the greeting turn fired).
func TestDecideBootstrap(t *testing.T) {
	tests := []struct {
		name                string
		userConfigExists    bool
		projectConfigExists bool
		hasBackendEnv       bool
		interactive         bool
		want                bootstrapAction
	}{
		{"nothing present, interactive => guided", false, false, false, true, bootstrapGuided},
		{"nothing present, non-interactive => hint", false, false, false, false, bootstrapHint},
		{"user config present, interactive => none", true, false, false, true, bootstrapNone},
		{"user config present, non-interactive => none", true, false, false, false, bootstrapNone},
		{"project config present, interactive => none", false, true, false, true, bootstrapNone},
		{"project config present, non-interactive => none", false, true, false, false, bootstrapNone},
		{"CORTEX_BACKEND set, interactive => none", false, false, true, true, bootstrapNone},
		{"CORTEX_BACKEND set, non-interactive => none", false, false, true, false, bootstrapNone},
		{"both config files present => none", true, true, false, true, bootstrapNone},
		{"config + env both present => none", true, false, true, true, bootstrapNone},
		{"every input true => none (config alone bypasses)", true, true, true, true, bootstrapNone},
		{"only project config, env unset, non-interactive => none", false, true, false, false, bootstrapNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideBootstrap(tt.userConfigExists, tt.projectConfigExists, tt.hasBackendEnv, tt.interactive)
			if got != tt.want {
				t.Errorf("decideBootstrap(%v, %v, %v, %v) = %v, want %v",
					tt.userConfigExists, tt.projectConfigExists, tt.hasBackendEnv, tt.interactive, got, tt.want)
			}
		})
	}
}

// TestCurrentBootstrapActionNoConfigNonTTYHints exercises the real
// gathering path (currentBootstrapAction, not the pure predicate) against
// an isolated CORTEX_HOME with no config anywhere and no CORTEX_BACKEND:
// a `go test` process's stdin is never a terminal, so this pins the
// non-TTY hint path end-to-end through userConfigPath/findConfigPath/
// lineedit.IsInteractive, not just the predicate in isolation.
func TestCurrentBootstrapActionNoConfigNonTTYHints(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Setenv("CORTEX_BACKEND", "")
	t.Chdir(t.TempDir())

	if got := currentBootstrapAction(); got != bootstrapHint {
		t.Errorf("currentBootstrapAction() = %v, want bootstrapHint (no config anywhere, non-TTY test process)", got)
	}
}

// TestCurrentBootstrapActionExistingUserConfigBypasses pins that an
// existing user config.json makes the wiring predicate skip the flow
// entirely (bootstrapNone) — the "existing config bypasses the flow"
// case named in the task.
func TestCurrentBootstrapActionExistingUserConfigBypasses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	t.Setenv("CORTEX_BACKEND", "")
	t.Chdir(t.TempDir())

	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"backend":{"type":"openrouter"}}`), 0o644); err != nil {
		t.Fatalf("failed to write fixture config.json: %v", err)
	}

	if got := currentBootstrapAction(); got != bootstrapNone {
		t.Errorf("currentBootstrapAction() = %v, want bootstrapNone (user config.json present)", got)
	}
}

// TestCurrentBootstrapActionExistingProjectConfigBypasses mirrors the
// above for a project .cortex/config.json instead of the user config —
// findConfigPath walks up from the CWD, so this pins that path too.
func TestCurrentBootstrapActionExistingProjectConfigBypasses(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Setenv("CORTEX_BACKEND", "")
	workspace := t.TempDir()
	t.Chdir(workspace)

	cortexDir := filepath.Join(workspace, ".cortex")
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		t.Fatalf("failed to create fixture .cortex dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cortexDir, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("failed to write fixture project config.json: %v", err)
	}

	if got := currentBootstrapAction(); got != bootstrapNone {
		t.Errorf("currentBootstrapAction() = %v, want bootstrapNone (project .cortex/config.json present)", got)
	}
}

// TestCurrentBootstrapActionBackendEnvBypasses pins the third bypass:
// CORTEX_BACKEND set, no config file anywhere.
func TestCurrentBootstrapActionBackendEnvBypasses(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Setenv("CORTEX_BACKEND", "http://localhost:9999")
	t.Chdir(t.TempDir())

	if got := currentBootstrapAction(); got != bootstrapNone {
		t.Errorf("currentBootstrapAction() = %v, want bootstrapNone ($CORTEX_BACKEND set)", got)
	}
}

// TestPrintFirstRunHint pins the non-TTY hint's content: it must name the
// user config path and point at docs/configuration.md, so a piped/CI
// first run gets an actionable pointer instead of a bare connection
// error.
func TestPrintFirstRunHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	printFirstRunHint()
	os.Stderr = orig
	w.Close()

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	wantConfigPath := filepath.Join(home, "config.json")
	if !strings.Contains(out, wantConfigPath) {
		t.Errorf("printFirstRunHint() output = %q, want it to name %q", out, wantConfigPath)
	}
	if !strings.Contains(out, "docs/configuration.md") {
		t.Errorf("printFirstRunHint() output = %q, want it to mention docs/configuration.md", out)
	}
}

// TestMaybeRunGuidedBootstrapNonTTYDoesNotBlock pins that the wiring
// entry point main() calls is safe to invoke in a non-interactive process
// (like this test): it must never attempt to read stdin (which would hang
// a `go test` process, since nothing is writing to it) when
// currentBootstrapAction() resolves to bootstrapHint or bootstrapNone.
// This is the "wiring predicate", not the interactive prompt itself —
// interactiveGuidedSetup's own behavior is exercised through its io seam,
// not by driving a real TTY.
func TestMaybeRunGuidedBootstrapNonTTYDoesNotBlock(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Setenv("CORTEX_BACKEND", "")
	t.Chdir(t.TempDir())

	done := make(chan struct{})
	go func() {
		maybeRunGuidedBootstrap()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("maybeRunGuidedBootstrap() did not return — it likely tried to read stdin in a non-TTY process")
	}
}

// TestInteractiveGuidedSetupSkipsOnEmptyKey pins Guide()'s decline path
// via its io seam: an empty line (the user pressing Enter) returns false
// and prints the skip message. Deliberately does NOT test the non-empty-
// key path here — that path calls storeOpenRouterKeychainKey, which would
// shell out to the REAL macOS Keychain and (with -U) overwrite any
// existing "cortex-openrouter" entry on the machine running the suite;
// that behavior is exercised at the BackendResolver level with fakes in
// bootstrap_test.go instead, per the task's scope (the interactive prompt
// itself stays covered there — this pins the io seam doesn't misfire on
// the safe, no-network branch).
func TestInteractiveGuidedSetupSkipsOnEmptyKey(t *testing.T) {
	in := strings.NewReader("\n")
	var out strings.Builder
	g := interactiveGuidedSetup{In: in, Out: &out}
	if ok := g.Guide(); ok {
		t.Errorf("Guide() = true, want false for an empty key")
	}
	if !strings.Contains(out.String(), "Skipped") {
		t.Errorf("Guide() output = %q, want it to mention Skipped", out.String())
	}
}
