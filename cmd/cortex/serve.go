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

	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/userhome"
)

// defaultServePort is the default `cortex serve` bind port (flag-overridable
// via --port). See GOAL.md §6 M4.1 / docs/cortex-web.md D7.
const defaultServePort = 7433

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
// serve endpoint runs behind this (GOAL.md §3 P4: "Token auth on every
// endpoint").
func authMiddleware(token string, next http.Handler) http.Handler {
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
// serve_session.go). M4.2b2 (turn/SSE) and M4.2c (landscape/models) extend
// this mux further.
func newServeMux(reg registry.Registry, mgr *SessionManager) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/projects", handleListProjects(reg))
	mux.HandleFunc("GET /api/projects/{name}/sessions", handleListProjectSessions(reg))
	mux.HandleFunc("POST /api/projects/{name}/sessions", handleCreateSession(mgr))
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

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv, ln, err := newServeServer(addr, authMiddleware(token, newServeMux(reg, mgr)))
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
