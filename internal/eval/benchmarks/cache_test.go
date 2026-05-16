package benchmarks

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCacheDir_HonorsXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-test")
	got, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	want := filepath.Join("/tmp/xdg-test", "cortex", "benchmarks")
	if got != want {
		t.Errorf("CacheDir=%q want %q", got, want)
	}
}

func TestCacheDir_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/tmp/home-test")
	got, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	want := filepath.Join("/tmp/home-test", ".cortex", "benchmarks")
	if got != want {
		t.Errorf("CacheDir=%q want %q", got, want)
	}
}

func TestEnsureCached_FetchesOnFirstCall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	body := "hello world"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	path, err := EnsureCached("dummy", "subdir/file.txt", srv.URL)
	if err != nil {
		t.Fatalf("EnsureCached: %v", err)
	}

	wantPath := filepath.Join(tmp, "cortex", "benchmarks", "dummy", "subdir", "file.txt")
	if path != wantPath {
		t.Errorf("path=%q want %q", path, wantPath)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Errorf("content=%q want %q", got, body)
	}
}

func TestEnsureCached_IdempotentSecondCallDoesNotRefetch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, "v1")
	}))
	defer srv.Close()

	if _, err := EnsureCached("dummy", "f.txt", srv.URL); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := EnsureCached("dummy", "f.txt", srv.URL); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits=%d want 1 (second call should hit cache)", got)
	}
}

func TestEnsureCached_AtomicWrite_NoPartialFileOnFailure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	// Server that closes connection mid-stream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10000")
		w.WriteHeader(http.StatusOK)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("hijacker unavailable")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		// Write a header partial then forcibly close so the body is
		// truncated.
		conn.Write([]byte("partial"))
		conn.Close()
	}))
	defer srv.Close()

	dest := filepath.Join(tmp, "cortex", "benchmarks", "dummy", "f.txt")
	// First call may succeed-with-truncated body OR fail; either way
	// we assert that there's no partial file at the target path with the
	// wrong size sitting around to mask a future legitimate fetch.
	_, err := EnsureCached("dummy", "f.txt", srv.URL)
	_ = err // tolerate either outcome
	if info, statErr := os.Stat(dest); statErr == nil {
		// File at destination must NOT be a partial mid-stream remnant
		// — temp file should have been deleted on error.
		if info.Size() == 7 { // "partial"
			t.Errorf("partial truncated file %q (size %d) left at destination; atomic-write contract violated",
				dest, info.Size())
		}
	}

	// And in all cases the directory should not have a *.tmp-* leftover.
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %q in cache dir; atomic-write cleanup missed", e.Name())
		}
	}
}

func TestEnsureCached_HTTP404Returns_Error(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	_, err := EnsureCached("dummy", "missing.txt", srv.URL)
	if err == nil {
		t.Fatal("EnsureCached: want error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "status") {
		t.Errorf("err=%v should mention status", err)
	}
}

func TestEnsureCached_RejectsEmptyInputs(t *testing.T) {
	tests := []struct {
		name      string
		benchmark string
		relPath   string
		url       string
	}{
		{"empty benchmark", "", "f.txt", "http://x"},
		{"empty relPath", "dummy", "", "http://x"},
		{"empty url", "dummy", "f.txt", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EnsureCached(tc.benchmark, tc.relPath, tc.url)
			if err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

// errReader simulates a body read that fails partway through, used
// indirectly via SetHTTPClient + roundTripper.
type errReader struct{ readsBeforeErr int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.readsBeforeErr <= 0 {
		return 0, errors.New("synthetic read failure")
	}
	e.readsBeforeErr--
	n := copy(p, []byte("x"))
	return n, nil
}
func (e *errReader) Close() error { return nil }

type stubTransport struct{ resp *http.Response }

func (s *stubTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return s.resp, nil
}

func TestEnsureCached_BodyReadFailureLeavesNoFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	SetHTTPClient(&http.Client{Transport: &stubTransport{resp: &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(&errReader{readsBeforeErr: 0}),
		Header:     http.Header{},
	}}})
	t.Cleanup(func() { SetHTTPClient(nil) })

	dest := filepath.Join(tmp, "cortex", "benchmarks", "dummy", "err.txt")
	_, err := EnsureCached("dummy", "err.txt", "http://stub")
	if err == nil {
		t.Fatal("EnsureCached: want error on body read failure")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("destination file exists after read failure (err=%v); want non-existent", statErr)
	}
}
