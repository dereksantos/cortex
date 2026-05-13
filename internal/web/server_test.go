package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "cortex-web-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	cfg := &config.Config{ContextDir: tempDir, ProjectID: "test"}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(cfg, store, 0)
	return srv, tempDir
}

// TestServer_GeneratesTokenAtConstruction asserts the Server materializes
// a token on New() — without one, the API endpoints would either be
// open or unusable. The token must also be persisted to disk so other
// trusted processes (CLI, scripts) can read it without re-deriving.
func TestServer_GeneratesTokenAtConstruction(t *testing.T) {
	srv, tempDir := newTestServer(t)
	if len(srv.Token) < 32 {
		t.Fatalf("expected a non-trivial token, got %q (len=%d)", srv.Token, len(srv.Token))
	}

	tokenFile := filepath.Join(tempDir, "dashboard.token")
	st, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	if mode := st.Mode().Perm(); mode&0077 != 0 {
		t.Errorf("token file perms %v are group/world-readable", mode)
	}
	data, _ := os.ReadFile(tokenFile)
	if strings.TrimSpace(string(data)) != srv.Token {
		t.Errorf("token file content does not match Server.Token")
	}
}

// TestServer_APIRequiresToken asserts that API endpoints reject requests
// with no token, a wrong token, or a stale token, and accept requests
// bearing the right token via either the Authorization header or the
// ?token= query parameter (the SSE path needs the query form since
// EventSource cannot set headers).
func TestServer_APIRequiresToken(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv.httpServer.Handler)
	defer ts.Close()

	cases := []struct {
		name       string
		path       string
		header     string
		query      string
		wantStatus int
	}{
		{"state without token → 401", "/api/state", "", "", http.StatusUnauthorized},
		{"state with wrong token → 401", "/api/state", "Bearer wrong", "", http.StatusUnauthorized},
		{"state with correct header → 200", "/api/state", "Bearer " + srv.Token, "", http.StatusOK},
		{"state with correct query → 200", "/api/state", "", "token=" + srv.Token, http.StatusOK},
		{"projects without token → 401", "/api/projects", "", "", http.StatusUnauthorized},
		{"projects with correct token → 200", "/api/projects", "Bearer " + srv.Token, "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := ts.URL + tc.path
			if tc.query != "" {
				url += "?" + tc.query
			}
			req, _ := http.NewRequest("GET", url, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("got status %d, want %d (body=%s)", resp.StatusCode, tc.wantStatus, body)
			}
		})
	}
}

// TestServer_DashboardEmbedsToken asserts the / route injects the token
// into the served HTML so the in-page JS can authenticate its SSE
// connection without an out-of-band channel. Without this injection,
// requiring auth on /api/events would prevent the dashboard from ever
// loading data.
func TestServer_DashboardEmbedsToken(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv.httpServer.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), srv.Token) {
		t.Errorf("token %q not embedded in dashboard HTML", srv.Token)
	}
	// The dashboard's JS expects window.__CORTEX_TOKEN__.
	if !strings.Contains(string(body), "__CORTEX_TOKEN__") {
		t.Errorf("expected '__CORTEX_TOKEN__' global hook in HTML; got body without it")
	}
}
