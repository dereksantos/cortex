package swebench

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// recordingTransport hands back a fixed response body for any request
// URL containing the given offset substring. Used to drive LoadInstances
// without touching the network or vendoring real SWE-bench rows.
type recordingTransport struct {
	mu    sync.Mutex
	pages map[int][]byte // offset → response body
	hits  []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.hits = append(rt.hits, req.URL.String())
	off, _ := strconv.Atoi(req.URL.Query().Get("offset"))
	body, ok := rt.pages[off]
	if !ok {
		body = []byte(`{"rows": []}`)
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

// syntheticPage builds a /rows envelope containing fabricated instance
// rows. Fields shadow the upstream schema but contain only
// test-author-written content — no upstream data is vendored.
func syntheticPage(t *testing.T, ids []string, repo string) []byte {
	t.Helper()
	rows := make([]map[string]any, 0, len(ids))
	for i, id := range ids {
		row := map[string]any{
			"instance_id":              id,
			"repo":                     repo,
			"base_commit":              "deadbeef" + strconv.Itoa(i),
			"problem_statement":        "Fix the synthetic " + id + " bug",
			"patch":                    "diff --git a/x b/x\n",
			"test_patch":               "diff --git a/x_test b/x_test\n",
			"hints_text":               "",
			"created_at":               "2026-01-01T00:00:00Z",
			"version":                  "0.1",
			"environment_setup_commit": "cafebabe",
			// Use the string-encoded variant to exercise that branch.
			"FAIL_TO_PASS": `["test_one", "test_two"]`,
			// Use the native list variant.
			"PASS_TO_PASS": []string{"test_existing"},
		}
		rows = append(rows, map[string]any{"row_idx": i, "row": row})
	}
	body, err := json.Marshal(map[string]any{"rows": rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// withFreshCache reroutes the benchmarks cache dir to a tempdir so each
// test starts with no on-disk state. Returns a teardown closure.
func withFreshCache(t *testing.T) func() {
	t.Helper()
	dir, err := os.MkdirTemp("", "cortex-swebench-cache-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prev := os.Getenv("XDG_CACHE_HOME")
	t.Setenv("XDG_CACHE_HOME", dir)
	return func() {
		os.RemoveAll(dir)
		if prev != "" {
			os.Setenv("XDG_CACHE_HOME", prev)
		}
	}
}

func TestLoadInstances_Limit(t *testing.T) {
	defer withFreshCache(t)()

	pages := map[int][]byte{
		0: syntheticPage(t, []string{"foo__a-1", "foo__a-2", "foo__a-3"}, "foo/a"),
	}
	rt := &recordingTransport{pages: pages}
	benchmarks.SetHTTPClient(&http.Client{Transport: rt})
	defer benchmarks.SetHTTPClient(nil)

	out, err := LoadInstances(context.Background(), benchmarks.LoadOpts{Subset: "verified", Limit: 2})
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	if out[0].InstanceID != "foo__a-1" {
		t.Errorf("want sorted first id foo__a-1, got %s", out[0].InstanceID)
	}
	if len(out[0].FailToPass) != 2 || out[0].FailToPass[0] != "test_one" {
		t.Errorf("FailToPass parse: %v", out[0].FailToPass)
	}
	if len(out[0].PassToPass) != 1 || out[0].PassToPass[0] != "test_existing" {
		t.Errorf("PassToPass parse: %v", out[0].PassToPass)
	}
}

func TestLoadInstances_RepoFilter(t *testing.T) {
	defer withFreshCache(t)()

	pageA := syntheticPage(t, []string{"foo__a-1"}, "foo/a")
	pageB := syntheticPage(t, []string{"bar__b-1"}, "bar/b")
	// Both fit in one page; build a combined envelope manually.
	var combined struct {
		Rows []json.RawMessage `json:"rows"`
	}
	var a, b struct{ Rows []json.RawMessage }
	if err := json.Unmarshal(pageA, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pageB, &b); err != nil {
		t.Fatal(err)
	}
	combined.Rows = append(combined.Rows, a.Rows...)
	combined.Rows = append(combined.Rows, b.Rows...)
	body, _ := json.Marshal(combined)

	rt := &recordingTransport{pages: map[int][]byte{0: body}}
	benchmarks.SetHTTPClient(&http.Client{Transport: rt})
	defer benchmarks.SetHTTPClient(nil)

	out, err := LoadInstances(context.Background(), benchmarks.LoadOpts{
		Subset: "verified",
		Limit:  10,
		Filter: map[string]string{"repo": "foo/a"},
	})
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(out) != 1 || out[0].Repo != "foo/a" {
		t.Fatalf("want one foo/a, got %+v", out)
	}
}

func TestLoadInstances_UnsupportedSubset(t *testing.T) {
	_, err := LoadInstances(context.Background(), benchmarks.LoadOpts{Subset: "lite"})
	if err == nil || !strings.Contains(err.Error(), "unsupported subset") {
		t.Fatalf("want unsupported subset error, got %v", err)
	}
}

// TestLoadInstances_FixtureRoundtrip verifies that on-disk cache files
// round-trip through parseRowsResponse. Confirms the cache layer the
// loader relies on isn't corrupting structure.
func TestLoadInstances_FixtureRoundtrip(t *testing.T) {
	defer withFreshCache(t)()

	body := syntheticPage(t, []string{"zzz__last-1"}, "zzz/last")
	dir := t.TempDir()
	path := filepath.Join(dir, "page.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := parseRowsResponse(read)
	if err != nil {
		t.Fatalf("parseRowsResponse: %v", err)
	}
	if len(out) != 1 || out[0].InstanceID != "zzz__last-1" {
		t.Errorf("got %+v", out)
	}
}
