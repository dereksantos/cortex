// serve_stop_cli_test.go — `cortex serve stop`/`cortex serve status`
// against a not-running state (design item 5's CLI-level coverage). These
// exercise runServeStopCLI/runServeStatusCLI as subprocesses since both
// call os.Exit — matching how this codebase already tests CLI entry points
// that terminate the process (see how runXxxCLI wrappers are documented as
// "untested" in-process; the pure helpers underneath carry the unit
// coverage, and this file covers the actual os.Exit behavior end to end).
// The binary is built once (sync.Once) and shared by every test here to
// keep this file's contribution to `go test ./...` bounded.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	serveStopTestBinOnce sync.Once
	serveStopTestBinPath string
	serveStopTestBinErr  error
)

// serveStopTestBinary compiles the cortex binary once per `go test` run
// into a shared temp dir, reused by every test in this file.
func serveStopTestBinary(t *testing.T) string {
	t.Helper()
	serveStopTestBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cortex-servestop-bin")
		if err != nil {
			serveStopTestBinErr = err
			return
		}
		bin := filepath.Join(dir, "cortex-servestop-test")
		cmd := exec.Command("go", "build", "-o", bin, ".")
		out, err := cmd.CombinedOutput()
		if err != nil {
			serveStopTestBinErr = errors.New(string(out))
			return
		}
		serveStopTestBinPath = bin
	})
	if serveStopTestBinErr != nil {
		t.Fatalf("building cortex test binary: %v", serveStopTestBinErr)
	}
	return serveStopTestBinPath
}

func TestServeStopCLINotRunning(t *testing.T) {
	bin := serveStopTestBinary(t)
	home := t.TempDir()

	cmd := exec.Command(bin, "serve", "stop")
	cmd.Env = append(os.Environ(), "CORTEX_HOME="+home)
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("serve stop (not running) exit = %v, want exit 1; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "not running") {
		t.Errorf("serve stop (not running) output = %q, want it to mention \"not running\"", out)
	}
}

func TestServeStatusCLINotRunning(t *testing.T) {
	bin := serveStopTestBinary(t)
	home := t.TempDir()

	cmd := exec.Command(bin, "serve", "status")
	cmd.Env = append(os.Environ(), "CORTEX_HOME="+home)
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("serve status (not running) exit = %v, want exit 1; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "not running") {
		t.Errorf("serve status (not running) output = %q, want it to mention \"not running\"", out)
	}
}

// TestServeStopCLIStalePIDCleansUp proves a stale pid file (pid of a
// process that has already exited) is cleaned up, not just reported —
// runServeStopCLI removes it before reporting not-running.
func TestServeStopCLIStalePIDCleansUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatalf("running `true`: %v", err)
	}
	if err := writeServePID(dead.Process.Pid, 7433, time.Now()); err != nil {
		t.Fatalf("writeServePID: %v", err)
	}

	bin := serveStopTestBinary(t)
	cmd := exec.Command(bin, "serve", "stop")
	cmd.Env = append(os.Environ(), "CORTEX_HOME="+home)
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("serve stop (stale pid) exit = %v, want exit 1; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "not running") {
		t.Errorf("serve stop (stale pid) output = %q, want it to mention \"not running\"", out)
	}
	if _, statErr := os.Stat(filepath.Join(home, "serve.pid")); !os.IsNotExist(statErr) {
		t.Errorf("stale serve.pid was not cleaned up: err=%v", statErr)
	}
}
