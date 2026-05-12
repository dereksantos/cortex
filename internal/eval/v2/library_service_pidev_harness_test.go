//go:build !windows

package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePiHappyScript is a minimal `pi` CLI stand-in. pi's prompt is the
// LAST positional argument (after `-p` and all flags), so the script
// walks $@ to grab it. Writes a marker in cwd and exits 0.
const fakePiHappyScript = `#!/bin/sh
set -e
LAST=""
for a in "$@"; do
  LAST="$a"
done
mkdir -p ./pi-out
printf '%s\n' "$LAST" > ./pi-out/last-prompt.txt
echo '{"type":"session","cwd":"."}'
echo '{"type":"agent_end"}'
`

// fakePiErrorScript exits non-zero with a known stderr line.
const fakePiErrorScript = `#!/bin/sh
echo "fake pi failure: model unreachable" >&2
exit 11
`

// fakePiSlowScript sleeps for the ctx-cancel test.
const fakePiSlowScript = `#!/bin/sh
sleep 30
`

// fakePiTelemetryScript emits an NDJSON event stream matching the
// shape documented in docs/pidev-events.md.
//
// Coverage:
//   - 2 turn_start events → AgentTurnsTotal=2
//   - 2 assistant message_end events with summed tokens/cost
//     (1000/300 + 1345/378 → 2345/678; 0.002 + 0.0014 → 0.0034)
//   - 1 user message_end → tokens NOT counted
//   - 1 toolResult message_end → tokens NOT counted
//   - 1 turn_end with the SAME usage as message_end → must NOT
//     double-count
//   - 2 completed edit tool_execution_end events (unique paths)
//   - 1 duplicate edit (same path) → deduped
//   - 1 errored edit (isError=true) → excluded
//   - 1 write tool_execution_end → included
//   - 1 read tool_execution_end → excluded from FilesChanged
const fakePiTelemetryScript = `#!/bin/sh
cat <<'EOF'
{"type":"session","cwd":"."}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_end","message":{"role":"user","content":[{"type":"text"}]}}
{"type":"tool_execution_end","toolName":"read","args":{"path":"hello.go"},"isError":false}
{"type":"tool_execution_end","toolName":"edit","args":{"path":"a.go"},"isError":false}
{"type":"tool_execution_end","toolName":"edit","args":{"path":"a.go"},"isError":false}
{"type":"tool_execution_end","toolName":"edit","args":{"path":"err.go"},"isError":true}
{"type":"message_end","message":{"role":"assistant","usage":{"input":1000,"output":300,"cost":{"total":0.002}}}}
{"type":"message_end","message":{"role":"toolResult"}}
{"type":"turn_end","message":{"role":"assistant","usage":{"input":1000,"output":300,"cost":{"total":0.002}}}}
{"type":"turn_start"}
{"type":"tool_execution_end","toolName":"write","args":{"path":"b.go"},"isError":false}
{"type":"message_end","message":{"role":"assistant","usage":{"input":1345,"output":378,"cost":{"total":0.0014}}}}
{"type":"agent_end"}
EOF
`

// fakePiEnvDumpScript verifies the OPEN_ROUTER_API_KEY → OPENROUTER_API_KEY
// re-export bridge.
const fakePiEnvDumpScript = `#!/bin/sh
mkdir -p ./out
printf '%s' "${OPENROUTER_API_KEY:-UNSET}" > ./out/openrouter-key.txt
echo '{"type":"message_end","message":{"role":"assistant","usage":{"input":1,"output":1,"cost":{"total":0}}}}'
`

// installFakePi writes the given script to <dir>/pi, chmods it
// executable, and returns the absolute path.
func installFakePi(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "pi")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func TestNewPiDevHarness_BinaryMissing(t *testing.T) {
	_, err := NewPiDevHarness("/path/does/not/exist/pi", "openrouter/openai/gpt-oss-20b:free")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "pi binary not found") {
		t.Errorf("err = %v, want 'pi binary not found'", err)
	}
}

func TestNewPiDevHarness_PiBinaryEnvRelativeRejected(t *testing.T) {
	t.Setenv("PI_BINARY", "relative/path/pi")
	_, err := NewPiDevHarness("", "")
	if err == nil {
		t.Fatal("expected error for relative PI_BINARY")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("err = %v, want 'must be absolute'", err)
	}
}

func TestNewPiDevHarness_PiBinaryEnvUsed(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakePi(t, binDir, fakePiHappyScript)
	t.Setenv("PI_BINARY", bin)

	h, err := NewPiDevHarness("", "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}
	if h.binary != bin {
		t.Errorf("binary = %q, want %q", h.binary, bin)
	}
}

// TestPiDevHarness_RunSession_HappyPath: harness invokes fake pi, the
// subprocess sees workdir as cwd, the prompt lands as the last
// positional arg, and exit 0 returns nil.
func TestPiDevHarness_RunSession_HappyPath(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiHappyScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}

	workdir := t.TempDir()
	prompt := "implement books resource per spec"
	if err := h.RunSession(context.Background(), prompt, workdir); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workdir, "pi-out", "last-prompt.txt"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(got)) != prompt {
		t.Errorf("forwarded prompt = %q, want %q", strings.TrimSpace(string(got)), prompt)
	}
}

func TestPiDevHarness_RunSession_NonZeroExitWrapsStderr(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiErrorScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}

	err = h.RunSession(context.Background(), "anything", t.TempDir())
	if err == nil {
		t.Fatal("expected non-nil error from failing pi")
	}
	if !strings.Contains(err.Error(), "pi exited") {
		t.Errorf("err = %v, want 'pi exited' wrapper", err)
	}
	if !strings.Contains(err.Error(), "model unreachable") {
		t.Errorf("err = %v, want stderr ('model unreachable') in wrap", err)
	}
}

// TestPiDevHarness_RunSessionWithResult_FakeBinary exercises the
// NDJSON parser end-to-end. The fake script emits the full coverage
// case including a turn_end that would double-count if the parser
// summed both event types.
func TestPiDevHarness_RunSessionWithResult_FakeBinary(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiTelemetryScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}

	res, err := h.RunSessionWithResult(context.Background(), "do thing", t.TempDir())
	if err != nil {
		t.Fatalf("RunSessionWithResult: %v", err)
	}

	if res.TokensIn != 2345 || res.TokensOut != 678 {
		t.Errorf("tokens: in=%d out=%d, want 2345/678 (double-count? user/toolResult counted?)",
			res.TokensIn, res.TokensOut)
	}
	if d := res.CostUSD - 0.0034; d > 1e-9 || d < -1e-9 {
		t.Errorf("CostUSD=%v want ~0.0034", res.CostUSD)
	}
	if res.AgentTurnsTotal != 2 {
		t.Errorf("AgentTurnsTotal=%d want 2", res.AgentTurnsTotal)
	}
	if len(res.FilesChanged) != 2 {
		t.Fatalf("FilesChanged: got %v want 2 entries (a.go deduped, err.go excluded, read excluded)",
			res.FilesChanged)
	}
	if res.FilesChanged[0] != "a.go" || res.FilesChanged[1] != "b.go" {
		t.Errorf("FilesChanged=%v want [a.go b.go]", res.FilesChanged)
	}
	if res.LatencyMs <= 0 {
		t.Errorf("LatencyMs=%d, want positive", res.LatencyMs)
	}
	if res.ProviderEcho != "openrouter" {
		t.Errorf("ProviderEcho=%q want openrouter", res.ProviderEcho)
	}
	if res.ModelEcho != "openrouter/openai/gpt-oss-20b:free" {
		t.Errorf("ModelEcho=%q", res.ModelEcho)
	}
}

func TestPiDevHarness_OpenRouterEnvBridge(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiEnvDumpScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}

	t.Setenv("OPEN_ROUTER_API_KEY", "sk-or-bridge-test")
	t.Setenv("OPENROUTER_API_KEY", "")

	workdir := t.TempDir()
	if _, err := h.RunSessionWithResult(context.Background(), "x", workdir); err != nil {
		t.Fatalf("RunSessionWithResult: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workdir, "out", "openrouter-key.txt"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "sk-or-bridge-test" {
		t.Errorf("subprocess saw OPENROUTER_API_KEY=%q, want sk-or-bridge-test (re-export missing)", string(got))
	}
}

func TestPiDevHarness_RunSession_ContextCancelTerminates(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiSlowScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- h.RunSession(ctx, "anything", t.TempDir())
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunSession returned nil; want context.Canceled")
		}
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("err = %v, want context.Canceled in chain", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("RunSession took %s after cancel; want < 5s", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunSession did not return within 10s of ctx cancel; subprocess leak?")
	}
}

func TestParsePidevStream(t *testing.T) {
	tests := []struct {
		name             string
		in               string
		wantIn, wantOut  int
		wantCost         float64
		wantTurns        int
		wantFilesChanged []string
	}{
		{
			name: "summed across two assistant message_end events; turn_end not double-counted",
			in: strings.Join([]string{
				`{"type":"turn_start"}`,
				`{"type":"message_end","message":{"role":"assistant","usage":{"input":1000,"output":300,"cost":{"total":0.002}}}}`,
				`{"type":"turn_end","message":{"role":"assistant","usage":{"input":1000,"output":300,"cost":{"total":0.002}}}}`,
				`{"type":"turn_start"}`,
				`{"type":"message_end","message":{"role":"assistant","usage":{"input":1345,"output":378,"cost":{"total":0.0014}}}}`,
			}, "\n"),
			wantIn:    2345,
			wantOut:   678,
			wantCost:  0.0034,
			wantTurns: 2,
		},
		{
			name: "user and toolResult message_end events excluded from token sum",
			in: strings.Join([]string{
				`{"type":"message_end","message":{"role":"user","usage":{"input":9999}}}`,
				`{"type":"message_end","message":{"role":"toolResult","usage":{"input":9999}}}`,
				`{"type":"message_end","message":{"role":"assistant","usage":{"input":42,"output":7,"cost":{"total":0}}}}`,
			}, "\n"),
			wantIn:  42,
			wantOut: 7,
		},
		{
			name: "files deduped, errors and non-edit tools excluded",
			in: strings.Join([]string{
				`{"type":"tool_execution_end","toolName":"edit","args":{"path":"a.go"},"isError":false}`,
				`{"type":"tool_execution_end","toolName":"edit","args":{"path":"a.go"},"isError":false}`,
				`{"type":"tool_execution_end","toolName":"write","args":{"path":"b.go"},"isError":false}`,
				`{"type":"tool_execution_end","toolName":"edit","args":{"path":"err.go"},"isError":true}`,
				`{"type":"tool_execution_end","toolName":"read","args":{"path":"r.go"},"isError":false}`,
				`{"type":"tool_execution_end","toolName":"bash","args":{},"isError":false}`,
			}, "\n"),
			wantFilesChanged: []string{"a.go", "b.go"},
		},
		{
			name: "non-json lines skipped",
			in: strings.Join([]string{
				`pi 0.74.0 starting...`,
				`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":50,"cost":{"total":0.001}}}}`,
			}, "\n"),
			wantIn:   100,
			wantOut:  50,
			wantCost: 0.001,
		},
		{
			name:    "missing cost field handled as zero",
			in:      `{"type":"message_end","message":{"role":"assistant","usage":{"input":10,"output":5}}}`,
			wantIn:  10,
			wantOut: 5,
		},
		{
			name: "empty stream → all zero, no panic",
			in:   "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePidevStream(tc.in)
			if got.TokensIn != tc.wantIn || got.TokensOut != tc.wantOut {
				t.Errorf("tokens: got in=%d out=%d, want in=%d out=%d",
					got.TokensIn, got.TokensOut, tc.wantIn, tc.wantOut)
			}
			if d := got.CostUSD - tc.wantCost; d > 1e-9 || d < -1e-9 {
				t.Errorf("cost=%v want ~%v", got.CostUSD, tc.wantCost)
			}
			if got.AgentTurnsTotal != tc.wantTurns {
				t.Errorf("AgentTurnsTotal=%d want %d", got.AgentTurnsTotal, tc.wantTurns)
			}
			if len(got.FilesChanged) != len(tc.wantFilesChanged) {
				t.Errorf("FilesChanged: got %v want %v", got.FilesChanged, tc.wantFilesChanged)
				return
			}
			for i := range got.FilesChanged {
				if got.FilesChanged[i] != tc.wantFilesChanged[i] {
					t.Errorf("FilesChanged[%d]: got %q want %q", i, got.FilesChanged[i], tc.wantFilesChanged[i])
				}
			}
		})
	}
}

func TestSplitPiModel(t *testing.T) {
	tests := []struct {
		in, wantProvider, wantModel string
	}{
		{"openrouter/openai/gpt-oss-20b:free", "openrouter", "openai/gpt-oss-20b:free"},
		{"anthropic/claude-3-5-haiku", "anthropic", "claude-3-5-haiku"},
		{"no-slash-just-model", "", "no-slash-just-model"},
		{"", "", ""},
		{"/leading-slash", "", "/leading-slash"}, // empty provider rejected; treat as model-only
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p, m := splitPiModel(tc.in)
			if p != tc.wantProvider || m != tc.wantModel {
				t.Errorf("splitPiModel(%q) = (%q, %q), want (%q, %q)",
					tc.in, p, m, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

// TestPiDevHarness_RunSessionWithResult_OpenRouterIntegration drives
// the real pi binary against an OpenRouter free model. Skips when
// the binary isn't on PATH or OPEN_ROUTER_API_KEY isn't set —
// including CI by default.
//
// Litmus test from the Phase 7 pass criteria: TokensIn > 0 proves
// the model actually saw the workdir's seed file (same trap that
// bit AiderHarness on 2026-05-10).
func TestPiDevHarness_RunSessionWithResult_OpenRouterIntegration(t *testing.T) {
	if os.Getenv("OPEN_ROUTER_API_KEY") == "" {
		t.Skip("OPEN_ROUTER_API_KEY not set — skipping real-binary smoke")
	}
	bin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not in PATH")
	}

	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}

	workdir := t.TempDir()
	stub := `package main

import "fmt"

// Greet returns a greeting message. TODO: implement.
func Greet(name string) string {
	return "" // TODO
}

func main() {
	fmt.Println(Greet("world"))
}
`
	if err := os.WriteFile(filepath.Join(workdir, "hello.go"), []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := h.RunSessionWithResult(ctx,
		"Implement the Greet function in hello.go so it returns 'Hello, ' + name + '!'.",
		workdir)
	if err != nil {
		t.Fatalf("RunSessionWithResult: %v", err)
	}

	if res.TokensIn <= 0 {
		t.Errorf("TokensIn=%d, want positive (model never saw the workdir?)", res.TokensIn)
	}
	if res.LatencyMs <= 0 {
		t.Errorf("LatencyMs=%d, want positive", res.LatencyMs)
	}
	if res.ProviderEcho != "openrouter" {
		t.Errorf("ProviderEcho=%q want openrouter", res.ProviderEcho)
	}
	if res.ModelEcho != "openrouter/openai/gpt-oss-20b:free" {
		t.Errorf("ModelEcho=%q want openrouter/openai/gpt-oss-20b:free", res.ModelEcho)
	}
}

// ---- Phase 8: cortex extension wiring -------------------------------------

func TestPiDevHarness_CortexExtensionToggle(t *testing.T) {
	h, err := NewPiDevHarness(installFakePi(t, t.TempDir(), fakePiHappyScript), "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}
	if h.CortexExtensionEnabled() {
		t.Fatal("new harness must start with extension disabled")
	}
	h.SetCortexExtensionEnabled(true)
	if !h.CortexExtensionEnabled() {
		t.Fatal("SetCortexExtensionEnabled(true) did not stick")
	}
	h.SetCortexExtensionEnabled(false)
	if h.CortexExtensionEnabled() {
		t.Fatal("SetCortexExtensionEnabled(false) did not reset")
	}
}

func TestEnsurePiCortexExtensionInstalled_MissingSourceErrors(t *testing.T) {
	t.Setenv(EnvPiCortexExtensionSource, "")
	t.Setenv("CORTEX_BINARY", "/tmp/some-cortex")
	err := ensurePiCortexExtensionInstalled(t.TempDir())
	if err == nil {
		t.Fatal("expected error when $CORTEX_PI_EXTENSION_SOURCE is unset")
	}
	if !strings.Contains(err.Error(), "CORTEX_PI_EXTENSION_SOURCE") {
		t.Errorf("err = %v, want mention of the env var name", err)
	}
}

func TestEnsurePiCortexExtensionInstalled_RelativeSourceRejected(t *testing.T) {
	t.Setenv(EnvPiCortexExtensionSource, "packages/pi-cortex")
	err := ensurePiCortexExtensionInstalled(t.TempDir())
	if err == nil {
		t.Fatal("expected error for relative source path")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("err = %v, want 'must be absolute'", err)
	}
}

func TestEnsurePiCortexExtensionInstalled_HappyPathSymlinks(t *testing.T) {
	// Stand up a fake "packages/pi-cortex" source tree.
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	t.Setenv(EnvPiCortexExtensionSource, source)

	workdir := t.TempDir()
	if err := ensurePiCortexExtensionInstalled(workdir); err != nil {
		t.Fatalf("install: %v", err)
	}

	dest := filepath.Join(workdir, ".pi", "extensions", "cortex")
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dest is not a symlink: mode=%v", info.Mode())
	}
	target, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != source {
		t.Errorf("symlink target = %q, want %q", target, source)
	}

	// Idempotent — calling again should replace the stale symlink
	// without error.
	if err := ensurePiCortexExtensionInstalled(workdir); err != nil {
		t.Fatalf("re-install: %v", err)
	}
}

func TestPiDevHarness_CortexExtension_RunSession_InstallsAndChecksEnv(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiHappyScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}
	h.SetCortexExtensionEnabled(true)

	// Source + binary both set → install runs, pi gets invoked.
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	t.Setenv(EnvPiCortexExtensionSource, source)
	t.Setenv("CORTEX_BINARY", "/tmp/fake-cortex")

	workdir := t.TempDir()
	if err := h.RunSession(context.Background(), "do thing", workdir); err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	dest := filepath.Join(workdir, ".pi", "extensions", "cortex")
	if _, err := os.Lstat(dest); err != nil {
		t.Errorf("expected extension symlink at %s, got: %v", dest, err)
	}
}

func TestPiDevHarness_CortexExtension_MissingCortexBinaryErrors(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiHappyScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}
	h.SetCortexExtensionEnabled(true)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	t.Setenv(EnvPiCortexExtensionSource, source)
	t.Setenv("CORTEX_BINARY", "")

	err = h.RunSession(context.Background(), "x", t.TempDir())
	if err == nil {
		t.Fatal("expected error when CORTEX_BINARY is unset and extension is enabled")
	}
	if !strings.Contains(err.Error(), "CORTEX_BINARY") {
		t.Errorf("err = %v, want mention of CORTEX_BINARY", err)
	}
}

func TestPiDevHarness_CortexExtensionDisabled_NoInstall(t *testing.T) {
	bin := installFakePi(t, t.TempDir(), fakePiHappyScript)
	h, err := NewPiDevHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewPiDevHarness: %v", err)
	}
	// Explicitly disabled (default state too).
	h.SetCortexExtensionEnabled(false)

	// Even with the source env var set, RunSession with extension
	// disabled must NOT install — baseline cells share the same
	// harness instance and would otherwise leak the install.
	source := t.TempDir()
	t.Setenv(EnvPiCortexExtensionSource, source)

	workdir := t.TempDir()
	if err := h.RunSession(context.Background(), "x", workdir); err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workdir, ".pi", "extensions", "cortex")); err == nil {
		t.Error("extension dir must NOT be created when SetCortexExtensionEnabled(false)")
	}
}
