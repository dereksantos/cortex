// serve.go — `cortex serve` (Phase 4 / M4.1): a foreground HTTP/SSE adapter,
// dispatched the same way study/turn/project/scan/change/discord are (see
// main.go). Loopback-only. This increment lands the listener + auth
// middleware only; the real endpoint surface (projects, sessions, turn,
// SSE, landscape, models) is M4.2.
//
// 2026-07-19: the bearer-token model (D7, docs/cortex-web.md) was replaced
// by a strict Host/Origin allowlist — see hostOriginMiddleware below and
// SECURITY.md's posture note. There is no longer a shared secret to mint,
// store, or carry in the URL.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/userhome"
)

// defaultServePort is the default `cortex serve` bind port (flag-overridable
// via --port). See GOAL.md §6 M4.1 / docs/cortex-web.md D7.
const defaultServePort = 7433

// defaultSessionIdleTimeout is how long a live session may go untouched
// (no Create/Resume/turn) before SessionManager evicts it from memory
// (GOAL.md §6 M4.7); the next request against that id transparently
// re-hydrates from the on-disk transcript, same as a restart (M4.6).
const defaultSessionIdleTimeout = 30 * time.Minute

// servePortFromArgs parses `--port <n>` out of a `cortex serve` argument
// list, defaulting to def (defaultServePort, or serve.port when configured)
// when absent or malformed. `--port` always wins over config when both are
// given — an explicit flag is the more specific, more recent intent.
func servePortFromArgs(args []string, def int) int {
	for i, a := range args {
		if a == "--port" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				return n
			}
		}
	}
	return def
}

// newServeServer constructs the *http.Server and its real, already-bound
// listener for `addr` (e.g. "127.0.0.1:7433" or "127.0.0.1:0" in tests).
// Binding happens here (not inside (*http.Server).ListenAndServe) so tests
// can assert loopback-ness against the real listener address before ever
// starting to Serve. The handler itself is built by buildHandler, called
// with the ACTUAL bound port (not the requested one — "127.0.0.1:0"
// resolves to whatever the OS picks) so hostOriginMiddleware's allowlist
// always matches this bind, `--port` included.
func newServeServer(addr string, buildHandler func(port string) http.Handler) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("failed to parse bound address %s: %w", ln.Addr(), err)
	}
	srv := &http.Server{Handler: buildHandler(port)}
	return srv, ln, nil
}

// cleanupLegacyServeToken removes a leftover <userhome>/serve.token file
// from the pre-2026-07-19 bearer-token model (SECURITY.md) so a stale
// on-disk secret doesn't linger now that nothing checks it. Best-effort and
// silent on the common case (no file); any other removal failure is logged
// but not fatal — it's cosmetic cleanup, not a precondition to serving.
func cleanupLegacyServeToken() {
	path, err := userhome.Path("serve.token")
	if err != nil {
		return
	}
	if err := os.Remove(path); err == nil {
		fmt.Fprintf(os.Stderr, "removed legacy serve.token (%s) — auth is now a Host/Origin allowlist, no bearer token\n", path)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "could not remove legacy serve.token (%s): %v\n", path, err)
	}
}

// serveURL is the ready-to-open page URL — plain, no query-string secret:
// the 2026-07-19 Host/Origin allowlist (hostOriginMiddleware below) needs
// nothing carried in the URL. What the startup line prints and
// maybeOpenBrowser launches.
func serveURL(addr string) string {
	return fmt.Sprintf("http://%s/", addr)
}

// serveOpenFromArgs parses the open-browser preference from a
// `cortex serve` argument list: opening the page is the DEFAULT; --no-open
// suppresses it for headless and scripted runs.
func serveOpenFromArgs(args []string) bool {
	for _, a := range args {
		if a == "--no-open" {
			return false
		}
	}
	return true
}

// openBrowser launches the platform browser opener, injectable for tests.
// Fire-and-forget: Start, not Run — serve must not block on the browser.
var openBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("no browser opener for %s", runtime.GOOS)
	}
	return cmd.Start()
}

// maybeOpenBrowser opens url when open is set; a failed launch is a hint,
// not a fatal — the URL is printed either way.
func maybeOpenBrowser(open bool, url string) {
	if !open {
		return
	}
	if err := openBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "could not open a browser (%v) — open %s yourself\n", err, url)
	}
}

// allowedHostSet returns the loopback Host/Origin values this bind
// accepts: localhost, 127.0.0.1, and [::1], each paired with the ACTUAL
// bound port (see newServeServer) — not the defaultServePort constant — so
// `--port` keeps working.
func allowedHostSet(port string) map[string]bool {
	return map[string]bool{
		"localhost:" + port: true,
		"127.0.0.1:" + port: true,
		"[::1]:" + port:     true,
	}
}

// hostOriginMiddleware is the 2026-07-19 replacement for the old
// bearer-token authMiddleware (SECURITY.md's posture note): a strict
// Host/Origin allowlist gates every "/api/..." request instead of a shared
// secret, so there is nothing to mint, store, or leak via a URL.
//
// r.Host must be exactly one of localhost:<port>, 127.0.0.1:<port>, or
// [::1]:<port>, where <port> is this bind's real listener port. This alone
// defeats DNS rebinding: an attacker page can get a victim's browser to
// resolve some other domain to 127.0.0.1, but the browser still sends
// "Host: that-domain:<port>", which never matches the allowlist — browsers
// set the Host header from the connection target and scripts cannot
// override it.
//
// When an Origin header is present — browsers attach one to every
// cross-origin fetch/XHR and cross-origin form POST, including the
// text/plain-body trick sites use to dodge CORS preflight — it must ALSO
// parse to a host in the same allowlist, or the request is rejected. This
// is what stops cross-site POSTs a Host check alone would miss. Requests
// with NO Origin header (curl, scripts, same-process tooling) pass this
// leg — the Host check above still gates them, which is what keeps the API
// scriptable without ceremony.
//
// Paths outside "/api/" (the static UI shell) are exempt, the same
// carve-out authMiddleware used: gating "/" would make the shell
// unreachable by a plain browser navigation, and the shell itself carries
// no sensitive data — only "/api/..." touches real project/session data.
func hostOriginMiddleware(port string, next http.Handler) http.Handler {
	allowed := allowedHostSet(port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !allowed[r.Host] {
			http.Error(w, "forbidden: untrusted Host header", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !allowed[u.Host] {
				http.Error(w, "forbidden: untrusted Origin header", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// newServeMux builds the route table. M4.1 landed a single health route
// solely to exercise the auth middleware end-to-end; M4.2a added the
// read-only project + session listing surface (reg is the small Registry
// interface, so this is httptest-testable with a fake — no model needed).
// M4.2b1 adds session create/resume (mgr is the SessionManager, itself
// httptest-testable via an injected hermetic sessionFactory — see
// serve_session.go). M4.2b2 adds the turn handler (serve_turn.go); M4.2b3
// adds the SSE turn/stream handler (serve_stream.go); M4.2c1 adds the
// landscape endpoint (configPath/homeDir — the same two inputs
// resolveScanRoots/buildScanReport already take at the CLI level,
// serve_landscape.go); M4.2c2a adds the read-only models endpoint
// (serve_models.go); M4.2c2b1 adds the file-backed scoped write
// (PUT /api/models/{role}?scope=user|project); M4.2c2b2 adds
// scope=session&session=<id>, the in-memory-only half — mgr is already
// threaded in for the turn/SSE routes, so no signature change was needed.
// M5.1 adds "/" serving the embedded web UI assets (webui.go) — registered
// first so the more specific "/api/..." patterns above still win (Go 1.22+
// ServeMux precedence is longest-match, not registration order, but keeping
// the catch-all visually first reads as "fallback" here). M5.3b adds
// GET /api/dashboard (serve_dashboard.go), the dashboard screen's endpoint.
// M5.3c1 adds GET /api/projects/{name}/sessions/{id} (serve_transcript.go),
// the session screen's transcript endpoint — distinct from the plain listing
// route above (an extra path segment, so ServeMux resolves them unambiguously).
// M6.7b adds GET /api/loops (serve_loops.go), wiring M6.7a's
// buildLoopsViewModel into the HTTP surface — loopsStore is a small
// loops.Store interface, matching the reg/mgr precedent of threading
// hermetically-fakeable seams rather than resolving internal/userhome
// inside the handler. M6.7c adds POST /api/loops (create), the write half,
// over the same loopsStore. M6.7d adds POST /api/loops/{name}/enable and
// .../disable, the single-field-toggle pair. M6.7e adds
// POST /api/loops/{name}/run-now, reusing mgr.newSession as the
// sessionFactory RunLoopFiring (loop_run.go, M6.3) needs — no new
// newServeMux parameter, same reasoning as M4.2c2b2 above. docs/
// cross-source-learning.md piece 4 adds GET /api/memory (list, tier-tagged),
// GET /api/memory/note (one note's full body), and DELETE /api/memory/note
// (the human correction loop routed to the real Store.Forget path) —
// serve_memory.go, backed by webui_memory.go's buildMemoryViewModel; reg is
// already threaded in for the project lookup, same as every other
// project-scoped route above.
func newServeMux(reg registry.Registry, mgr *SessionManager, configPath, homeDir string, loopsStore loops.Store, running *runningSet) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/", webUIHandler())
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/dashboard", handleDashboard(reg))
	mux.HandleFunc("GET /api/projects", handleListProjects(reg))
	mux.HandleFunc("GET /api/projects/{name}/sessions", handleListProjectSessions(reg))
	mux.HandleFunc("POST /api/projects/{name}/sessions", handleCreateSession(mgr))
	mux.HandleFunc("GET /api/projects/{name}/sessions/{id}", handleGetSession(reg))
	mux.HandleFunc("POST /api/projects/{name}/sessions/{id}/turn", handleTurn(mgr))
	mux.HandleFunc("POST /api/projects/{name}/sessions/{id}/turn/stream", handleTurnStream(mgr))
	mux.HandleFunc("GET /api/landscape", handleLandscape(configPath, homeDir))
	mux.HandleFunc("POST /api/landscape/rescan", handleLandscapeRescan(configPath, homeDir))
	mux.HandleFunc("GET /api/models", handleModels(configPath))
	mux.HandleFunc("PUT /api/models/{role}", handleSetModelBinding(configPath, reg, mgr))
	mux.HandleFunc("GET /api/loops", handleListLoops(loopsStore))
	mux.HandleFunc("POST /api/loops", handleCreateLoop(loopsStore))
	mux.HandleFunc("POST /api/loops/{name}/enable", handleSetLoopEnabled(loopsStore, true))
	mux.HandleFunc("POST /api/loops/{name}/disable", handleSetLoopEnabled(loopsStore, false))
	mux.HandleFunc("POST /api/loops/{name}/run-now", handleRunLoop(loopsStore, reg, mgr.newSession, running))
	mux.HandleFunc("GET /api/memory", handleListMemory(reg))
	mux.HandleFunc("GET /api/memory/note", handleGetMemoryNote(reg))
	mux.HandleFunc("DELETE /api/memory/note", handleDeleteMemoryNote(reg))
	return mux
}

// runServeCLI is the `cortex serve` entry point (dispatched from main.go),
// following runScanCLI/runProjectCLI's established convention: this
// os.Exit-driving wrapper itself is untested; the pure functions it
// composes (servePortFromArgs, newServeServer, hostOriginMiddleware,
// newServeMux) carry the coverage. `cortex serve stop`/`status` (serve_stop.go)
// are handled first and return before touching config, the registry, or
// the network beyond serve.pid and (for status) a single request against
// an already-running instance.
func runServeCLI(args []string) {
	if len(args) > 0 && args[0] == "stop" {
		runServeStopCLI()
		return
	}
	if len(args) > 0 && args[0] == "status" {
		runServeStatusCLI()
		return
	}

	cfg := LoadConfig()
	port := servePortFromArgs(args, cfg.servePort(defaultServePort))
	loops.CadenceFloorMinutes = cfg.loopCadenceFloorMin()
	projectSessionsLimit = cfg.projectSessionsLimit()
	cleanupLegacyServeToken()
	reg, err := registry.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mgr := NewSessionManager(reg, newProductionSession)
	mgr.SetIdleTimeout(cfg.sessionIdleTimeout())

	loopsStore, err := loops.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// The overlap guard (runningSet) is constructed once here and shared
	// between the run-now HTTP handler (newServeMux → handleRunLoop) and the
	// tick scheduler (newLoopScheduler below) — a single source of truth for
	// "is this loop currently firing", so a UI-triggered run-now and a
	// scheduler tick can never fire the same loop concurrently either
	// direction.
	running := newRunningSet()

	// startedAt is recorded once here and threaded into BOTH serve.pid
	// (writeServePID below, read back by `cortex serve status`) and
	// GET /api/status (handleStatus) — the two surfaces derive uptime from
	// the same instant so they can never disagree.
	startedAt := time.Now()
	mux := newServeMux(reg, mgr, userConfigPath(), homeDir, loopsStore, running)
	mux.HandleFunc("GET /api/status", handleStatus(startedAt, mgr, loopsStore))

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv, ln, err := newServeServer(addr, func(boundPort string) http.Handler {
		return hostOriginMiddleware(boundPort, mux)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	_, boundPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	boundPortNum, err := strconv.Atoi(boundPort)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeServePID(os.Getpid(), boundPortNum, startedAt); err != nil {
		// Non-fatal: `cortex serve stop`/`status` degrade to "not found"
		// rather than "found but wrong", and serving itself doesn't depend
		// on this file existing.
		fmt.Fprintln(os.Stderr, "warning: failed to write serve.pid:", err)
	}

	// M6.8: the serve-resident scheduler (docs/cortex-web.md Phase 6 —
	// "Scheduler in the serve process, ticks while cortex serve runs").
	// mgr.newSession is the same sessionFactory seam M6.7e's run-now
	// handler already reuses — no new construction path. The tick
	// interval (1 minute) just needs to be finer than the 15-minute
	// cadence floor (internal/loops.CadenceFloorMinutes); it is not itself
	// part of the spec format. Stopped when runServeCLI returns (server
	// error, or a graceful SIGTERM/SIGINT-triggered stop — see drainServe,
	// serve_drain.go) so the scheduler never outlives the listener.
	//
	// The ticker/scheduler teardown must run before this process exits, so
	// the serving loop lives in a closure returning an exit code —
	// os.Exit does not run deferred calls.
	exitCode := func() int {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		schedCtx, stopSched := context.WithCancel(context.Background())
		sched := newLoopScheduler(loopsStore, reg, mgr.newSession, time.Now, running)
		schedStopped := sched.Start(schedCtx, ticker.C)

		pageURL := serveURL(ln.Addr().String())
		fmt.Printf("cortex serve listening — open %s\n(loopback Host/Origin allowlist; --no-open to suppress the browser; `cortex serve stop` to shut down)\n", pageURL)
		maybeOpenBrowser(serveOpenFromArgs(args), pageURL)

		// Graceful shutdown (design item 2): SIGTERM/SIGINT triggers
		// drainServe (serve_drain.go) — Shutdown + scheduler teardown — on
		// its own goroutine; srv.Serve runs on another. Whichever finishes
		// first decides the exit code; the teardown steps are safe to
		// re-run (see drainServe's doc comment) so there's no ordering
		// hazard between the two branches below.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(sigCh)

		serveErrCh := make(chan error, 1)
		go func() { serveErrCh <- srv.Serve(ln) }()

		drainErrCh := make(chan error, 1)
		go func() { drainErrCh <- drainServe(sigCh, srv, stopSched, schedStopped, sched, 10*time.Second) }()

		var result error
		select {
		case result = <-serveErrCh:
			// srv.Serve returned on its own (a real listen/accept error —
			// SIGTERM/SIGINT would surface here as http.ErrServerClosed via
			// the drain branch below, not this one). Tear the scheduler
			// down directly since drainServe's goroutine is still parked on
			// <-sigCh and will never get to it.
			stopSched()
			<-schedStopped
			sched.Wait()
		case drainErr := <-drainErrCh:
			// A signal fired: drainServe already ran the full teardown
			// (Shutdown + stopSched + Wait). srv.Serve(ln) is now returning
			// http.ErrServerClosed on its own goroutine — drain it so it
			// doesn't leak past this function returning.
			<-serveErrCh
			result = drainErr
		}
		removeServePID()
		if result != nil && result != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, result)
			return 1
		}
		return 0
	}()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
