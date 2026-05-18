package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestFetchExternal_GoDocStdlib(t *testing.T) {
	// fmt is stdlib — go doc always works, fast.
	dir := t.TempDir()
	handler := NewFetchExternalHandler(FetchExternalConfig{CacheDir: dir})
	res, err := handler(context.Background(),
		map[string]any{"package": "fmt", "symbol": "Println"},
		dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 3})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	snippet, _ := res.Out["snippet"].(string)
	if !strings.Contains(snippet, "func Println") {
		t.Errorf("expected snippet to contain 'func Println'; got %q", snippet)
	}
	if res.Out["cached"].(bool) {
		t.Errorf("first call should not be cached; got cached=true")
	}
}

func TestFetchExternal_CacheHitOnSecondCall(t *testing.T) {
	dir := t.TempDir()
	handler := NewFetchExternalHandler(FetchExternalConfig{CacheDir: dir})
	in := map[string]any{"package": "fmt", "symbol": "Sprintf"}

	res1, _ := handler(context.Background(), in, dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 3})
	res2, _ := handler(context.Background(), in, dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 3})

	if res1.Out["cached"].(bool) {
		t.Errorf("first call should not be cached")
	}
	if !res2.Out["cached"].(bool) {
		t.Errorf("second call should be cached; got cached=false")
	}
	if res1.Out["snippet"] != res2.Out["snippet"] {
		t.Errorf("cached snippet should match original; got %q vs %q",
			res1.Out["snippet"], res2.Out["snippet"])
	}
	// Cache file should exist on disk.
	if _, err := os.Stat(filepath.Join(dir, "fmt.Sprintf.txt")); err != nil {
		t.Errorf("cache file missing: %v", err)
	}
}

func TestFetchExternal_MissingPackageInput(t *testing.T) {
	handler := NewFetchExternalHandler(FetchExternalConfig{CacheDir: t.TempDir()})
	res, _ := handler(context.Background(),
		map[string]any{},
		dag.Budget{LatencyMS: 1000, Tokens: 0, Depth: 3})
	if errStr, _ := res.Out["error"].(string); errStr == "" {
		t.Errorf("expected error output for missing package; got %+v", res.Out)
	}
}

func TestFetchExternal_CacheKeySanitizesPath(t *testing.T) {
	// Import path with slashes shouldn't escape the cache dir.
	key := cacheKey("github.com/jmoiron/sqlx", "")
	if strings.Contains(key, "/") {
		t.Errorf("cache key should sanitize slashes; got %q", key)
	}
}
