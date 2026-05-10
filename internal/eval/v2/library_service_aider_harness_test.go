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
