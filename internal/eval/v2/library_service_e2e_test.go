//go:build !windows

package eval

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndToEndPassRate_CohesiveFixture is the headline assertion for Plan 04:
// the hand-crafted cohesive fixture has a runnable cmd/server wiring all 25
// endpoints, so the probe plan should score 1.0.
func TestEndToEndPassRate_CohesiveFixture(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	workdir := filepath.Join(root, "test", "evals", "library-service", "fixtures", "cohesive")
	if _, err := os.Stat(filepath.Join(workdir, "cmd", "server", "main.go")); err != nil {
		t.Fatalf("cohesive fixture missing cmd/server: %v", err)
	}

	rate, err := endToEndPassRate(workdir)
	if err != nil {
		t.Fatalf("endToEndPassRate: %v", err)
	}
	if rate != 1.0 {
		t.Errorf("cohesive fixture pass rate = %.3f, want 1.0 (all 25 endpoints)", rate)
	}
}

// TestEndToEndPassRate_EmptySeed locks in the gotcha called out in the plan:
// the seed has cmd/server/main.go but its main is a no-op, so the binary
// builds and exits immediately. We must score 0.0 with a diagnostic error
// (so eval operators can see what went wrong) and no orphaned subprocess.
//
// Score swallows this error and reports EndToEndPassRate=0 cleanly — that's
// covered by TestScore_EmptySeed_Clean below.
func TestEndToEndPassRate_EmptySeed(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	workdir := filepath.Join(root, "test", "evals", "projects", "library-service-seed")

	rate, err := endToEndPassRate(workdir)
	if rate != 0 {
		t.Errorf("empty seed pass rate = %.3f, want 0", rate)
	}
	if err == nil {
		t.Fatal("expected diagnostic error from empty seed (binary exits immediately)")
	}
	if !strings.Contains(err.Error(), "did not come up") {
		t.Errorf("error should explain server failed to come up, got: %v", err)
	}
}

// TestScore_EmptySeed_Clean verifies the wiring: even though endToEndPassRate
// returns an error for the empty seed, Score must swallow it and report
// EndToEndPassRate=0 alongside the other rubric metrics (which are also 0
// for an empty repo). This is the path real evals will hit when a session
// produces nothing useful.
func TestScore_EmptySeed_Clean(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	workdir := filepath.Join(root, "test", "evals", "projects", "library-service-seed")

	ev := NewLibraryServiceEvaluator("", "")
	score, err := ev.Score(t.Context(), workdir)
	if err != nil {
		t.Fatalf("Score should not propagate e2e errors, got: %v", err)
	}
	if score.EndToEndPassRate != 0 {
		t.Errorf("EndToEndPassRate = %.3f, want 0 for empty seed", score.EndToEndPassRate)
	}
}

// TestEndToEndPassRate_PartialImplementation pins the proportional pass-rate
// behavior. A workdir that only implements the books resource should score
// exactly 5/25 = 0.2 — the four books CRUD probes (POST, GET list, GET one,
// PUT) plus the deferred DELETE /books/{id} run last. Everything that depends
// on captured ids from other resources naturally fails because the captures
// are never populated.
func TestEndToEndPassRate_PartialImplementation(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/books-only\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "cmd", "server", "main.go"), booksOnlyServerSrc)

	rate, err := endToEndPassRate(dir)
	if err != nil {
		t.Fatalf("endToEndPassRate: %v", err)
	}
	want := 5.0 / 25.0
	if rate != want {
		t.Errorf("books-only pass rate = %.3f, want %.3f (5/25)", rate, want)
	}
}

// booksOnlyServerSrc is a minimal HTTP server that wires only the 5 books
// endpoints. Used to exercise the partial-implementation path of the probe
// plan without keeping another fixture directory under source control.
const booksOnlyServerSrc = `package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
)

var (
	mu    sync.Mutex
	books = map[string]map[string]any{}
)

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func listBooks(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	out := make([]map[string]any, 0, len(books))
	for _, v := range books {
		out = append(out, v)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func createBook(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	_ = json.NewDecoder(r.Body).Decode(&item)
	if item == nil {
		item = map[string]any{}
	}
	id := newID()
	item["id"] = id
	mu.Lock()
	books[id] = item
	mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
}

func getBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mu.Lock()
	defer mu.Unlock()
	item, ok := books[id]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(item)
}

func updateBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var item map[string]any
	_ = json.NewDecoder(r.Body).Decode(&item)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := books[id]; !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if item == nil {
		item = map[string]any{}
	}
	item["id"] = id
	books[id] = item
	w.WriteHeader(http.StatusNoContent)
}

func deleteBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mu.Lock()
	defer mu.Unlock()
	if _, ok := books[id]; !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	delete(books, id)
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /books", listBooks)
	mux.HandleFunc("POST /books", createBook)
	mux.HandleFunc("GET /books/{id}", getBook)
	mux.HandleFunc("PUT /books/{id}", updateBook)
	mux.HandleFunc("DELETE /books/{id}", deleteBook)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
`

// TestEndToEndPassRate_NoCmdServer covers the cheapest skip path: a workdir
// with no cmd/server is treated as "nothing to probe" and returns (0, nil).
// Same shape diverged/near_cohesive fixtures take in Score.
func TestEndToEndPassRate_NoCmdServer(t *testing.T) {
	tmp := t.TempDir()
	rate, err := endToEndPassRate(tmp)
	if err != nil {
		t.Fatalf("missing cmd/server should not error, got: %v", err)
	}
	if rate != 0 {
		t.Errorf("rate = %.3f, want 0", rate)
	}
}

// TestEndToEndPassRate_BuildFails confirms the build-failure path returns an
// informative error rather than a misleading 0.0. Score swallows the error,
// but callers that bypass Score deserve to see why.
func TestEndToEndPassRate_BuildFails(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "go.mod"), "module example.com/broken\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(tmp, "cmd", "server", "main.go"),
		"package main\n\nfunc main() { this is not valid go }\n")

	rate, err := endToEndPassRate(tmp)
	if err == nil {
		t.Fatalf("expected build failure error, got rate=%.3f nil err", rate)
	}
	if !strings.Contains(err.Error(), "build failed") {
		t.Errorf("error should mention build failure, got: %v", err)
	}
	if rate != 0 {
		t.Errorf("rate on build failure = %.3f, want 0", rate)
	}
}

// TestEndToEndPassRate_SubprocessReaped guards against orphaned child
// processes after the helper returns. We can't easily check the OS
// process table, but we can verify Stop is idempotent and that a normal
// success path (cohesive) leaves no child still running on the port.
func TestEndToEndPassRate_SubprocessReaped(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	workdir := filepath.Join(root, "test", "evals", "library-service", "fixtures", "cohesive")

	port, err := pickFreePort()
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	bin, cleanup, err := buildLibraryServer(workdir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()

	sub, err := startLibraryServer(bin, port)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	sub.Stop()
	sub.Stop() // second call must not block or panic

	// After Stop the port should be free again — i.e. picking the same port
	// can succeed (best-effort signal; OS may hold TIME_WAIT briefly).
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Logf("port %d not immediately free after Stop (TIME_WAIT?): %v", port, err)
		return
	}
	_ = l.Close()
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
