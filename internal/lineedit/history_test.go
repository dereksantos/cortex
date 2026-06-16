package lineedit

import (
	"path/filepath"
	"testing"
)

func TestHistoryAddDedupAndSkip(t *testing.T) {
	h := &History{max: defaultMaxHistory}
	h.Add("one")
	h.Add("two")
	h.Add("two") // consecutive dup ignored
	h.Add("  ")  // blank ignored
	h.Add("three")
	if h.Len() != 3 {
		t.Fatalf("len = %d, want 3", h.Len())
	}
	if h.at(0) != "one" || h.at(2) != "three" {
		t.Errorf("entries = %q..%q", h.at(0), h.at(2))
	}
}

func TestHistoryPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h := LoadHistory(path) // missing file → empty, no error
	h.Add("first command")
	h.Add("multi\nline\npaste")
	h.Add("third")

	// Reload from disk: order and multi-line content preserved.
	h2 := LoadHistory(path)
	if h2.Len() != 3 {
		t.Fatalf("reloaded len = %d, want 3", h2.Len())
	}
	if h2.at(0) != "first command" || h2.at(1) != "multi\nline\npaste" || h2.at(2) != "third" {
		t.Errorf("reloaded entries wrong: %q / %q / %q", h2.at(0), h2.at(1), h2.at(2))
	}
}

func TestHistorySearchBackward(t *testing.T) {
	h := &History{max: defaultMaxHistory}
	for _, s := range []string{"go build", "git status", "go test ./...", "git commit"} {
		h.Add(s)
	}
	// Newest match for "git" is "git commit" (index 3).
	idx, m, ok := h.searchBackward("git", h.Len())
	if !ok || idx != 3 || m != "git commit" {
		t.Fatalf("first git match = (%d,%q,%v)", idx, m, ok)
	}
	// Next older "git" match before index 3 is "git status" (index 1).
	idx, m, ok = h.searchBackward("git", idx)
	if !ok || idx != 1 || m != "git status" {
		t.Fatalf("second git match = (%d,%q,%v)", idx, m, ok)
	}
	// Case-insensitive.
	if _, m, ok := h.searchBackward("GO TEST", h.Len()); !ok || m != "go test ./..." {
		t.Errorf("case-insensitive search = (%q,%v)", m, ok)
	}
	// No match.
	if _, _, ok := h.searchBackward("docker", h.Len()); ok {
		t.Error("expected no match for 'docker'")
	}
}

func TestHistoryNilSafe(t *testing.T) {
	var h *History
	h.Add("x") // must not panic
	if h.Len() != 0 {
		t.Errorf("nil history Len = %d, want 0", h.Len())
	}
	if _, _, ok := h.searchBackward("x", 0); ok {
		t.Error("nil search should not match")
	}
}
