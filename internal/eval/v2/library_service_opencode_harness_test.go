//go:build !windows

package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeOpencodeHappyScript is a minimal `opencode` CLI stand-in. opencode's
// prompt is the LAST positional argument (after `run` and all flags),
// so the script walks $@ to grab it. Writes a marker file in cwd (which
// the harness sets to workdir) and exits 0.
const fakeOpencodeHappyScript = `#!/bin/sh
set -e
LAST=""
for a in "$@"; do
  LAST="$a"
done
mkdir -p ./opencode-out
printf '%s\n' "$LAST" > ./opencode-out/last-prompt.txt
echo '{"type":"step_start","sessionID":"s","part":{"type":"step-start"}}'
echo '{"type":"step_finish","sessionID":"s","part":{"reason":"stop","tokens":{"input":0,"output":0},"cost":0}}'
`

// fakeOpencodeErrorScript exits non-zero with a known stderr line so the
// test can assert error wrapping/propagation.
const fakeOpencodeErrorScript = `#!/bin/sh
echo "fake opencode failure: model unreachable" >&2
exit 7
`

// fakeOpencodeSlowScript sleeps long enough for the ctx-cancel test to
// reliably trigger the SIGTERM path.
const fakeOpencodeSlowScript = `#!/bin/sh
sleep 30
`

// fakeOpencodeTelemetryScript emits an NDJSON event stream matching the
// shape documented in docs/opencode-tiers.md. Token / cost values are
// chosen so the test can spot mistakes in the aggregation rule
// (summing across step_finish events, not picking last).
//
// Coverage:
//   - 2 step_start events → AgentTurnsTotal=2
//   - 2 step_finish events with summed tokens/cost
//   - 2 completed edit tool_use events with unique paths
//   - 1 duplicate edit (same path) → must be deduped
//   - 1 errored edit (status="error") → excluded
//   - 1 "invalid" tool (model hallucinated tool name) → excluded
//   - 1 read tool_use → excluded from FilesChanged
const fakeOpencodeTelemetryScript = `#!/bin/sh
cat <<'EOF'
{"type":"step_start","sessionID":"s","part":{"type":"step-start"}}
{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"/x/handlers/books.go"}}}}
{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"/x/handlers/books.go"}}}}
{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"edit","state":{"status":"error","input":{"filePath":"/x/should-not-appear.go"}}}}
{"type":"step_finish","sessionID":"s","part":{"reason":"tool-calls","tokens":{"input":1000,"output":300},"cost":0.0020}}
{"type":"step_start","sessionID":"s","part":{"type":"step-start"}}
{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"invalid","state":{"status":"completed","input":{"filePath":"/x/also-not.go"}}}}
{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"/x/handlers/books_test.go"}}}}
{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"/x/handlers/books.go"}}}}
{"type":"text","sessionID":"s","part":{"text":"done"}}
{"type":"step_finish","sessionID":"s","part":{"reason":"tool-calls","tokens":{"input":1345,"output":378},"cost":0.0014}}
EOF
`

// fakeOpencodeEnvDumpScript writes OPENROUTER_API_KEY as seen by the
// subprocess into a marker file. Verifies the OPEN_ROUTER_API_KEY →
// OPENROUTER_API_KEY re-export bridge.
const fakeOpencodeEnvDumpScript = `#!/bin/sh
mkdir -p ./out
printf '%s' "${OPENROUTER_API_KEY:-UNSET}" > ./out/openrouter-key.txt
echo '{"type":"step_finish","sessionID":"s","part":{"tokens":{"input":1,"output":1},"cost":0}}'
`

// installFakeOpencode writes the given script to <dir>/opencode, chmods
// it executable, and returns the absolute path.
func installFakeOpencode(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "opencode")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	return path
}

func TestNewOpenCodeHarness_BinaryMissing(t *testing.T) {
	_, err := NewOpenCodeHarness("/path/does/not/exist/opencode", "openrouter/openai/gpt-oss-20b:free")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "opencode binary not found") {
		t.Errorf("err = %v, want 'opencode binary not found'", err)
	}
}

func TestNewOpenCodeHarness_OpencodeBinaryEnvRelativeRejected(t *testing.T) {
	t.Setenv("OPENCODE_BINARY", "relative/path/opencode")
	_, err := NewOpenCodeHarness("", "")
	if err == nil {
		t.Fatal("expected error for relative OPENCODE_BINARY")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("err = %v, want 'must be absolute'", err)
	}
}

func TestNewOpenCodeHarness_OpencodeBinaryEnvUsed(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeOpencode(t, binDir, fakeOpencodeHappyScript)
	t.Setenv("OPENCODE_BINARY", bin)

	h, err := NewOpenCodeHarness("", "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewOpenCodeHarness: %v", err)
	}
	if h.binary != bin {
		t.Errorf("binary = %q, want %q", h.binary, bin)
	}
}

// TestOpenCodeHarness_RunSession_HappyPath: harness invokes the fake
// opencode, the subprocess sees workdir as cwd, the prompt lands as the
// last positional arg, and exit 0 returns nil.
func TestOpenCodeHarness_RunSession_HappyPath(t *testing.T) {
	bin := installFakeOpencode(t, t.TempDir(), fakeOpencodeHappyScript)
	h, err := NewOpenCodeHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewOpenCodeHarness: %v", err)
	}

	workdir := t.TempDir()
	prompt := "implement books resource per spec"
	if err := h.RunSession(context.Background(), prompt, workdir); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workdir, "opencode-out", "last-prompt.txt"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(got)) != prompt {
		t.Errorf("forwarded prompt = %q, want %q", strings.TrimSpace(string(got)), prompt)
	}
}

// TestOpenCodeHarness_RunSession_NonZeroExitWrapsStderr: a non-zero exit
// MUST surface as an error that includes the captured stderr.
func TestOpenCodeHarness_RunSession_NonZeroExitWrapsStderr(t *testing.T) {
	bin := installFakeOpencode(t, t.TempDir(), fakeOpencodeErrorScript)
	h, err := NewOpenCodeHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewOpenCodeHarness: %v", err)
	}

	err = h.RunSession(context.Background(), "anything", t.TempDir())
	if err == nil {
		t.Fatal("expected non-nil error from failing opencode")
	}
	if !strings.Contains(err.Error(), "opencode exited") {
		t.Errorf("err = %v, want 'opencode exited' wrapper", err)
	}
	if !strings.Contains(err.Error(), "model unreachable") {
		t.Errorf("err = %v, want stderr ('model unreachable') in wrap", err)
	}
}

// TestOpenCodeHarness_RunSessionWithResult_FakeBinary exercises the
// NDJSON parser end-to-end. The fake script emits the full coverage
// case (2 steps, deduped edits, error/invalid tools excluded).
func TestOpenCodeHarness_RunSessionWithResult_FakeBinary(t *testing.T) {
	bin := installFakeOpencode(t, t.TempDir(), fakeOpencodeTelemetryScript)
	h, err := NewOpenCodeHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewOpenCodeHarness: %v", err)
	}

	res, err := h.RunSessionWithResult(context.Background(), "do thing", t.TempDir())
	if err != nil {
		t.Fatalf("RunSessionWithResult: %v", err)
	}

	if res.TokensIn != 2345 || res.TokensOut != 678 {
		t.Errorf("tokens: in=%d out=%d, want 2345/678", res.TokensIn, res.TokensOut)
	}
	if d := res.CostUSD - 0.0034; d > 1e-9 || d < -1e-9 {
		t.Errorf("CostUSD=%v want ~0.0034", res.CostUSD)
	}
	if res.AgentTurnsTotal != 2 {
		t.Errorf("AgentTurnsTotal=%d want 2", res.AgentTurnsTotal)
	}
	if len(res.FilesChanged) != 2 {
		t.Fatalf("FilesChanged: got %v want 2 entries (deduped)", res.FilesChanged)
	}
	if res.FilesChanged[0] != "/x/handlers/books.go" || res.FilesChanged[1] != "/x/handlers/books_test.go" {
		t.Errorf("FilesChanged=%v", res.FilesChanged)
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

func TestOpenCodeHarness_OpenRouterEnvBridge(t *testing.T) {
	bin := installFakeOpencode(t, t.TempDir(), fakeOpencodeEnvDumpScript)
	h, err := NewOpenCodeHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewOpenCodeHarness: %v", err)
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

// TestOpenCodeHarness_RunSession_ContextCancelTerminates: ctx cancel
// kills the subprocess group within the 2s SIGTERM grace window.
func TestOpenCodeHarness_RunSession_ContextCancelTerminates(t *testing.T) {
	bin := installFakeOpencode(t, t.TempDir(), fakeOpencodeSlowScript)
	h, err := NewOpenCodeHarness(bin, "openrouter/openai/gpt-oss-20b:free")
	if err != nil {
		t.Fatalf("NewOpenCodeHarness: %v", err)
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
			t.Errorf("RunSession took %s after cancel; want < 5s (SIGTERM+2s+SIGKILL)", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunSession did not return within 10s of ctx cancel; subprocess leak?")
	}
}

func TestParseOpencodeStream(t *testing.T) {
	tests := []struct {
		name             string
		in               string
		wantIn, wantOut  int
		wantCost         float64
		wantTurns        int
		wantFilesChanged []string
	}{
		{
			name: "summed across two step_finish events",
			in: strings.Join([]string{
				`{"type":"step_start","part":{}}`,
				`{"type":"step_finish","part":{"tokens":{"input":1000,"output":300},"cost":0.002}}`,
				`{"type":"step_start","part":{}}`,
				`{"type":"step_finish","part":{"tokens":{"input":1345,"output":378},"cost":0.0014}}`,
			}, "\n"),
			wantIn:    2345,
			wantOut:   678,
			wantCost:  0.0034,
			wantTurns: 2,
		},
		{
			name: "files deduped, errors and invalid tools excluded",
			in: strings.Join([]string{
				`{"type":"tool_use","part":{"tool":"edit","state":{"status":"completed","input":{"filePath":"a.go"}}}}`,
				`{"type":"tool_use","part":{"tool":"edit","state":{"status":"completed","input":{"filePath":"a.go"}}}}`,
				`{"type":"tool_use","part":{"tool":"write","state":{"status":"completed","input":{"filePath":"b.go"}}}}`,
				`{"type":"tool_use","part":{"tool":"edit","state":{"status":"error","input":{"filePath":"err.go"}}}}`,
				`{"type":"tool_use","part":{"tool":"invalid","state":{"status":"completed","input":{"filePath":"inv.go"}}}}`,
			}, "\n"),
			wantFilesChanged: []string{"a.go", "b.go"},
		},
		{
			name: "free model: cost field zero",
			in:   `{"type":"step_finish","part":{"tokens":{"input":42,"output":7},"cost":0}}`,
			wantIn:  42,
			wantOut: 7,
		},
		{
			name: "non-json lines are skipped",
			in: strings.Join([]string{
				`opencode v1.14.46 starting...`,
				`{"type":"step_finish","part":{"tokens":{"input":100,"output":50},"cost":0.001}}`,
				`final banner line`,
			}, "\n"),
			wantIn:   100,
			wantOut:  50,
			wantCost: 0.001,
		},
		{
			name: "unknown event types ignored",
			in: strings.Join([]string{
				`{"type":"some_future_event","part":{"tokens":{"input":999,"output":999}}}`,
				`{"type":"step_finish","part":{"tokens":{"input":10,"output":5},"cost":0}}`,
			}, "\n"),
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
			got := parseOpencodeStream(tc.in)
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

func TestOpencodeProviderFromModel(t *testing.T) {
	tests := []struct {
		model, want string
	}{
		{"openrouter/openai/gpt-oss-20b:free", "openrouter"},
		{"anthropic/claude-3-5-haiku", "anthropic"},
		{"ollama/qwen2.5-coder:1.5b", "ollama"},
		{"no-slash-here", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			if got := opencodeProviderFromModel(tc.model); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
