//go:build !windows

package eval

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAiderHappyScript is a minimal `aider` CLI stand-in for the harness
// happy path. It pulls --message out of argv, writes a deterministic
// marker file into cwd (the workdir), and exits 0. Mirrors the
// fakeCortexScript pattern used by inject_test.go.
const fakeAiderHappyScript = `#!/bin/sh
set -e
MSG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --message) MSG="$2"; shift 2 ;;
    --message=*) MSG="${1#--message=}"; shift ;;
    *) shift ;;
  esac
done
mkdir -p ./aider-out
printf '%s\n' "$MSG" > ./aider-out/last-message.txt
echo "fake aider ok"
`

// fakeAiderErrorScript exits non-zero with a known stderr line so the test
// can assert error wrapping/propagation.
const fakeAiderErrorScript = `#!/bin/sh
echo "fake aider failure: model unreachable" >&2
exit 13
`

// fakeAiderSlowScript sleeps long enough for the ctx-cancel test to
// reliably trigger the SIGTERM path. Reads from /dev/null so the trap is
// reached promptly when the parent kills the process group.
const fakeAiderSlowScript = `#!/bin/sh
sleep 30
`

// installFakeAider writes the given script to <dir>/aider, chmods it
// executable, and returns the absolute path. The whole file is tagged
// !windows because the shebang machinery is Unix-only.
func installFakeAider(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "aider")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake aider: %v", err)
	}
	return path
}

func TestNewAiderHarness_BinaryMissing(t *testing.T) {
	_, err := NewAiderHarness("/path/does/not/exist/aider", "ollama/qwen2.5-coder:1.5b")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "aider binary not found") {
		t.Errorf("err = %v, want 'aider binary not found'", err)
	}
}

func TestNewAiderHarness_AiderBinaryEnvRelativeRejected(t *testing.T) {
	t.Setenv("AIDER_BINARY", "relative/path/aider")
	_, err := NewAiderHarness("", "")
	if err == nil {
		t.Fatal("expected error for relative AIDER_BINARY")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("err = %v, want 'must be absolute'", err)
	}
}

func TestNewAiderHarness_AiderBinaryEnvUsed(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeAider(t, binDir, fakeAiderHappyScript)
	t.Setenv("AIDER_BINARY", bin)

	h, err := NewAiderHarness("", "ollama/qwen2.5-coder:1.5b")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
	}
	if h.binary != bin {
		t.Errorf("binary = %q, want %q", h.binary, bin)
	}
}

// TestAiderHarness_RunSession_HappyPath confirms the harness invokes the
// fake aider with the prompt, the subprocess sees workdir as cwd, and a
// successful exit returns nil.
func TestAiderHarness_RunSession_HappyPath(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeAider(t, binDir, fakeAiderHappyScript)
	h, err := NewAiderHarness(bin, "ollama/qwen2.5-coder:1.5b")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
	}

	workdir := t.TempDir()
	prompt := "implement books resource per spec"
	if err := h.RunSession(context.Background(), prompt, workdir); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	// The fake aider wrote ./aider-out/last-message.txt — confirms cwd was
	// workdir AND that --message was forwarded.
	got, err := os.ReadFile(filepath.Join(workdir, "aider-out", "last-message.txt"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(got)) != prompt {
		t.Errorf("forwarded message = %q, want %q", strings.TrimSpace(string(got)), prompt)
	}
}

// TestAiderHarness_RunSession_NonZeroExitWrapsStderr: a non-zero exit MUST
// surface as an error that includes the captured stderr, so eval failures
// land in the runner with a useful diagnostic.
func TestAiderHarness_RunSession_NonZeroExitWrapsStderr(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeAider(t, binDir, fakeAiderErrorScript)
	h, err := NewAiderHarness(bin, "ollama/qwen2.5-coder:1.5b")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
	}

	err = h.RunSession(context.Background(), "anything", t.TempDir())
	if err == nil {
		t.Fatal("expected non-nil error from failing aider")
	}
	if !strings.Contains(err.Error(), "aider exited") {
		t.Errorf("err = %v, want 'aider exited' wrapper", err)
	}
	if !strings.Contains(err.Error(), "model unreachable") {
		t.Errorf("err = %v, want stderr ('model unreachable') in wrap", err)
	}
}

// fakeAiderTelemetryScript emits aider-like summary lines (matching
// 0.86.2 output format) so the parser can be exercised without a real
// LLM call.
const fakeAiderTelemetryScript = `#!/bin/sh
mkdir -p ./out
cat > ./out/edited.go <<'EOF'
package x
EOF
echo "Aider v0.86.2"
echo "Applied edit to out/edited.go"
echo "Applied edit to out/another.go"
echo "Tokens: 2,345 sent, 678 received."
echo "Cost: \$0.0034 message, \$0.0034 session."
`

// fakeAiderEnvDumpScript writes the OPENROUTER_API_KEY env var seen by
// the subprocess to a marker file. Used to verify the OPEN_ROUTER_API_KEY
// → OPENROUTER_API_KEY re-export bridge that the harness installs for
// litellm compatibility.
const fakeAiderEnvDumpScript = `#!/bin/sh
mkdir -p ./out
printf '%s' "${OPENROUTER_API_KEY:-UNSET}" > ./out/openrouter-key.txt
echo "Tokens: 1 sent, 1 received."
echo "Cost: \$0 message, \$0 session."
`

func TestParseAiderOutput(t *testing.T) {
	tests := []struct {
		name             string
		in               string
		wantIn, wantOut  int
		wantCost         float64
		wantFilesChanged []string
	}{
		{
			name: "comma format with two edits",
			in: `Aider v0.86.2
Applied edit to handlers/books.go
Applied edit to handlers/books_test.go
Tokens: 1,234 sent, 567 received.
Cost: $0.0012 message, $0.0024 session.`,
			wantIn:           1234,
			wantOut:          567,
			wantCost:         0.0012,
			wantFilesChanged: []string{"handlers/books.go", "handlers/books_test.go"},
		},
		{
			name:     "k suffix, zero cost (free model)",
			in:       "Tokens: 1.2k sent, 567 received.\nCost: $0.00 message, $0.00 session.",
			wantIn:   1200,
			wantOut:  567,
			wantCost: 0.00,
		},
		{
			name:     "request variant cost label",
			in:       "Tokens: 100 sent, 50 received.\nCost: $0.0050 request, $0.01 session.",
			wantIn:   100,
			wantOut:  50,
			wantCost: 0.0050,
		},
		{
			name:    "tokens only, no cost line",
			in:      "Tokens: 100 sent, 50 received.",
			wantIn:  100,
			wantOut: 50,
		},
		{
			name: "no summary at all",
			in:   "Aider crashed before summary",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAiderOutput(tc.in)
			if got.TokensIn != tc.wantIn || got.TokensOut != tc.wantOut {
				t.Errorf("tokens: got in=%d out=%d, want in=%d out=%d",
					got.TokensIn, got.TokensOut, tc.wantIn, tc.wantOut)
			}
			if got.CostUSD != tc.wantCost {
				t.Errorf("cost=%v want %v", got.CostUSD, tc.wantCost)
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

func TestParseAiderNumber(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"1234", 1234},
		{"1,234", 1234},
		{"1.2k", 1200},
		{"1.2K", 1200},
		{"1.5m", 1_500_000},
		{"1.5M", 1_500_000},
		{"0", 0},
		{"", 0},
		{"abc", 0},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := parseAiderNumber(tc.in); got != tc.want {
				t.Errorf("parseAiderNumber(%q)=%d want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestAiderProviderFromModel(t *testing.T) {
	tests := []struct {
		model, want string
	}{
		{"ollama/qwen2.5-coder:1.5b", "ollama"},
		{"openrouter/openai/gpt-oss-20b:free", "openrouter"},
		{"anthropic/claude-3-5-haiku", "anthropic"},
		{"no-slash-here", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			if got := aiderProviderFromModel(tc.model); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAiderHarness_RunSessionWithResult_FakeBinary(t *testing.T) {
	bin := installFakeAider(t, t.TempDir(), fakeAiderTelemetryScript)
	h, err := NewAiderHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
	}

	res, err := h.RunSessionWithResult(context.Background(), "do thing", t.TempDir())
	if err != nil {
		t.Fatalf("RunSessionWithResult: %v", err)
	}

	if res.TokensIn != 2345 || res.TokensOut != 678 {
		t.Errorf("tokens: in=%d out=%d, want 2345/678", res.TokensIn, res.TokensOut)
	}
	if res.CostUSD != 0.0034 {
		t.Errorf("CostUSD=%v want 0.0034", res.CostUSD)
	}
	if len(res.FilesChanged) != 2 {
		t.Fatalf("FilesChanged: got %v want 2 entries", res.FilesChanged)
	}
	if res.FilesChanged[0] != "out/edited.go" || res.FilesChanged[1] != "out/another.go" {
		t.Errorf("FilesChanged=%v", res.FilesChanged)
	}
	if res.LatencyMs <= 0 {
		t.Errorf("LatencyMs=%d, want positive", res.LatencyMs)
	}
	if res.AgentTurnsTotal != 1 {
		t.Errorf("AgentTurnsTotal=%d want 1", res.AgentTurnsTotal)
	}
	if res.ProviderEcho != "openrouter" {
		t.Errorf("ProviderEcho=%q want openrouter", res.ProviderEcho)
	}
	if res.ModelEcho != "openrouter/openai/gpt-oss-20b:free" {
		t.Errorf("ModelEcho=%q", res.ModelEcho)
	}
}

func TestAiderHarness_OpenRouterEnvBridge(t *testing.T) {
	bin := installFakeAider(t, t.TempDir(), fakeAiderEnvDumpScript)
	h, err := NewAiderHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
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
		t.Errorf("subprocess saw OPENROUTER_API_KEY=%q, want %q (re-export from OPEN_ROUTER_API_KEY missing)", string(got), "sk-or-bridge-test")
	}
}

// TestAiderHarness_RunSessionWithResult_OllamaIntegration drives the real
// Aider binary against a local Ollama server. Skips on every machine
// where Ollama isn't running on the default port — including CI.
func TestAiderHarness_RunSessionWithResult_OllamaIntegration(t *testing.T) {
	if !ollamaReachable() {
		t.Skip("ollama not running on 127.0.0.1:11434 — start `ollama serve` and `ollama pull qwen2.5-coder:1.5b` to exercise this test")
	}
	bin, err := exec.LookPath("aider")
	if err != nil {
		t.Skip("aider not in PATH")
	}

	h, err := NewAiderHarness(bin, "ollama/qwen2.5-coder:1.5b")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
	}

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "hello.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := h.RunSessionWithResult(ctx, "Add a func main() that prints hello.", workdir)
	if err != nil {
		t.Fatalf("RunSessionWithResult: %v", err)
	}

	if res.LatencyMs <= 0 {
		t.Errorf("LatencyMs=%d, want positive", res.LatencyMs)
	}
	if res.AgentTurnsTotal <= 0 {
		t.Errorf("AgentTurnsTotal=%d, want positive", res.AgentTurnsTotal)
	}
	if res.TokensIn <= 0 {
		t.Errorf("TokensIn=%d, want positive (parse failure?)", res.TokensIn)
	}
	if res.ProviderEcho != "ollama" {
		t.Errorf("ProviderEcho=%q want ollama", res.ProviderEcho)
	}
}

func ollamaReachable() bool {
	c := http.Client{Timeout: 1 * time.Second}
	resp, err := c.Get("http://127.0.0.1:11434/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// TestAiderHarness_RunSession_ContextCancelTerminates: ctx cancel MUST
// kill the subprocess group within the 2s SIGTERM grace window. We give
// the test 5s of slack to absorb scheduler jitter on slow CI hardware.
func TestAiderHarness_RunSession_ContextCancelTerminates(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeAider(t, binDir, fakeAiderSlowScript)
	h, err := NewAiderHarness(bin, "ollama/qwen2.5-coder:1.5b")
	if err != nil {
		t.Fatalf("NewAiderHarness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- h.RunSession(ctx, "anything", t.TempDir())
	}()

	// Give the subprocess time to actually start before cancelling.
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
			t.Errorf("RunSession took %s after cancel; want < 5s (SIGTERM+2s+SIGKILL)", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunSession did not return within 10s of ctx cancel; subprocess leak?")
	}
}
