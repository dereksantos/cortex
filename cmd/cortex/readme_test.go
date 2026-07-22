package main

import (
	"os"
	"strings"
	"testing"
)

// TestReadmeSurface pins README.md to GOAL.md §4's acceptance so a later
// edit that drifts the README from the current product surface fails a
// check instead of going unnoticed. It is deliberately grep-shaped to match
// the GOAL.md acceptance criteria exactly.
func TestReadmeSurface(t *testing.T) {
	path := findUp("README.md")
	if path == "" {
		t.Fatal("README.md not found via findUp")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(raw)

	t.Run("documents every command", func(t *testing.T) {
		for _, cmd := range []string{
			"cortex", "resume", "turn", "study", "learn", "change", "serve", "scan", "project", "discord", "study-eval", "model", "version",
		} {
			if !strings.Contains(readme, cmd) {
				t.Errorf("README.md does not mention command %q", cmd)
			}
		}
	})

	t.Run("mentions memory tools study recall context web", func(t *testing.T) {
		checks := []struct {
			name        string
			alternative string
		}{
			{"memory_write", "memory tools"},
			{"study", ""},
			{"recall", ""},
			{"context_evict", "context tools"},
			{"web_search", ""},
			{"fetch_url", ""},
		}
		for _, c := range checks {
			if strings.Contains(readme, c.name) {
				continue
			}
			if c.alternative != "" && strings.Contains(readme, c.alternative) {
				continue
			}
			t.Errorf("README.md mentions neither %q nor %q", c.name, c.alternative)
		}
	})

	t.Run("mentions the agent tool", func(t *testing.T) {
		if !strings.Contains(readme, "`agent`") {
			t.Error("README.md does not mention the `agent` tool (M4 has landed)")
		}
	})

	t.Run("no references to removed surfaces", func(t *testing.T) {
		for _, removed := range []string{
			"project_index", "/remember", "/forget", "cortex daemon", "pkg/cognition",
		} {
			if strings.Contains(readme, removed) {
				t.Errorf("README.md still references removed surface %q", removed)
			}
		}
	})
}
