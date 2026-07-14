package main

import (
	"os"
	"strings"
	"testing"
)

// resolveServeToken reuses the stored token across restarts (a bookmarked
// tokened URL keeps working) and only mints a fresh one when none exists or
// the stored value is unusable.
func TestResolveServeTokenReusesExisting(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	first, path, err := resolveServeToken()
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("token should be 32 random bytes hex-encoded, got %d chars", len(first))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file should exist: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
	}

	second, _, err := resolveServeToken()
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if second != first {
		t.Errorf("restart must reuse the stored token: first %q, second %q", first, second)
	}
}

func TestResolveServeTokenRegeneratesUnusable(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	// Seed a corrupt token file (too short / not hex).
	if _, path, err := resolveServeToken(); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(path, []byte("not-a-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := resolveServeToken()
	if err != nil {
		t.Fatalf("resolve after corruption: %v", err)
	}
	if len(tok) != 64 || tok == "not-a-token" {
		t.Errorf("unusable stored token must be replaced, got %q", tok)
	}
}

func TestServeURLCarriesToken(t *testing.T) {
	got := serveURL("127.0.0.1:7433", "abc123")
	want := "http://127.0.0.1:7433/?token=abc123"
	if got != want {
		t.Errorf("serveURL = %q, want %q", got, want)
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

func TestMaybeOpenBrowser(t *testing.T) {
	var opened []string
	orig := openBrowser
	openBrowser = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	defer func() { openBrowser = orig }()

	maybeOpenBrowser(false, "http://127.0.0.1:7433/?token=x")
	if len(opened) != 0 {
		t.Fatalf("open=false must not launch a browser, got %v", opened)
	}
	maybeOpenBrowser(true, "http://127.0.0.1:7433/?token=x")
	if len(opened) != 1 || !strings.Contains(opened[0], "token=x") {
		t.Fatalf("open=true should launch the tokened URL exactly once, got %v", opened)
	}
}
