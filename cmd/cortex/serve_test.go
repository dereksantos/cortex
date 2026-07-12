package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// M4.1 — `cortex serve`: loopback-only listener, generated bearer token
// written under the user home with mode 0600, and an auth middleware that
// rejects tokenless/wrong-token requests. See GOAL.md §6 M4.1.

func TestServePortFromArgsDefaultsTo7433(t *testing.T) {
	got := servePortFromArgs(nil)
	if got != defaultServePort {
		t.Errorf("servePortFromArgs(nil) = %d, want %d", got, defaultServePort)
	}
}

func TestServePortFromArgsFlagOverrides(t *testing.T) {
	got := servePortFromArgs([]string{"--port", "9090"})
	if got != 9090 {
		t.Errorf("servePortFromArgs(--port 9090) = %d, want 9090", got)
	}
}

func TestNewServeServerListenerIsLoopback(t *testing.T) {
	srv, ln, err := newServeServer("127.0.0.1:0", http.NewServeMux())
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

// TestNewServeServerSetsNoWriteTimeout is M4.5's other half (GOAL.md §6 /
// D6): the real *http.Server `cortex serve` constructs (runServeCLI calls
// newServeServer directly — not an httptest wrapper, which doesn't expose
// the configured *http.Server) must leave WriteTimeout unset (zero), or a
// long-lived SSE turn/stream response (M4.2b3) would be killed mid-stream.
func TestNewServeServerSetsNoWriteTimeout(t *testing.T) {
	srv, ln, err := newServeServer("127.0.0.1:0", http.NewServeMux())
	if err != nil {
		t.Fatalf("newServeServer: %v", err)
	}
	defer ln.Close()
	defer srv.Close()

	if srv.WriteTimeout != 0 {
		t.Errorf("http.Server WriteTimeout = %v, want 0 (unset) — a nonzero WriteTimeout would kill SSE streams mid-turn", srv.WriteTimeout)
	}
}

func TestGenerateServeTokenIsNonEmptyAndUnique(t *testing.T) {
	a, err := generateServeToken()
	if err != nil {
		t.Fatalf("generateServeToken: %v", err)
	}
	if len(a) < 32 {
		t.Errorf("generateServeToken() length = %d, want >= 32", len(a))
	}
	b, err := generateServeToken()
	if err != nil {
		t.Fatalf("generateServeToken: %v", err)
	}
	if a == b {
		t.Errorf("generateServeToken() returned the same value twice: %q", a)
	}
}

func TestWriteServeTokenModeAndContent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	path, err := writeServeToken("sekrit-token-value")
	if err != nil {
		t.Fatalf("writeServeToken: %v", err)
	}

	wantPath := filepath.Join(tmp, "serve.token")
	if path != wantPath {
		t.Errorf("writeServeToken path = %q, want %q", path, wantPath)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("serve.token mode = %o, want 0600", mode)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != "sekrit-token-value" {
		t.Errorf("serve.token content = %q, want %q", got, "sekrit-token-value")
	}
}

func TestServeAuthMiddlewareRejectsTokenlessAndWrongToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ts := httptest.NewServer(authMiddleware("expected-token", mux))
	defer ts.Close()

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token", http.StatusUnauthorized},
		{"malformed header", "expected-token", http.StatusUnauthorized},
		{"correct token", "Bearer expected-token", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

// TestServeAuthMiddlewareAllowsStaticAssetsWithoutToken pins the M5.3b
// carve-out: paths outside "/api/" (the UI shell — index.html/app.js/
// app.css) serve without a bearer token, while "/api/..." stays gated. A
// plain browser navigation can never attach a custom Authorization header,
// so requiring the token on "/" would make the web UI unreachable by any
// normal browser action (GOAL.md §6 M5.3b Decisions Log).
func TestServeAuthMiddlewareAllowsStaticAssetsWithoutToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("shell"))
	})
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ts := httptest.NewServer(authMiddleware("expected-token", mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("Get /: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / without token: status = %d, want 200", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("Get /api/health: %v", err)
	}
	defer resp2.Body.Close()
	io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/health without token: status = %d, want 401", resp2.StatusCode)
	}
}
