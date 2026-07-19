package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// M4.1 — `cortex serve`: loopback-only listener. 2026-07-19: the auth model
// changed from a generated bearer token to a strict Host/Origin allowlist
// (hostOriginMiddleware, serve.go) — see SECURITY.md's posture note. See
// GOAL.md §6 M4.1.

func TestServePortFromArgsDefaultsTo7433(t *testing.T) {
	got := servePortFromArgs(nil, defaultServePort)
	if got != defaultServePort {
		t.Errorf("servePortFromArgs(nil) = %d, want %d", got, defaultServePort)
	}
}

func TestServePortFromArgsFlagOverrides(t *testing.T) {
	got := servePortFromArgs([]string{"--port", "9090"}, defaultServePort)
	if got != 9090 {
		t.Errorf("servePortFromArgs(--port 9090) = %d, want 9090", got)
	}
}

func TestServePortFromArgsConfigDefaultOverridesWhenNoFlag(t *testing.T) {
	got := servePortFromArgs(nil, 8080)
	if got != 8080 {
		t.Errorf("servePortFromArgs(nil, 8080) = %d, want 8080", got)
	}
}

func TestServePortFromArgsFlagWinsOverConfigDefault(t *testing.T) {
	got := servePortFromArgs([]string{"--port", "9090"}, 8080)
	if got != 9090 {
		t.Errorf("servePortFromArgs(--port 9090, 8080) = %d, want 9090 (flag wins)", got)
	}
}

func TestNewServeServerListenerIsLoopback(t *testing.T) {
	srv, ln, err := newServeServer("127.0.0.1:0", func(string) http.Handler { return http.NewServeMux() })
	if err != nil {
		t.Fatalf("newServeServer: %v", err)
	}
	defer ln.Close()
	defer srv.Close()

	addrPort, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("ParseAddrPort(%q): %v", ln.Addr().String(), err)
	}
	if !addrPort.Addr().IsLoopback() {
		t.Errorf("listener address %s is not loopback", ln.Addr())
	}
}

// TestNewServeServerBuildsHandlerWithActualBoundPort pins the reason
// newServeServer takes a handler-builder func instead of a plain
// http.Handler: "127.0.0.1:0" resolves to whatever port the OS picks, and
// hostOriginMiddleware's allowlist has to match that REAL port (not the
// requested one) for the resulting server to accept any request at all.
func TestNewServeServerBuildsHandlerWithActualBoundPort(t *testing.T) {
	var gotPort string
	srv, ln, err := newServeServer("127.0.0.1:0", func(port string) http.Handler {
		gotPort = port
		return http.NewServeMux()
	})
	if err != nil {
		t.Fatalf("newServeServer: %v", err)
	}
	defer ln.Close()
	defer srv.Close()

	_, wantPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", ln.Addr(), err)
	}
	if gotPort != wantPort {
		t.Errorf("buildHandler saw port %q, want the actual bound port %q", gotPort, wantPort)
	}
}

// TestNewServeServerSetsNoWriteTimeout is M4.5's other half (GOAL.md §6 /
// D6): the real *http.Server `cortex serve` constructs (runServeCLI calls
// newServeServer directly — not an httptest wrapper, which doesn't expose
// the configured *http.Server) must leave WriteTimeout unset (zero), or a
// long-lived SSE turn/stream response (M4.2b3) would be killed mid-stream.
func TestNewServeServerSetsNoWriteTimeout(t *testing.T) {
	srv, ln, err := newServeServer("127.0.0.1:0", func(string) http.Handler { return http.NewServeMux() })
	if err != nil {
		t.Fatalf("newServeServer: %v", err)
	}
	defer ln.Close()
	defer srv.Close()

	if srv.WriteTimeout != 0 {
		t.Errorf("http.Server WriteTimeout = %v, want 0 (unset) — a nonzero WriteTimeout would kill SSE streams mid-turn", srv.WriteTimeout)
	}
}

// TestHostOriginMiddlewareTable is the core coverage for the 2026-07-19
// auth model (Derek's decision, SECURITY.md): a strict Host/Origin
// allowlist gates "/api/..." instead of a bearer token. Table-driven per
// GOAL.md's testing convention.
func TestHostOriginMiddlewareTable(t *testing.T) {
	const port = "7433"

	tests := []struct {
		name       string
		host       string
		origin     string // "" means no Origin header at all
		wantStatus int
	}{
		{"good host localhost, no origin", "localhost:" + port, "", http.StatusOK},
		{"good host 127.0.0.1, no origin", "127.0.0.1:" + port, "", http.StatusOK},
		{"good host [::1], no origin", "[::1]:" + port, "", http.StatusOK},
		{"foreign host rejected (rebinding)", "attacker.example:" + port, "", http.StatusForbidden},
		{"right hostname wrong port rejected", "localhost:9999", "", http.StatusForbidden},
		{"good host, same-origin Origin passes", "localhost:" + port, "http://localhost:" + port, http.StatusOK},
		{"good host, foreign Origin rejected (CSRF)", "localhost:" + port, "https://attacker.example", http.StatusForbidden},
		{"good host, foreign Origin rejected even though Host is fine", "127.0.0.1:" + port, "http://attacker.example:" + port, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
			})
			handler := hostOriginMiddleware(port, mux)

			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (host=%q origin=%q)", rec.Code, tt.wantStatus, tt.host, tt.origin)
			}
			if tt.wantStatus == http.StatusForbidden {
				body := rec.Body.String()
				if body == "" {
					t.Error("a 403 should carry a plain-text reason, got empty body")
				}
			}
		})
	}
}

// TestHostOriginMiddlewareAllowsStaticAssetsWithoutHostCheck pins the
// M5.3b carve-out this middleware kept from authMiddleware: paths outside
// "/api/" (the UI shell — index.html/app.js/app.css) serve regardless of
// Host/Origin, while "/api/..." stays gated. A plain browser navigation
// can attach any Host it was given by DNS, so gating "/" would risk making
// the web UI unreachable; the shell carries no sensitive data.
func TestHostOriginMiddlewareAllowsStaticAssetsWithoutHostCheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("shell"))
	})
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ts := httptest.NewServer(hostOriginMiddleware("7433", mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("Get /: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / with a foreign bound port: status = %d, want 200", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("Get /api/health: %v", err)
	}
	defer resp2.Body.Close()
	io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("GET /api/health with a Host the allowlist doesn't recognize: status = %d, want 403", resp2.StatusCode)
	}
}
