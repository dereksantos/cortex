package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixtureFiles writes the given name→source map under a fresh tempdir
// and returns the dir plus the absolute paths in the same order as names.
// Sources don't need to compile — the scorers go through go/parser directly.
func writeFixtureFiles(t *testing.T, names []string, sources []string) (string, []string) {
	t.Helper()
	if len(names) != len(sources) {
		t.Fatalf("names and sources length mismatch: %d vs %d", len(names), len(sources))
	}
	dir := t.TempDir()
	paths := make([]string, len(names))
	for i, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(sources[i]), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		paths[i] = p
	}
	return dir, paths
}

func TestShapeSimilarity_IdenticalShapes(t *testing.T) {
	cohesive := `package h

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListBooks(w http.ResponseWriter, r *http.Request) {
	items, err := store.List(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		return
	}
}

func GetBook(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(nil)
}
`
	// Same shape, just substitute the resource name.
	mkAuthors := func() string {
		return `package h

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListAuthors(w http.ResponseWriter, r *http.Request) {
	items, err := store.List(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		return
	}
}

func GetAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(nil)
}
`
	}

	_, paths := writeFixtureFiles(t,
		[]string{"books.go", "authors.go", "loans.go"},
		[]string{cohesive, mkAuthors(), mkAuthors()},
	)

	got, err := shapeSimilarity(paths)
	if err != nil {
		t.Fatalf("shapeSimilarity: %v", err)
	}
	if got < 0.95 {
		t.Errorf("identical-shape files should score ≥ 0.95, got %.3f", got)
	}
}

func TestShapeSimilarity_DivergentShapes(t *testing.T) {
	// Each file uses a different idiom: error wrapping, response writing,
	// signature shape, status codes.
	books := `package h

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListBooks(w http.ResponseWriter, r *http.Request) {
	items, err := list()
	if err != nil {
		http.Error(w, fmt.Errorf("list: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(items)
}
`
	authors := `package h

import (
	"github.com/pkg/errors"
)

type AuthorService struct{}

func (s *AuthorService) Index() ([]Author, error) {
	rows, err := s.db.Query("SELECT * FROM authors")
	if err != nil {
		return nil, errors.Wrap(err, "query")
	}
	return rows, nil
}
`
	loans := `package h

import (
	"log"
)

func loanGetter(id int) interface{} {
	defer func() {
		if r := recover(); r != nil {
			log.Println("recovered:", r)
		}
	}()
	panic("not implemented")
}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books.go", "authors.go", "loans.go"},
		[]string{books, authors, loans},
	)

	got, err := shapeSimilarity(paths)
	if err != nil {
		t.Fatalf("shapeSimilarity: %v", err)
	}
	if got > 0.5 {
		t.Errorf("divergent-shape files should score ≤ 0.5, got %.3f", got)
	}
}

func TestShapeSimilarity_RejectsSingleFile(t *testing.T) {
	_, paths := writeFixtureFiles(t,
		[]string{"books.go"},
		[]string{"package h"},
	)
	if _, err := shapeSimilarity(paths); err == nil {
		t.Fatal("expected error with only 1 file")
	}
}

func TestSignatureKey(t *testing.T) {
	src := `package h

import "net/http"

func A(w http.ResponseWriter, r *http.Request) {}
func B(x, y int) (string, error) { return "", nil }
func C() {}
`
	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	feats, err := extractHandlerFeatures(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantSigs := map[string]bool{
		"sig:(http.ResponseWriter,*http.Request)": true,
		"sig:(int,int)->string,error":             true,
		"sig:()":                                  true,
	}
	for k := range wantSigs {
		if feats[k] == 0 {
			t.Errorf("missing expected feature %q", k)
		}
	}
}
