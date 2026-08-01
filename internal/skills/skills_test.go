package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates dir/name/SKILL.md with the given body under t.TempDir().
func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	skillDir := filepath.Join(root, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseFrontmatterCases(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantName    string
		wantDesc    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "plain",
			body:     "---\nname: pdf-tools\ndescription: Extract text and tables from PDFs.\n---\n\nBody here.\n",
			wantName: "pdf-tools",
			wantDesc: "Extract text and tables from PDFs.",
		},
		{
			name:     "quoted",
			body:     "---\nname: 'pdf-tools'\ndescription: \"Extract text and tables from PDFs.\"\n---\n",
			wantName: "pdf-tools",
			wantDesc: "Extract text and tables from PDFs.",
		},
		{
			name:        "missing name",
			body:        "---\ndescription: no name here\n---\n",
			wantErr:     true,
			errContains: "\"name\"",
		},
		{
			name:        "missing description",
			body:        "---\nname: pdf-tools\n---\n",
			wantErr:     true,
			errContains: "\"description\"",
		},
		{
			name:        "no frontmatter",
			body:        "# Just a markdown file\n\nNo frontmatter block at all.\n",
			wantErr:     true,
			errContains: "frontmatter",
		},
		{
			name:        "unclosed frontmatter",
			body:        "---\nname: pdf-tools\ndescription: x\n",
			wantErr:     true,
			errContains: "closed",
		},
		{
			name:        "multi-line description block scalar",
			body:        "---\nname: pdf-tools\ndescription: |\n  line one\n  line two\n---\n",
			wantErr:     true,
			errContains: "multiple lines",
		},
		{
			name:        "multi-line description bare continuation",
			body:        "---\nname: pdf-tools\ndescription: this description\n  continues on the next line\n---\n",
			wantErr:     true,
			errContains: "multiple lines",
		},
		{
			name:     "other fields tolerated but ignored",
			body:     "---\nname: pdf-tools\ndescription: Extract text.\nlicense: MIT\nallowed-tools: [read_file]\nuser-invocable: true\n---\n",
			wantName: "pdf-tools",
			wantDesc: "Extract text.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, desc, err := parseFrontmatter([]byte(c.body))
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseFrontmatter(%q): expected error, got name=%q desc=%q", c.name, name, desc)
				}
				if c.errContains != "" && !strings.Contains(err.Error(), c.errContains) {
					t.Errorf("parseFrontmatter(%q): error %q does not contain %q", c.name, err.Error(), c.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFrontmatter(%q): unexpected error: %v", c.name, err)
			}
			if name != c.wantName {
				t.Errorf("parseFrontmatter(%q): name = %q, want %q", c.name, name, c.wantName)
			}
			if desc != c.wantDesc {
				t.Errorf("parseFrontmatter(%q): description = %q, want %q", c.name, desc, c.wantDesc)
			}
		})
	}
}

func TestDiscoverValidatesNameAndDescription(t *testing.T) {
	root := t.TempDir()
	// name/dir mismatch
	writeSkill(t, root, "actual-dir", "---\nname: different-name\ndescription: valid desc\n---\n")
	// over-length description
	long := make([]byte, maxDescLen+1)
	for i := range long {
		long[i] = 'a'
	}
	writeSkill(t, root, "too-long-desc", "---\nname: too-long-desc\ndescription: "+string(long)+"\n---\n")
	// bad name charset
	writeSkill(t, root, "Bad_Name", "---\nname: Bad_Name\ndescription: valid desc\n---\n")
	// valid skill, to prove the others didn't poison discovery
	writeSkill(t, root, "good-skill", "---\nname: good-skill\ndescription: A perfectly fine skill.\n---\n")

	got := Discover([]string{root})
	if len(got) != 1 {
		t.Fatalf("Discover: got %d skills, want 1 (only good-skill should validate): %+v", len(got), got)
	}
	if got[0].Name != "good-skill" {
		t.Errorf("Discover: got skill %q, want good-skill", got[0].Name)
	}
}

func TestDiscoverPrecedenceAndDedup(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dirA, "shared", "---\nname: shared\ndescription: from A, wins.\n---\n")
	writeSkill(t, dirB, "shared", "---\nname: shared\ndescription: from B, shadowed.\n---\n")
	writeSkill(t, dirB, "only-in-b", "---\nname: only-in-b\ndescription: unique to B.\n---\n")

	got := Discover([]string{dirA, dirB})
	if len(got) != 2 {
		t.Fatalf("Discover: got %d skills, want 2: %+v", len(got), got)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if s, ok := byName["shared"]; !ok || s.Description != "from A, wins." {
		t.Errorf("Discover: shared = %+v, want dirA's version to win (first dir wins)", s)
	}
	if _, ok := byName["only-in-b"]; !ok {
		t.Error("Discover: only-in-b missing — a later dir's unique skill should still be discovered")
	}
	if !filepath.IsAbs(byName["shared"].Path) {
		t.Errorf("Discover: Path %q is not absolute", byName["shared"].Path)
	}
}

func TestDiscoverMissingDirsAreSilentlySkipped(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist")
	root := t.TempDir()
	writeSkill(t, root, "good-skill", "---\nname: good-skill\ndescription: fine.\n---\n")

	got := Discover([]string{nonexistent, root})
	if len(got) != 1 || got[0].Name != "good-skill" {
		t.Errorf("Discover: got %+v, want exactly good-skill (missing dir must not error or block later dirs)", got)
	}
}

func TestDiscoverIgnoresNonSkillSubdirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose-file.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Discover([]string{root})
	if len(got) != 0 {
		t.Errorf("Discover: got %+v, want none (no SKILL.md anywhere)", got)
	}
}
