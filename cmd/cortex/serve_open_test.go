package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/userhome"
)

// serveURL is the ready-to-open page URL — plain, no token query string
// (2026-07-19: the bearer-token model was replaced by a Host/Origin
// allowlist, see SECURITY.md and serve.go's hostOriginMiddleware).
func TestServeURLIsPlainNoTokenQueryString(t *testing.T) {
	got := serveURL("127.0.0.1:7433")
	want := "http://127.0.0.1:7433/"
	if got != want {
		t.Errorf("serveURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "token") {
		t.Errorf("serveURL = %q should carry no token query string", got)
	}
}

// --open is the default; --no-open suppresses it (headless, scripts, CI).
func TestServeOpenFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"default opens", []string{}, true},
		{"port only still opens", []string{"--port", "9000"}, true},
		{"no-open suppresses", []string{"--no-open"}, false},
		{"no-open with port", []string{"--port", "9000", "--no-open"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveOpenFromArgs(tc.args); got != tc.want {
				t.Errorf("serveOpenFromArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// cleanupLegacyServeToken removes a leftover serve.token file from the old
// bearer-token model on startup (item 2 of the 2026-07-19 auth swap) — a
// stale on-disk secret shouldn't linger once nothing checks it.
func TestCleanupLegacyServeTokenRemovesExistingFile(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	path, err := userhome.Path("serve.token")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("old-token"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cleanupLegacyServeToken()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("serve.token should be removed, Stat err = %v", err)
	}
}

// A missing serve.token (the common case, post-2026-07-19) must be a
// silent no-op — no error, no panic.
func TestCleanupLegacyServeTokenNoopWhenAbsent(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	cleanupLegacyServeToken() // must not panic
}

func TestMaybeOpenBrowser(t *testing.T) {
	var opened []string
	orig := openBrowser
	openBrowser = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	defer func() { openBrowser = orig }()

	maybeOpenBrowser(false, "http://127.0.0.1:7433/")
	if len(opened) != 0 {
		t.Fatalf("open=false must not launch a browser, got %v", opened)
	}
	maybeOpenBrowser(true, "http://127.0.0.1:7433/")
	if len(opened) != 1 || !strings.Contains(opened[0], "127.0.0.1:7433") {
		t.Fatalf("open=true should launch the page URL exactly once, got %v", opened)
	}
}
