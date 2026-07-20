// serve_stop.go — `cortex serve stop` and `cortex serve status` (design
// items 3-4), dispatched from runServeCLI (serve.go) before it does
// anything else, so neither reads config or touches the network beyond the
// pid file and (for status) a single GET /api/status against the already-
// running instance.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"
)

// serveStopTimeout/serveStopPollInterval bound how long `cortex serve stop`
// waits for the signaled process to actually exit before giving up —
// design item 3 is explicit that stop must NOT escalate to SIGKILL
// automatically; it prints the kill -9 hint instead.
const (
	serveStopTimeout      = 10 * time.Second
	serveStopPollInterval = 200 * time.Millisecond
)

// runServeStopCLI implements `cortex serve stop`: read the pid file,
// verify it's actually a live cortex process (serve_pid.go's
// isLiveCortexServe — a stale/recycled pid is never trusted enough to
// signal), SIGTERM it, and poll for exit.
func runServeStopCLI() {
	info, err := readServePID()
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "cortex serve is not running")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "failed to read serve.pid:", err)
		os.Exit(1)
	}

	if !isLiveCortexServe(info.PID) {
		_ = removeServePID()
		fmt.Fprintln(os.Stderr, "cortex serve is not running")
		os.Exit(1)
	}

	proc, err := os.FindProcess(info.PID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to locate process", info.PID, ":", err)
		os.Exit(1)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintln(os.Stderr, "failed to signal cortex serve (pid", info.PID, "):", err)
		os.Exit(1)
	}

	if waitForProcessExit(info.PID, serveStopTimeout, serveStopPollInterval) {
		fmt.Printf("stopped cortex serve (pid %d)\n", info.PID)
		return
	}
	fmt.Fprintf(os.Stderr, "cortex serve (pid %d) is still running after %s — it did not exit gracefully; if you're sure it's wedged: kill -9 %d\n", info.PID, serveStopTimeout, info.PID)
	os.Exit(1)
}

// waitForProcessExit polls isLiveCortexServe every interval until pid is no
// longer a live cortex process, or timeout elapses, reporting which
// happened. Extracted so tests can drive it with tiny interval/timeout
// values rather than the 10s production default.
func waitForProcessExit(pid int, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !isLiveCortexServe(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// runServeStatusCLI implements `cortex serve status`: report pid/port/
// uptime from serve.pid, then try GET /api/status for the live view
// (sessions + loops). The API being unreachable while the process is
// alive is a normal, expected state (design item 4) — it still exits 0.
func runServeStatusCLI() {
	info, err := readServePID()
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "cortex serve is not running")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "failed to read serve.pid:", err)
		os.Exit(1)
	}

	if !isLiveCortexServe(info.PID) {
		_ = removeServePID()
		fmt.Fprintln(os.Stderr, "cortex serve is not running")
		os.Exit(1)
	}

	uptime := "unknown"
	if !info.StartedAt.IsZero() {
		uptime = time.Since(info.StartedAt).Round(time.Second).String()
	}
	fmt.Printf("cortex serve is running (pid %d, port %d, uptime %s)\n", info.PID, info.Port, uptime)

	status, err := fetchServeStatus(info.Port)
	if err != nil {
		fmt.Println("process running; API not responding")
		return
	}

	fmt.Printf("live sessions: %d\n", status.LiveSessions)
	if len(status.Loops) == 0 {
		fmt.Println("loops: (none registered)")
		return
	}
	for _, l := range status.Loops {
		next := l.NextRun
		if next == "" {
			next = "-"
		}
		fmt.Printf("  %-24s enabled=%-5v next=%s\n", l.Name, l.Enabled, next)
	}
}

// fetchServeStatus calls GET /api/status on a running cortex serve bound
// to port. The request URL targets 127.0.0.1, so Go's http.Client sends
// "Host: 127.0.0.1:<port>" by default — exactly what hostOriginMiddleware's
// allowlist (serve.go) requires, with no explicit header override needed.
func fetchServeStatus(port int) (statusResponse, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/status", port)
	resp, err := client.Get(url)
	if err != nil {
		return statusResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusResponse{}, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var status statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return statusResponse{}, fmt.Errorf("decode /api/status response: %w", err)
	}
	return status, nil
}
