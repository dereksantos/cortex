// Package memory is the file-backed substrate for model-driven memory: a set of
// free-form named notes under .cortex/memory/, plus a regenerated INDEX.md that
// lists them. The model curates it through tools (write/read/search/forget); the
// only mechanical use is injecting the index so the model knows what it can
// recall. See docs/memory-tools.md.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store holds the memory notes for one project. Safe for concurrent use.
type Store struct {
	dir string // <contextDir>/memory
	mu  sync.Mutex
}

// New opens (creating if needed) the memory store under contextDir/memory.
func New(contextDir string) (*Store, error) {
	dir := filepath.Join(contextDir, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// NoteMeta is a note's identity + a one-line hook for the index/search results.
type NoteMeta struct {
	Name    string
	Hook    string
	Updated time.Time
}

var (
	nameUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)
	fmBlock    = regexp.MustCompile(`(?s)^---\n.*?\n---\n`)
)

// normalizeName turns a model-chosen name into a safe, conflict-free filename:
// lowercased kebab, no path separators or traversal, single .md extension. This
// is what makes "the model names the files" safe.
func normalizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".md")
	n = nameUnsafe.ReplaceAllString(n, "-")
	n = strings.Trim(n, "-._")
	if n == "" {
		return ""
	}
	return n + ".md"
}

func (s *Store) path(name string) (string, bool) {
	fn := normalizeName(name)
	if fn == "" || fn == "index.md" {
		return "", false
	}
	return filepath.Join(s.dir, fn), true
}

// Write creates or updates a note. created is preserved across updates; updated
// is stamped now. The index is regenerated. Returns the normalized name.
func (s *Store) Write(name, content string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.path(name)
	if !ok {
		return "", fmt.Errorf("invalid memory name %q", name)
	}
	created := now
	if existing, err := os.ReadFile(p); err == nil {
		if c := frontmatterTime(existing, "created"); !c.IsZero() {
			created = c
		}
	}
	body := strings.TrimSpace(stripFrontmatter(content))
	doc := fmt.Sprintf("---\ncreated: %s\nupdated: %s\n---\n%s\n",
		created.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), body)
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		return "", fmt.Errorf("write note: %w", err)
	}
	if err := s.writeIndexLocked(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(filepath.Base(p), ".md"), nil
}

// Read returns a note's body (frontmatter stripped). os.ErrNotExist if absent.
func (s *Store) Read(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.path(name)
	if !ok {
		return "", fmt.Errorf("invalid memory name %q", name)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stripFrontmatter(string(b))), nil
}

// Forget removes a note. Missing is not an error (idempotent).
func (s *Store) Forget(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.path(name)
	if !ok {
		return false, fmt.Errorf("invalid memory name %q", name)
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, s.writeIndexLocked()
}

// List returns every note's metadata, most-recently-updated first.
func (s *Store) List() ([]NoteMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// Search returns notes whose name or body contains all query terms (case-
// insensitive), most-recently-updated first. Keyword, not semantic — the corpus
// is the model's own notes and the full index is injected anyway.
func (s *Store) Search(query string) ([]NoteMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil, nil
	}
	metas, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	var out []NoteMeta
	for _, m := range metas {
		b, err := os.ReadFile(filepath.Join(s.dir, m.Name+".md"))
		if err != nil {
			continue
		}
		hay := strings.ToLower(m.Name + " " + string(b))
		if containsAll(hay, terms) {
			out = append(out, m)
		}
	}
	return out, nil
}

// Index renders the injectable recall surface: one line per note. Empty string
// when there are no notes.
func (s *Store) Index() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	metas, err := s.listLocked()
	if err != nil {
		return "", err
	}
	return renderIndex(metas), nil
}

func (s *Store) listLocked() ([]NoteMeta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var metas []NoteMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "INDEX.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		metas = append(metas, NoteMeta{
			Name:    strings.TrimSuffix(e.Name(), ".md"),
			Hook:    hook(stripFrontmatter(string(b))),
			Updated: frontmatterTime(b, "updated"),
		})
	}
	sort.Slice(metas, func(i, j int) bool {
		if !metas[i].Updated.Equal(metas[j].Updated) {
			return metas[i].Updated.After(metas[j].Updated)
		}
		return metas[i].Name < metas[j].Name
	})
	return metas, nil
}

func (s *Store) writeIndexLocked() error {
	metas, err := s.listLocked()
	if err != nil {
		return err
	}
	idx := renderIndex(metas)
	p := filepath.Join(s.dir, "INDEX.md")
	if idx == "" {
		_ = os.Remove(p)
		return nil
	}
	return os.WriteFile(p, []byte(idx), 0o644)
}

func renderIndex(metas []NoteMeta) string {
	if len(metas) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Memory index\n")
	for _, m := range metas {
		when := ""
		if !m.Updated.IsZero() {
			when = " (updated " + m.Updated.UTC().Format("2006-01-02") + ")"
		}
		fmt.Fprintf(&b, "- %s — %s%s\n", m.Name, m.Hook, when)
	}
	return strings.TrimRight(b.String(), "\n")
}

func stripFrontmatter(s string) string { return fmBlock.ReplaceAllString(s, "") }

func frontmatterTime(b []byte, key string) time.Time {
	m := fmBlock.Find(b)
	if m == nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(m), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func hook(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if l := strings.TrimSpace(strings.TrimLeft(line, "#")); l != "" {
			r := []rune(strings.Join(strings.Fields(l), " "))
			if len(r) > 80 {
				return string(r[:80]) + "…"
			}
			return string(r)
		}
	}
	return ""
}

func containsAll(hay string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}
