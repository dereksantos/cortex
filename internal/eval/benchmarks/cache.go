package benchmarks

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheDir returns the per-user benchmark cache root. Honors
// XDG_CACHE_HOME if set; otherwise falls back to ~/.cortex/benchmarks.
// Returns the resolved absolute path and creates parent directories on
// the way through.
func CacheDir() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "cortex", "benchmarks"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".cortex", "benchmarks"), nil
}

// httpClient is overridable by tests via SetHTTPClient.
var (
	httpMu     sync.RWMutex
	httpClient = &http.Client{Timeout: 60 * time.Second}
)

// SetHTTPClient lets tests inject a recorded transport without touching
// the package-level default.
func SetHTTPClient(c *http.Client) {
	httpMu.Lock()
	defer httpMu.Unlock()
	if c == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
		return
	}
	httpClient = c
}

func getHTTPClient() *http.Client {
	httpMu.RLock()
	defer httpMu.RUnlock()
	return httpClient
}

// EnsureCached fetches url into <CacheDir>/<benchmark>/<relPath> if it
// is not already present. Returns the absolute path of the cached file
// (whether freshly fetched or already on disk).
//
// Writes are atomic via temp file + rename in the same directory, so a
// crash mid-download leaves no partial file at the target path.
// Subsequent calls with the same arguments are no-ops; the function
// is idempotent.
//
// On first fetch, the source URL is logged to stderr so the operator
// has a record of where the data came from and can audit licensing
// after the fact.
func EnsureCached(benchmark, relPath, url string) (string, error) {
	if benchmark == "" {
		return "", fmt.Errorf("benchmark name required")
	}
	if relPath == "" {
		return "", fmt.Errorf("relPath required")
	}
	if url == "" {
		return "", fmt.Errorf("url required")
	}
	root, err := CacheDir()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(root, benchmark, relPath)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat cache: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}

	fmt.Fprintf(os.Stderr, "benchmarks: fetching %s for %s into %s\n", url, benchmark, dest)

	resp, err := getHTTPClient().Get(url)
	if err != nil {
		return "", fmt.Errorf("http get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("http get %s: status %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write body: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename temp->dest: %w", err)
	}
	return dest, nil
}
