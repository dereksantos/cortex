// serve_pid_test.go — pid-file round trip + stale-pid detection for
// `cortex serve stop`/`status` (see serve_pid.go). $CORTEX_HOME is
// redirected per test (t.Setenv) so these never touch a real
// ~/.cortex/serve.pid.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadRemoveServePIDRoundTrip(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	startedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if err := writeServePID(4242, 7433, startedAt); err != nil {
		t.Fatalf("writeServePID: %v", err)
	}

	path, err := servePIDPath()
	if err != nil {
		t.Fatalf("servePIDPath: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("serve.pid mode = %o, want 0600", perm)
	}

	info, err := readServePID()
	if err != nil {
		t.Fatalf("readServePID: %v", err)
	}
	if info.PID != 4242 {
		t.Errorf("PID = %d, want 4242", info.PID)
	}
	if info.Port != 7433 {
		t.Errorf("Port = %d, want 7433", info.Port)
	}
	if !info.StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v", info.StartedAt, startedAt)
	}

	if err := removeServePID(); err != nil {
		t.Fatalf("removeServePID: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("serve.pid still exists after removeServePID: err=%v", err)
	}

	// removeServePID on an already-absent file is not an error.
	if err := removeServePID(); err != nil {
		t.Errorf("removeServePID (already gone) = %v, want nil", err)
	}
}

func TestReadServePIDMissingFileReportsNotExist(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	if _, err := readServePID(); !os.IsNotExist(err) {
		t.Errorf("readServePID (no file) err = %v, want os.IsNotExist", err)
	}
}

// TestIsLiveCortexServeStalePID proves a stale pid file — one whose pid has
// already exited — is detected as not-live rather than trusted enough to
// signal. Uses a just-exited child process's pid (spec's suggested
// approach) rather than a hardcoded number, since pid reuse makes any fixed
// "definitely free" pid a myth.
func TestIsLiveCortexServeStalePID(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running `true`: %v", err)
	}
	stalePID := cmd.Process.Pid

	if isLiveCortexServe(stalePID) {
		t.Errorf("isLiveCortexServe(%d) = true for an already-exited process, want false", stalePID)
	}
}

// TestIsLiveCortexServeRejectsNonCortexProcess proves a pid that is very
// much alive but is NOT a cortex process (a long-lived `sleep`) is still
// rejected — the "recycled to an unrelated process" half of the stale-pid
// contract, not just "the pid is gone".
func TestIsLiveCortexServeRejectsNonCortexProcess(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if isLiveCortexServe(cmd.Process.Pid) {
		t.Errorf("isLiveCortexServe(%d) = true for a `sleep` process, want false", cmd.Process.Pid)
	}
}

func TestWriteServePIDResolvesUnderCortexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	if err := writeServePID(1, 2, time.Now()); err != nil {
		t.Fatalf("writeServePID: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "serve.pid")); err != nil {
		t.Errorf("serve.pid not under CORTEX_HOME: %v", err)
	}
}
