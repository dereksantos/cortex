package fractal

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNovelty_SeenWithinWindow(t *testing.T) {
	n := NewNovelty(8, 1*time.Hour)
	h := n.HashContent("hello")
	if n.Seen("a", h) {
		t.Fatal("nothing recorded yet")
	}
	n.RecordSeen("a", h, false)
	if !n.Seen("a", h) {
		t.Errorf("should be seen immediately after record")
	}
}

func TestNovelty_ContentHashMismatch(t *testing.T) {
	n := NewNovelty(8, 1*time.Hour)
	h1 := n.HashContent("v1")
	h2 := n.HashContent("v2")
	n.RecordSeen("a", h1, true)
	if n.Seen("a", h2) {
		t.Errorf("changed content should not be seen")
	}
}

func TestNovelty_LRUEviction(t *testing.T) {
	n := NewNovelty(2, 1*time.Hour)
	n.RecordSeen("a", 1, false)
	n.RecordSeen("b", 2, false)
	n.RecordSeen("c", 3, false)
	if n.Len() != 2 {
		t.Errorf("expected cap=2 after 3 inserts, got %d", n.Len())
	}
	if n.Seen("a", 1) {
		t.Errorf("'a' should have been evicted")
	}
}

func TestNovelty_SnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "novelty.json")

	a := NewNovelty(8, 1*time.Hour)
	a.RecordSeen("x", 42, true)
	a.RecordSeen("y", 99, false)
	if err := a.Snapshot(path); err != nil {
		t.Fatal(err)
	}

	b := NewNovelty(8, 1*time.Hour)
	if err := b.Load(path); err != nil {
		t.Fatal(err)
	}
	if !b.Seen("x", 42) || !b.Seen("y", 99) {
		t.Errorf("loaded cache should retain entries")
	}
}

func TestNovelty_LoadMissingFile(t *testing.T) {
	n := NewNovelty(4, time.Hour)
	if err := n.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Errorf("missing snapshot must not error: %v", err)
	}
}
