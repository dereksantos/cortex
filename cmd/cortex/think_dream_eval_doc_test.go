package main

import (
	"os"
	"strings"
	"testing"
)

// TestThinkDreamDesignDoc pins docs/think-dream-eval.md to GOAL.md §5's
// acceptance so a later edit that drops a required heading (or the file
// itself) fails a check instead of going unnoticed. Deliberately grep-shaped
// to match the GOAL.md acceptance criteria exactly, mirroring
// TestReadmeSurface's shape for README.md.
func TestThinkDreamDesignDoc(t *testing.T) {
	path := findUp("docs/think-dream-eval.md")
	if path == "" {
		t.Fatal("docs/think-dream-eval.md not found via findUp")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read docs/think-dream-eval.md: %v", err)
	}
	doc := string(raw)

	t.Run("has all four required section headings", func(t *testing.T) {
		for _, heading := range []string{
			"## Decision question",
			"## Prior art in git history",
			"## The eval",
			"## Go / no-go criteria",
		} {
			if !strings.Contains(doc, heading) {
				t.Errorf("docs/think-dream-eval.md missing required heading %q", heading)
			}
		}
	})
}
