package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// slashCommandPattern pulls the command token off each helpLines entry
// ("/help              show this list" -> "/help"). helpLines (main.go) is
// already the /help body's source of truth, so this reads it directly
// rather than re-deriving it from source text.
var slashCommandPattern = regexp.MustCompile(`^(/\S+)`)

// TestToolSurfaceDocumented pins README.md and CLAUDE.md to the tool
// registry (internal/tools.All) and the REPL slash-command list (main.go's
// helpLines): a tool or slash command added to the code without a matching
// doc mention trips this instead of going unnoticed. Deliberately
// grep-shaped, mirroring TestReadmeSurface.
func TestToolSurfaceDocumented(t *testing.T) {
	readmePath := findUp("README.md")
	if readmePath == "" {
		t.Fatal("README.md not found via findUp")
	}
	claudePath := findUp("CLAUDE.md")
	if claudePath == "" {
		t.Fatal("CLAUDE.md not found via findUp")
	}
	readmeRaw, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	claudeRaw, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	readme := string(readmeRaw)
	claude := string(claudeRaw)

	t.Run("every registered tool is documented", func(t *testing.T) {
		for _, tool := range tools.All {
			name := tool.Function.Name
			if !strings.Contains(readme, name) {
				t.Errorf("README.md does not mention tool %q", name)
			}
			if !strings.Contains(claude, name) {
				t.Errorf("CLAUDE.md does not mention tool %q", name)
			}
		}
	})

	t.Run("every slash command is documented", func(t *testing.T) {
		for _, line := range helpLines {
			m := slashCommandPattern.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				t.Fatalf("helpLines entry %q does not start with a /command", line)
			}
			cmd := m[1]
			if !strings.Contains(readme, cmd) {
				t.Errorf("README.md does not mention slash command %q", cmd)
			}
			if !strings.Contains(claude, cmd) {
				t.Errorf("CLAUDE.md does not mention slash command %q", cmd)
			}
		}
	})
}

// journalTypeConstPattern matches a Go constant assignment whose value is a
// journal entry-type string ("<class>.<kind>", e.g. "study.result"). Scoped
// to identifiers named Type* since every entry-type constant in
// internal/journal follows that convention (see internal/journal/entry.go).
var journalTypeConstPattern = regexp.MustCompile(`\bType[A-Za-z0-9_]*\s*=\s*"([a-z_]+\.[a-z_]+)"`)

// TestJournalClassesDocumented pins docs/journal.md's writer-class taxonomy
// to the Type* entry-type constants actually defined in internal/journal:
// a class or entry type added (or renamed) in code without a matching
// update to the taxonomy table trips this instead of silently drifting, the
// way the six-live/seven-dormant reconciliation this test pins against was
// found by hand. Deliberately grep-shaped, scanning source text rather than
// hardcoding the class list.
func TestJournalClassesDocumented(t *testing.T) {
	journalDocPath := findUp("docs/journal.md")
	if journalDocPath == "" {
		t.Fatal("docs/journal.md not found via findUp")
	}
	docRaw, err := os.ReadFile(journalDocPath)
	if err != nil {
		t.Fatalf("read docs/journal.md: %v", err)
	}
	doc := string(docRaw)

	journalDir := findUp("internal/journal")
	if journalDir == "" {
		t.Fatal("internal/journal not found via findUp")
	}
	entries, err := os.ReadDir(journalDir)
	if err != nil {
		t.Fatalf("read internal/journal: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(journalDir, name))
		if err != nil {
			t.Fatalf("read internal/journal/%s: %v", name, err)
		}
		for _, m := range journalTypeConstPattern.FindAllStringSubmatch(string(raw), -1) {
			seen[m[1]] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no Type* entry-type constants under internal/journal; scan pattern likely broken")
	}

	for entryType := range seen {
		if !strings.Contains(doc, entryType) {
			t.Errorf("docs/journal.md does not mention journal entry type %q", entryType)
		}
	}
}

// envVarPattern extracts the literal env-var name from os.Getenv("NAME"),
// os.LookupEnv("NAME"), or the local envInt("NAME", def) helper — the three
// call shapes production code uses to read an env var.
var envVarPattern = regexp.MustCompile(`(?:os\.Getenv|os\.LookupEnv|envInt)\("([A-Za-z0-9_]+)"`)

// productionEnvVarPattern filters to Cortex/Discord-owned names; provider
// vars like OPEN_ROUTER_API_KEY or ANTHROPIC_API_KEY are conventional and
// out of scope for docs/configuration.md.
var productionEnvVarPattern = regexp.MustCompile(`^(CORTEX_|DISCORD_)`)

// TestEnvVarsDocumented pins docs/configuration.md to every CORTEX_/DISCORD_
// env var actually read by production code: one added without a doc mention
// trips this instead of going unnoticed. Deliberately grep-shaped, walking
// source text under cmd/, internal/, pkg/ rather than hardcoding the list.
func TestEnvVarsDocumented(t *testing.T) {
	configDocPath := findUp("docs/configuration.md")
	if configDocPath == "" {
		t.Fatal("docs/configuration.md not found via findUp")
	}
	docRaw, err := os.ReadFile(configDocPath)
	if err != nil {
		t.Fatalf("read docs/configuration.md: %v", err)
	}
	doc := string(docRaw)

	root := findUp("go.mod")
	if root == "" {
		t.Fatal("go.mod not found via findUp")
	}
	root = filepath.Dir(root)

	seen := map[string]bool{}
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		start := filepath.Join(root, dir)
		err := filepath.WalkDir(start, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, m := range envVarPattern.FindAllStringSubmatch(string(raw), -1) {
				if productionEnvVarPattern.MatchString(m[1]) {
					seen[m[1]] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no CORTEX_/DISCORD_ env vars under cmd/, internal/, pkg/; scan pattern likely broken")
	}

	for name := range seen {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/configuration.md does not mention env var %q", name)
		}
	}
}
