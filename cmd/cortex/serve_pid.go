// serve_pid.go — the `<userhome>/serve.pid` file `cortex serve stop`/
// `status` (serve_stop.go) key off, and runServeCLI (serve.go) writes on
// startup and removes on clean shutdown. Signal-based stop works even when
// the HTTP surface itself is wedged, and needs no auth story of its own
// (SECURITY.md's Host/Origin posture note covers the API; this file covers
// process control, a different surface entirely).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/userhome"
)

// servePIDInfo is the parsed contents of serve.pid.
type servePIDInfo struct {
	PID       int
	Port      int
	StartedAt time.Time
}

// servePIDPath resolves <userhome>/serve.pid — the same internal/userhome
// resolution serve.token used (cleanupLegacyServeToken, serve.go), so
// $CORTEX_HOME redirects it identically in tests.
func servePIDPath() (string, error) {
	return userhome.Path("serve.pid")
}

// writeServePID records the running serve process's identity to
// <userhome>/serve.pid: one line, "<pid> <port> <started-at-RFC3339>".
// 0600 — machine-local process metadata, not a secret, but no reason to
// make it world-readable. `cortex serve stop`/`status` read it back
// (readServePID); runServeCLI's drain path removes it on clean shutdown
// (removeServePID).
func writeServePID(pid, port int, startedAt time.Time) error {
	path, err := servePIDPath()
	if err != nil {
		return fmt.Errorf("failed to resolve serve.pid path: %w", err)
	}
	line := fmt.Sprintf("%d %d %s\n", pid, port, startedAt.UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		return fmt.Errorf("failed to write serve.pid: %w", err)
	}
	return nil
}

// readServePID reads and parses <userhome>/serve.pid. A missing file
// returns the raw *PathError from os.ReadFile unwrapped, so
// os.IsNotExist(err) still reports true — callers (runServeStopCLI/
// runServeStatusCLI) treat that as "not running", not an infra failure.
func readServePID() (servePIDInfo, error) {
	path, err := servePIDPath()
	if err != nil {
		return servePIDInfo{}, fmt.Errorf("failed to resolve serve.pid path: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return servePIDInfo{}, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return servePIDInfo{}, fmt.Errorf("malformed serve.pid (%s): want at least 2 fields, got %d", path, len(fields))
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return servePIDInfo{}, fmt.Errorf("malformed serve.pid (%s): bad pid %q: %w", path, fields[0], err)
	}
	port, err := strconv.Atoi(fields[1])
	if err != nil {
		return servePIDInfo{}, fmt.Errorf("malformed serve.pid (%s): bad port %q: %w", path, fields[1], err)
	}
	info := servePIDInfo{PID: pid, Port: port}
	if len(fields) >= 3 {
		if t, err := time.Parse(time.RFC3339, fields[2]); err == nil {
			info.StartedAt = t
		}
	}
	return info, nil
}

// removeServePID deletes <userhome>/serve.pid. Best-effort: a missing file
// is not an error — the common case (stop/status already removed it, or
// serve exited before ever binding).
func removeServePID() error {
	path, err := servePIDPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove serve.pid: %w", err)
	}
	return nil
}

// processCommandLine shells out to `ps -o command= -p <pid>` — one call
// that answers both "is this pid alive" (ps exits non-zero for a
// nonexistent pid) and "what is it" (the command string), tolerant of both
// darwin and linux `ps` (both accept -o command= -p <pid>; "command="
// gives the full argv on both, which the "cortex" substring check below
// needs).
func processCommandLine(pid int) (string, bool) {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// isLiveCortexServe reports whether pid is a currently-running process
// whose command line contains "cortex" — the stale-pid check design item 1
// asks for: a pid file left behind by a crashed serve, later recycled by
// the OS to an unrelated process, must never be trusted enough to signal.
// A pid that's simply gone (ps -p fails) also reports false — the ordinary
// "already stopped" case.
func isLiveCortexServe(pid int) bool {
	cmd, alive := processCommandLine(pid)
	if !alive {
		return false
	}
	return strings.Contains(cmd, "cortex")
}
