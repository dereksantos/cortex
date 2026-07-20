package memory

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStore_WriteReadUpdateForget(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	name, err := s.Write("Auth Decisions", "We sign requests with JWT bearer tokens.", t0)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if name != "auth-decisions" {
		t.Errorf("name = %q, want auth-decisions (normalized)", name)
	}

	body, err := s.Read("auth-decisions")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if body != "We sign requests with JWT bearer tokens." {
		t.Errorf("body = %q (frontmatter not stripped?)", body)
	}

	// Update preserves created, advances updated.
	t1 := t0.Add(48 * time.Hour)
	if _, err := s.Write("auth-decisions", "Now we also rotate keys weekly.", t1); err != nil {
		t.Fatalf("update: %v", err)
	}
	metas, _ := s.List()
	if len(metas) != 1 || !metas[0].Updated.Equal(t1) {
		t.Errorf("after update: metas=%v want updated=%v", metas, t1)
	}

	ok, err := s.Forget("auth-decisions")
	if err != nil || !ok {
		t.Fatalf("Forget: ok=%v err=%v", ok, err)
	}
	if _, err := s.Read("auth-decisions"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("read after forget: err=%v, want not-exist", err)
	}
}

func TestStore_IndexDeterministicOnTimestampTie(t *testing.T) {
	s, _ := New(t.TempDir())
	// Same calendar day (day-granularity render) => equal updated timestamp.
	now := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	_, _ = s.Write("deploy", "We deploy on Mondays.", now)
	_, _ = s.Write("auth", "JWT bearer tokens.", now)

	first, _ := s.Index()
	for i := 0; i < 20; i++ {
		again, _ := s.Index()
		if again != first {
			t.Fatalf("index not stable across calls (tie on updated):\n first: %q\n iter%d: %q", first, i, again)
		}
	}
}

func TestStore_IndexAndSearch(t *testing.T) {
	s, _ := New(t.TempDir())
	now := time.Now()
	_, _ = s.Write("deploy", "We deploy on Mondays via the release script.", now)
	_, _ = s.Write("auth", "JWT bearer tokens, not sessions.", now.Add(time.Hour))

	idx, _ := s.Index()
	if !strings.Contains(idx, "deploy") || !strings.Contains(idx, "auth") || !strings.Contains(idx, "updated ") {
		t.Errorf("index missing notes or dates:\n%s", idx)
	}

	hits, _ := s.Search("jwt tokens")
	if len(hits) != 1 || hits[0].Name != "auth" {
		t.Errorf("search = %v, want [auth]", hits)
	}
	if hits, _ := s.Search("kubernetes"); len(hits) != 0 {
		t.Errorf("search for absent term returned %v", hits)
	}
}

func TestStore_NameSafety(t *testing.T) {
	s, _ := New(t.TempDir())
	// Path traversal / separators must be neutralized, never escape the dir.
	name, err := s.Write("../../etc/passwd", "nope", time.Now())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		t.Errorf("unsafe name leaked: %q", name)
	}
	if _, err := s.Read(name); err != nil {
		t.Errorf("normalized note not readable: %v", err)
	}
}
