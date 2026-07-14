// webui_test.go — M5.1: proves the embedded web UI assets serve correctly
// with no filesystem presence (the whole point of go:embed — the binary is
// self-contained; see GOAL.md §6 M5.1).
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebUIServesEmbeddedAssetsWithNoFilesystemPresence proves the web UI
// route is backed entirely by the compiled-in embed.FS: it changes the
// working directory to an unrelated temp dir (so an implementation reading
// cmd/cortex/webui/index.html off the real filesystem at request time would
// fail to find the source tree) and confirms both index.html and a static
// asset (app.js) still serve their real embedded content.
func TestWebUIServesEmbeddedAssetsWithNoFilesystemPresence(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
	defer ts.Close()

	t.Chdir(t.TempDir()) // no cmd/cortex/webui/ reachable under this cwd

	for _, tc := range []struct {
		path   string
		wantIn string
	}{
		{"/", "Cortex"},
		{"/index.html", "Cortex"},
		{"/app.js", "Cortex web UI"},
	} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+tc.path, nil)
		if err != nil {
			t.Fatalf("failed to build request for %s: %v", tc.path, err)
		}
		req.Header.Set("Authorization", "Bearer tok")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to GET %s: %v", tc.path, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: got status %d, want 200", tc.path, resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read body for %s: %v", tc.path, err)
		}
		if !strings.Contains(string(body), tc.wantIn) {
			t.Errorf("GET %s: body = %q, want substring %q", tc.path, body, tc.wantIn)
		}
	}
}
