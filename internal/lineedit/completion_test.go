package lineedit

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- commonPrefix / fillPrefix (the acceptance "common-prefix fill") --------

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"empty list", nil, ""},
		{"single element", []string{"/model"}, "/model"},
		{"shared prefix", []string{"/model", "/model-x"}, "/model"},
		{"no shared prefix", []string{"/model", "/help"}, "/"},
		{"one is a prefix of the other", []string{"/m", "/model"}, "/m"},
		{"all equal", []string{"a", "a", "a"}, "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commonPrefix(tt.in); got != tt.want {
				t.Errorf("commonPrefix(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFillPrefix(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		cursor   int
		cands    []string
		wantLine string
		wantPos  int
		wantOK   bool
	}{
		{
			name:     "fills the common prefix and parks the cursor at its end",
			line:     "/mo",
			cursor:   3,
			cands:    []string{"/model", "/model-x"},
			wantLine: "/model",
			wantPos:  6,
			wantOK:   true,
		},
		{
			name:     "a shorter typed word extends to the common prefix",
			line:     "/m",
			cursor:   2,
			cands:    []string{"/model", "/model-x"},
			wantLine: "/model",
			wantPos:  6,
			wantOK:   true,
		},
		{
			name:     "a longer common prefix extends the typed word",
			line:     "/mo",
			cursor:   3,
			cands:    []string{"/model", "/modelx"},
			wantLine: "/model",
			wantPos:  6,
			wantOK:   true,
		},
		{
			name:     "cursor mid-word: the word ending at the cursor is the target",
			line:     "/modelx",
			cursor:   6,
			cands:    []string{"/model"},
			wantLine: "/modelx",
			wantPos:  6,
			wantOK:   false,
		},
		{
			name:     "the typed word is not a prefix of the common prefix: refuse",
			line:     "/x",
			cursor:   2,
			cands:    []string{"/help", "/hook"},
			wantLine: "/x",
			wantPos:  2,
			wantOK:   false,
		},
		{
			name:     "empty candidate list: no change",
			line:     "/m",
			cursor:   2,
			cands:    nil,
			wantLine: "/m",
			wantPos:  2,
			wantOK:   false,
		},
		{
			name:     "prefix shorter than the typed word: no change",
			line:     "/model",
			cursor:   6,
			cands:    []string{"/m", "/m2"},
			wantLine: "/model",
			wantPos:  6,
			wantOK:   false,
		},
		{
			name:     "cursor in trailing whitespace completes the previous word",
			line:     "fix /mo ",
			cursor:   7,
			cands:    []string{"/model", "/model-x"},
			wantLine: "fix /model ",
			wantPos:  10,
			wantOK:   true,
		},
		{
			name:     "word start is whitespace-bounded (the left word is finished)",
			line:     "fix /mo",
			cursor:   7,
			cands:    []string{"/model", "/model-x"},
			wantLine: "fix /model",
			wantPos:  10,
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLine, gotPos, ok := fillPrefix(tt.line, tt.cursor, tt.cands)
			if gotLine != tt.wantLine || gotPos != tt.wantPos || ok != tt.wantOK {
				t.Errorf("fillPrefix(%q, %d, %v) = (%q, %d, %v), want (%q, %d, %v)",
					tt.line, tt.cursor, tt.cands, gotLine, gotPos, ok, tt.wantLine, tt.wantPos, tt.wantOK)
			}
		})
	}
}

// --- Completions.Tab (the acceptance "completion cycling") ------------------

func TestTabCycling(t *testing.T) {
	cands := []string{"/model", "/model-x", "/model-y"}
	get := func(line string, cursor int) []string { return cands }

	t.Run("first Tab fills the common prefix and shows all candidates", func(t *testing.T) {
		cm := NewCompletions()
		line, pos, row := cm.Tab("/m", 2, get)
		if line != "/model" || pos != 6 {
			t.Errorf("first Tab: line=%q pos=%d, want \"/model\" 6", line, pos)
		}
		if row != strings.Join(cands, "  ") {
			t.Errorf("first Tab row = %q, want %q", row, strings.Join(cands, "  "))
		}
	})

	t.Run("subsequent Tabs cycle the candidate list with wraparound", func(t *testing.T) {
		cm := NewCompletions()
		cm.Tab("/m", 2, get) // first: fill
		line, pos, row := cm.Tab("/model", 6, get)
		if line != "/model-x" || pos != 8 || row != "/model-x" {
			t.Errorf("second Tab = (%q, %d, %q), want (\"/model-x\", 8, \"/model-x\")", line, pos, row)
		}
		line, pos, row = cm.Tab("/model-x", 8, get)
		if line != "/model-y" || pos != 8 || row != "/model-y" {
			t.Errorf("third Tab = (%q, %d, %q), want (\"/model-y\", 8, \"/model-y\")", line, pos, row)
		}
		line, pos, row = cm.Tab("/model-y", 8, get)
		if line != "/model" || pos != 6 || row != "/model" {
			t.Errorf("fourth Tab (wraparound) = (%q, %d, %q), want (\"/model\", 6, \"/model\")", line, pos, row)
		}
	})

	t.Run("no candidates: the line is untouched and no row is offered", func(t *testing.T) {
		cm := NewCompletions()
		line, pos, row := cm.Tab("hello", 5, func(string, int) []string { return nil })
		if line != "hello" || pos != 5 || row != "" {
			t.Errorf("Tab with no candidates = (%q, %d, %q), want (\"hello\", 5, \"\")", line, pos, row)
		}
	})

	t.Run("a changed candidate list resets to a fresh fill", func(t *testing.T) {
		cm := NewCompletions()
		cm.Tab("/m", 2, get)
		other := []string{"/hook"}
		line, pos, row := cm.Tab("/m", 2, func(string, int) []string { return other })
		if line != "/m" || pos != 2 || row != "/hook" {
			t.Errorf("Tab after list change = (%q, %d, %q), want (\"/m\", 2, \"/hook\") — nothing new to fill, just offer the new list", line, pos, row)
		}
	})

	t.Run("Change resets the state so the next Tab re-fills", func(t *testing.T) {
		cm := NewCompletions()
		cm.Tab("/m", 2, get)
		cm.Change()
		line, pos, _ := cm.Tab("/m", 2, get)
		if line != "/model" || pos != 6 {
			t.Errorf("Tab after Change = (%q, %d), want (\"/model\", 6) (a fresh fill, not a cycle)", line, pos)
		}
	})

	t.Run("row caps a long list at 8 candidates plus a marker", func(t *testing.T) {
		var many []string
		for i := 0; i < 12; i++ {
			many = append(many, "c"+itoa(i))
		}
		row := renderCandidateRow(many)
		if !strings.Contains(row, "c0") || !strings.Contains(row, "c7") || strings.Contains(row, "c8") {
			t.Errorf("row = %q, want the first 8 candidates only", row)
		}
		if !strings.Contains(row, "+4") {
			t.Errorf("row = %q, want a \"+4\" marker for the hidden candidates", row)
		}
	})
}

// --- SlashCompleter ---------------------------------------------------------

func TestSlashCompleter(t *testing.T) {
	commands := []string{"/clear", "/compact", "/context", "/help", "/hook", "/model", "/plan", "/quit", "/sessions"}
	s := SlashCompleter{Commands: commands}

	t.Run("a bare slash offers the whole set", func(t *testing.T) {
		got := s.Candidates("/", 1)
		if !reflect.DeepEqual(got, commands) {
			t.Errorf("Candidates(\"/\", 1) = %v, want the full set %v", got, commands)
		}
	})

	t.Run("a prefix narrows the set", func(t *testing.T) {
		got := s.Candidates("/m", 2)
		want := []string{"/model"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/m\", 2) = %v, want %v", got, want)
		}
	})

	t.Run("a non-command line offers nothing", func(t *testing.T) {
		if got := s.Candidates("hello", 5); got != nil {
			t.Errorf("Candidates(\"hello\", 5) = %v, want nil", got)
		}
	})

	t.Run("a mid-sentence slash offers nothing", func(t *testing.T) {
		if got := s.Candidates("see /m", 6); got != nil {
			t.Errorf("Candidates(\"see /m\", 6) = %v, want nil", got)
		}
	})

	t.Run("no prefix match offers nothing", func(t *testing.T) {
		if got := s.Candidates("/z", 2); got != nil {
			t.Errorf("Candidates(\"/z\", 2) = %v, want nil", got)
		}
	})
}

func TestSlashCompleterSub(t *testing.T) {
	commands := []string{"/help", "/model", "/quit"}
	subCalls := 0
	s := SlashCompleter{
		Commands: commands,
		Sub: func(line string, cursor int) []string {
			subCalls++
			return []string{"/model qwen/qwen3-coder:free"}
		},
	}

	t.Run("a known command plus space triggers Sub", func(t *testing.T) {
		got := s.Candidates("/model ", 7)
		want := []string{"/model qwen/qwen3-coder:free"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model \", 7) = %v, want %v", got, want)
		}
		if subCalls == 0 {
			t.Error("Sub was not consulted")
		}
	})

	t.Run("an unknown command plus space does not trigger Sub", func(t *testing.T) {
		before := subCalls
		if got := s.Candidates("/nope ", 6); got != nil {
			t.Errorf("Candidates(\"/nope \", 6) = %v, want nil", got)
		}
		if subCalls != before {
			t.Error("Sub was consulted for an unknown command")
		}
	})

	t.Run("a bare command (no trailing space) offers the command list, not Sub", func(t *testing.T) {
		got := s.Candidates("/model", 6)
		want := []string{"/model"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model\", 6) = %v, want %v", got, want)
		}
	})
}

// --- ModelCompleter ----------------------------------------------------------

func TestModelCompleter(t *testing.T) {
	m := ModelCompleter{Names: func() []string {
		return []string{"qwen/qwen3-coder:free", "tencent/hy3:free"}
	}}

	t.Run("completes after /model ", func(t *testing.T) {
		got := m.Candidates("/model q", 8)
		want := []string{"/model qwen/qwen3-coder:free"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model q\", 8) = %v, want %v", got, want)
		}
	})

	t.Run("an empty argument offers every name", func(t *testing.T) {
		got := m.Candidates("/model ", 7)
		if len(got) != 2 {
			t.Errorf("Candidates(\"/model \", 7) = %v, want both names", got)
		}
	})

	t.Run("an empty argument offers every name when the cursor is past it", func(t *testing.T) {
		got := m.Candidates("/model x", 8)
		if len(got) != 0 {
			t.Errorf("Candidates(\"/model x\", 8) = %v, want none (x matches no name)", got)
		}
	})

	t.Run("a non-model line offers nothing", func(t *testing.T) {
		if got := m.Candidates("/help ", 6); got != nil {
			t.Errorf("Candidates(\"/help \", 6) = %v, want nil", got)
		}
	})

	t.Run("a nil Names func offers nothing", func(t *testing.T) {
		var none ModelCompleter
		if got := none.Candidates("/model ", 7); got != nil {
			t.Errorf("Candidates with nil Names = %v, want nil", got)
		}
	})
}

// --- PathCompleter -----------------------------------------------------------

// writeTree creates a small workspace under root with a nested .gitignore.
func writeTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"src/alpha.go":    "package main\n",
		"src/internal.go": "package main\n",
		"main.go":         "package main\n",
		"notes.txt":       "hello\n",
		"build/out.bin":   "ignored\n",
		"vendor/dep.go":   "package dep\n",
	}
	for path, content := range files {
		p := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	ig := "build/\nvendor/\n"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(ig), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
}

func sortedCands(t *testing.T, line string, cursor int, p PathCompleter) []string {
	t.Helper()
	got := p.Candidates(line, cursor)
	sort.Strings(got)
	return got
}

func TestPathCompleterRefusesOutOfWorkspace(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root)
	p := PathCompleter{Root: root}

	tests := []struct {
		name string
		line string
	}{
		{"dot-dot escape", "@../../etc/passwd"},
		{"dot-dot escape mid-path", "@src/../../../etc/passwd"},
		{"absolute path", "@/etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.Candidates(tt.line, len(tt.line))
			if got != nil {
				t.Errorf("Candidates(%q) = %v, want nil (refused)", tt.line, got)
			}
		})
	}
}

func TestPathCompleterListsWorkspace(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root)
	p := PathCompleter{Root: root}

	t.Run("a bare @ offers the top level", func(t *testing.T) {
		got := sortedCands(t, "@", 1, p)
		// build/ and vendor/ are gitignored; .git is not created by the
		// fixture. The candidates are the complete "@path" mentions.
		want := []string{"@.gitignore", "@main.go", "@notes.txt", "@src/"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@\", 1) = %v, want %v", got, want)
		}
	})

	t.Run("a directory mention lists its children", func(t *testing.T) {
		got := sortedCands(t, "@src/", 5, p)
		want := []string{"@src/alpha.go", "@src/internal.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@src/\", 5) = %v, want %v", got, want)
		}
	})

	t.Run("a partial name under a directory is prefix-matched", func(t *testing.T) {
		got := sortedCands(t, "@src/al", 6, p)
		want := []string{"@src/alpha.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@src/al\", 6) = %v, want %v", got, want)
		}
	})

	t.Run("gitignored paths never appear", func(t *testing.T) {
		got := sortedCands(t, "@build/", 7, p)
		// The directory exists but is gitignored: its children are listed
		// (the user explicitly navigated in), but each child is ignored too.
		if len(got) != 0 {
			t.Errorf("Candidates(\"@build/\", 7) = %v, want none (gitignored)", got)
		}
		got = sortedCands(t, "@", 1, p)
		for _, c := range got {
			if strings.Contains(c, "vendor") || strings.Contains(c, "build") {
				t.Errorf("top-level candidates include a gitignored path: %v", got)
			}
		}
	})

	t.Run("an @ in prose is not a mention", func(t *testing.T) {
		got := p.Candidates("email me at a@b", 15)
		if got != nil {
			t.Errorf("Candidates(\"email me at a@b\", 15) = %v, want nil", got)
		}
	})

	t.Run("a missing file offers the nearest ancestor's matches", func(t *testing.T) {
		got := sortedCands(t, "@src/nonexist", 11, p)
		// "src" exists; "nonexist" is typed under it: no file matches the
		// prefix "nonexist", so nothing is offered.
		if got != nil {
			t.Errorf("Candidates(\"@src/nonexist\", 11) = %v, want nil", got)
		}
		got = sortedCands(t, "@src/alpha", 9, p)
		want := []string{"@src/alpha.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@src/alpha\", 9) = %v, want %v", got, want)
		}
	})
}

func TestPathCompleterSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root)
	p := PathCompleter{Root: root}

	// A symlink inside the workspace that points outside it must not be
	// offered: confinement resolves symlinks before accepting a candidate.
	target := t.TempDir() // a different temp dir, outside root
	if err := os.WriteFile(filepath.Join(target, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got := p.Candidates("@link", 5)
	for _, c := range got {
		if strings.Contains(c, "secret") {
			t.Errorf("Candidates(\"@link\", 5) offers a symlinked escape: %v", got)
		}
	}
}

func TestGitignored(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root)

	tests := []struct {
		path string
		want bool
	}{
		{"build/out.bin", true},
		{"vendor/dep.go", true},
		{"main.go", false},
		{"src/alpha.go", false},
		{".gitignore", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := gitignored(root, filepath.Join(root, tt.path)); got != tt.want {
				t.Errorf("gitignored(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestGitignoreMatch(t *testing.T) {
	tests := []struct {
		rule string
		rel  string
		want bool
	}{
		{"*.log", "a/b/c.log", true},
		{"out.bin", "build/out.bin", true},
		{"build/*", "build/out.bin", true},
		{"build/*", "other/out.bin", false},
		{"foo", "bar", false},
	}
	for _, tt := range tests {
		t.Run(tt.rule+"_"+tt.rel, func(t *testing.T) {
			if got := gitignoreMatch(tt.rule, tt.rel, false); got != tt.want {
				t.Errorf("gitignoreMatch(%q, %q, false) = %v, want %v", tt.rule, tt.rel, got, tt.want)
			}
		})
	}
}
