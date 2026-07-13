// serve.go — `cortex serve` (Phase 4 / M4.1): a foreground HTTP/SSE adapter,
// dispatched the same way study/turn/project/scan/change/discord are (see
// main.go). Loopback-only, bearer-token-authenticated. This increment lands
// the listener + auth middleware only; the real endpoint surface (projects,
// sessions, turn, SSE, landscape, models) is M4.2.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// list, defaulting to defaultServePort when absent or malformed.
func servePortFromArgs(args []string) int {
	for i, a := range args {
		if a == "--port" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				return n
			}
		}
	}
	return defaultServePort
}

// newServeServer constructs the *http.Server and its real, already-bound
// listener for `addr` (e.g. "127.0.0.1:7433" or "127.0.0.1:0" in tests).
// Binding happens here (not inside (*http.Server).ListenAndServe) so tests
// can assert loopback-ness against the real listener address before ever
// starting to Serve.
func newServeServer(addr string, handler http.Handler) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	srv := &http.Server{Handler: handler}
	return srv, ln, nil
}

// generateServeToken returns a fresh random bearer token (hex-encoded), a
// new one each call — `cortex serve` mints one per foreground run rather
// than reusing a stored value.
func generateServeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate serve token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// writeServeToken writes token to <userhome>/serve.token, mode 0600
// (user-only — same posture as configwrite.go's writeJSONDoc), creating the
// user-home directory if needed. Returns the path written.
func writeServeToken(token string) (string, error) {
	path, err := userhome.Path("serve.token")
	if err != nil {
		return "", fmt.Errorf("failed to resolve serve.token path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("failed to create user home directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("failed to write serve.token: %w", err)
	}
	return path, nil
}

// authMiddleware rejects any request whose Authorization header is not
// exactly "Bearer <token>" with 401, before delegating to next. Every
// `/api/...` endpoint runs behind this (GOAL.md §3 P4: "Token auth on every
// endpoint" — read as every DATA endpoint). Paths outside "/api/" (the
// static UI shell: "/", "/index.html", "/app.js", "/app.css") are exempt —
// M5.3b's Decisions Log: a plain browser navigation can never attach a
// custom Authorization header, so gating the shell itself would make the
// web UI unreachable by any normal browser action. The shell carries no
// sensitive data (structure only); every byte of real project/session data
// still flows exclusively through the gated "/api/..." surface.
func authMiddleware(token string, next http.Handler) http.Handler {
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
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
// .../disable, the single-field-toggle pair.
func newServeMux(reg registry.Registry, mgr *SessionManager, configPath, homeDir string, loopsStore loops.Store) *http.ServeMux {
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
	mux.HandleFunc("GET /api/models", handleModels(configPath))
	mux.HandleFunc("PUT /api/models/{role}", handleSetModelBinding(configPath, reg, mgr))
	mux.HandleFunc("GET /api/loops", handleListLoops(loopsStore))
	mux.HandleFunc("POST /api/loops", handleCreateLoop(loopsStore))
	mux.HandleFunc("POST /api/loops/{name}/enable", handleSetLoopEnabled(loopsStore, true))
	mux.HandleFunc("POST /api/loops/{name}/disable", handleSetLoopEnabled(loopsStore, false))
	return mux
}

// runServeCLI is the `cortex serve` entry point (dispatched from main.go),
// following runScanCLI/runProjectCLI's established convention: this
// os.Exit-driving wrapper itself is untested; the pure functions it
// composes (servePortFromArgs, newServeServer, generateServeToken,
// writeServeToken, authMiddleware, newServeMux) carry the coverage.
func runServeCLI(args []string) {
	port := servePortFromArgs(args)
	token, err := generateServeToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tokenPath, err := writeServeToken(token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	reg, err := registry.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mgr := NewSessionManager(reg, newProductionSession)
	mgr.SetIdleTimeout(defaultSessionIdleTimeout)

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

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv, ln, err := newServeServer(addr, authMiddleware(token, newServeMux(reg, mgr, userConfigPath(), homeDir, loopsStore)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("cortex serve listening on http://%s (token: %s)\n", ln.Addr(), tokenPath)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
