package mteb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// TestRegistered — the package registers itself under "mteb" via init().
// A typo here breaks CLI dispatch silently; cheaper to catch in unit test.
func TestRegistered(t *testing.T) {
	got, err := benchmarks.Get("mteb")
	if err != nil {
		t.Fatalf("benchmarks.Get(\"mteb\"): %v", err)
	}
	if got.Name() != "mteb" {
		t.Fatalf("Name() = %q, want %q", got.Name(), "mteb")
	}
}

// TestLoadRejectsUnsupportedTask — Phase A wires NFCorpus only; any
// other task name must return an error mentioning "Phase B" so an
// operator who types `--tasks SciFact` understands the gap immediately.
func TestLoadRejectsUnsupportedTask(t *testing.T) {
	b, _ := benchmarks.Get("mteb")
	_, err := b.Load(context.Background(), benchmarks.LoadOpts{Subset: "SciFact"})
	if err == nil {
		t.Fatal("Load(SciFact) should error in Phase A")
	}
	if !strings.Contains(err.Error(), "Phase B") {
		t.Errorf("error %q should mention \"Phase B\"", err.Error())
	}
}

// TestLoadDefaultsToNFCorpus — empty Subset is treated as "NFCorpus".
// This matches the brief: `--tasks NFCorpus` is the only valid value in
// this PR, but operators who omit the flag get the same behaviour rather
// than a confusing usage error.
func TestLoadDefaultsToNFCorpus(t *testing.T) {
	srv := newFakeNFCorpusServer(t, smallFixture())
	defer srv.Close()
	withCacheRoot(t)
	withFakeHostInLoader(t, srv.URL)

	b, _ := benchmarks.Get("mteb")
	insts, err := b.Load(context.Background(), benchmarks.LoadOpts{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("len(insts)=%d, want 1", len(insts))
	}
	if insts[0].ID != "mteb/NFCorpus" {
		t.Errorf("ID = %q, want %q", insts[0].ID, "mteb/NFCorpus")
	}
	payload, ok := insts[0].Payload.(*Task)
	if !ok {
		t.Fatalf("payload type = %T, want *Task", insts[0].Payload)
	}
	if len(payload.Corpus) != 3 {
		t.Errorf("corpus size = %d, want 3", len(payload.Corpus))
	}
	if len(payload.Queries) != 2 {
		t.Errorf("queries = %d, want 2", len(payload.Queries))
	}
	// Qrels are per-query; we check at least one is non-empty.
	if got := payload.Qrels["q1"]["d1"]; got != 2 {
		t.Errorf("qrels[q1][d1] = %d, want 2", got)
	}
}

// TestLoadHonorsLimitOnQueries — --limit caps queries, NOT the corpus.
// The corpus must be intact so retrieval doesn't artificially benefit
// from a tiny haystack.
func TestLoadHonorsLimitOnQueries(t *testing.T) {
	srv := newFakeNFCorpusServer(t, smallFixture())
	defer srv.Close()
	withCacheRoot(t)
	withFakeHostInLoader(t, srv.URL)

	b, _ := benchmarks.Get("mteb")
	insts, err := b.Load(context.Background(), benchmarks.LoadOpts{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	payload := insts[0].Payload.(*Task)
	if len(payload.Queries) != 1 {
		t.Errorf("Limit=1 left %d queries, want 1", len(payload.Queries))
	}
	if len(payload.Corpus) != 3 {
		t.Errorf("Limit=1 trimmed corpus to %d (want 3 — limit caps queries, not corpus)",
			len(payload.Corpus))
	}
}

// TestLoadParsesGradedQrels — the qrels parser must preserve {0,1,2}
// graded relevance (NFCorpus uses 2 for "primary", 1 for "partial").
func TestLoadParsesGradedQrels(t *testing.T) {
	srv := newFakeNFCorpusServer(t, smallFixture())
	defer srv.Close()
	withCacheRoot(t)
	withFakeHostInLoader(t, srv.URL)

	b, _ := benchmarks.Get("mteb")
	insts, _ := b.Load(context.Background(), benchmarks.LoadOpts{})
	payload := insts[0].Payload.(*Task)
	if payload.Qrels["q2"]["d3"] != 1 {
		t.Errorf("qrels[q2][d3] = %d, want 1", payload.Qrels["q2"]["d3"])
	}
}

// TestLoadFiltersToJudgedQueries — NFCorpus's queries.jsonl mixes
// train/dev/test, and only the test split has qrels. Load must drop
// unjudged queries BEFORE applying --limit, or a small --limit can
// score 0 queries when the first N happened to be train-only.
func TestLoadFiltersToJudgedQueries(t *testing.T) {
	f := smallFixture()
	// Add an unjudged query (no qrels for q-extra). Order matters:
	// put it FIRST, so a naive Limit=1 would otherwise pick it.
	f.queries = append([]map[string]string{{"_id": "q-extra", "text": "no qrels here"}}, f.queries...)

	srv := newFakeNFCorpusServer(t, f)
	defer srv.Close()
	withCacheRoot(t)
	withFakeHostInLoader(t, srv.URL)

	b, _ := benchmarks.Get("mteb")
	insts, err := b.Load(context.Background(), benchmarks.LoadOpts{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	payload := insts[0].Payload.(*Task)
	if len(payload.Queries) != 1 {
		t.Fatalf("got %d queries, want 1", len(payload.Queries))
	}
	if payload.Queries[0].ID == "q-extra" {
		t.Errorf("Limit=1 selected unjudged q-extra; filter ran AFTER limit")
	}
}

// TestLoadCachesFiles — second Load doesn't re-fetch (no extra HTTP
// hits). Confirms EnsureCached is being used through the skeleton's
// cache layer rather than re-downloading per invocation.
func TestLoadCachesFiles(t *testing.T) {
	hits := 0
	fixture := smallFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		serveFixture(t, w, r, fixture)
	}))
	defer srv.Close()
	withCacheRoot(t)
	withFakeHostInLoader(t, srv.URL)

	b, _ := benchmarks.Get("mteb")
	if _, err := b.Load(context.Background(), benchmarks.LoadOpts{}); err != nil {
		t.Fatal(err)
	}
	firstHits := hits
	if _, err := b.Load(context.Background(), benchmarks.LoadOpts{}); err != nil {
		t.Fatal(err)
	}
	if hits != firstHits {
		t.Errorf("second Load made %d extra HTTP hits; want 0 (cache miss)", hits-firstHits)
	}
}

// --- fixtures + helpers ---

type fixture struct {
	corpus  []map[string]string
	queries []map[string]string
	qrels   [][3]string // (query-id, corpus-id, score)
}

func smallFixture() fixture {
	return fixture{
		corpus: []map[string]string{
			{"_id": "d1", "title": "alpha", "text": "the quick brown fox"},
			{"_id": "d2", "title": "beta", "text": "lazy dog sleeps"},
			{"_id": "d3", "title": "gamma", "text": "rain falls mainly on the plain"},
		},
		queries: []map[string]string{
			{"_id": "q1", "text": "fox jumps"},
			{"_id": "q2", "text": "rain plain"},
		},
		qrels: [][3]string{
			{"q1", "d1", "2"},
			{"q2", "d3", "1"},
		},
	}
}

// newFakeNFCorpusServer stands in for the HuggingFace download endpoint
// so the loader test stays hermetic. The test pins httpClient via
// benchmarks.SetHTTPClient so any URL — including the real HF one — gets
// dialed back to this server.
func newFakeNFCorpusServer(t *testing.T, f fixture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFixture(t, w, r, f)
	}))
	return srv
}

func serveFixture(t *testing.T, w http.ResponseWriter, r *http.Request, f fixture) {
	t.Helper()
	switch {
	case strings.HasSuffix(r.URL.Path, "corpus.jsonl"):
		writeJSONL(t, w, f.corpus)
	case strings.HasSuffix(r.URL.Path, "queries.jsonl"):
		writeJSONL(t, w, f.queries)
	case strings.HasSuffix(r.URL.Path, "qrels/test.tsv"):
		writeTSV(t, w, f.qrels)
	default:
		http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
	}
}

func writeJSONL(t *testing.T, w io.Writer, rows []map[string]string) {
	t.Helper()
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
		w.Write([]byte("\n"))
	}
}

func writeTSV(t *testing.T, w io.Writer, rows [][3]string) {
	t.Helper()
	// MTEB qrels TSVs begin with a header row that the parser must skip.
	w.Write([]byte("query-id\tcorpus-id\tscore\n"))
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r[0], r[1], r[2])
	}
}

// withCacheRoot points EnsureCached at a temp dir for this test only.
func withCacheRoot(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	prev := os.Getenv("XDG_CACHE_HOME")
	if err := os.Setenv("XDG_CACHE_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if prev == "" {
			os.Unsetenv("XDG_CACHE_HOME")
		} else {
			os.Setenv("XDG_CACHE_HOME", prev)
		}
	})
}

// withFakeHostInLoader swaps the base URL the loader fetches from for
// the lifetime of the test. The mteb package reads it at Load() time.
func withFakeHostInLoader(t *testing.T, base string) {
	t.Helper()
	prev := nfcorpusBaseURL
	nfcorpusBaseURL = base + "/datasets/mteb/nfcorpus/resolve/main"
	t.Cleanup(func() { nfcorpusBaseURL = prev })

	// And route any unrelated GETs to the fake too, since the loader
	// constructs absolute URLs against nfcorpusBaseURL.
	httpc := &http.Client{Transport: &rewriteTransport{target: base}}
	benchmarks.SetHTTPClient(httpc)
	t.Cleanup(func() { benchmarks.SetHTTPClient(nil) })
}

// rewriteTransport sends every request to target.Host, preserving path.
type rewriteTransport struct {
	target string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.target == "" {
		return nil, errors.New("rewriteTransport: empty target")
	}
	u := req.URL
	// Strip the host the loader thought it was hitting; keep the path/query.
	rewrittenPath := u.Path
	// Build a new request against the fake server.
	r2, err := http.NewRequestWithContext(req.Context(), req.Method, rt.target+rewrittenPath, req.Body)
	if err != nil {
		return nil, err
	}
	r2.Header = req.Header
	return http.DefaultTransport.RoundTrip(r2)
}

// Reuse filepath to silence unused-import false positive if cleanup
// helpers move around.
var _ = filepath.Join
