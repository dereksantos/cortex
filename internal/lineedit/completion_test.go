package lineedit

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- commonPrefix / firstFill (the acceptance "completion fill") ------------

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

func TestFirstFill(t *testing.T) {
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
			// A single candidate splices the word in place — even one whose
			// text is NOT a prefix-extension of the typed word — so a
			// mid-word cursor with exactly one match still fills (and the
			// cursor parks at the end of the replaced word).
			name:     "mid-word cursor with one candidate: the word is replaced",
			line:     "/modelx",
			cursor:   6,
			cands:    []string{"/model"},
			wantLine: "/modelx",
			wantPos:  6,
			wantOK:   true,
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
		{
			// Single candidate: the "single match fills" contract — the
			// candidate is spliced in place of the WHOLE word (the Completer
			// contract), even when it is not a textual extension of the typed
			// word: "@sr/al" resolves through the directory "src" to
			// "@src/alpha.go" on a single Tab, and the buffer keeps the real
			// mention. (When the word and candidate share no prefix at all the
			// fill is still a clean word splice, so the cursor lands at the
			// end of the word — there is no prefix to stop at.)
			name:     "a single candidate fills in place of the word, even across a resolved level",
			line:     "@sr/al",
			cursor:   6,
			cands:    []string{"@src/alpha.go"},
			wantLine: "@src/alpha.go",
			wantPos:  13,
			wantOK:   true,
		},
		{
			// A single candidate splices the word in place; surrounding text
			// on both sides survives.
			name:     "a single candidate keeps the surrounding text",
			line:     "see @src/al, ok",
			cursor:   11,
			cands:    []string{"@src/alpha.go"},
			wantLine: "see @src/alpha.go, ok",
			wantPos:  17,
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLine, gotPos, ok := firstFill(tt.line, tt.cursor, tt.cands)
			if gotLine != tt.wantLine || gotPos != tt.wantPos || ok != tt.wantOK {
				t.Errorf("firstFill(%q, %d, %v) = (%q, %d, %v), want (%q, %d, %v)",
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
		if line != "/model" || pos != 6 || row != "/model" {
			t.Errorf("second Tab = (%q, %d, %q), want (\"/model\", 6, \"/model\") — the FIRST candidate, not a skip", line, pos, row)
		}
		line, pos, row = cm.Tab("/model", 6, get)
		if line != "/model-x" || pos != 8 || row != "/model-x" {
			t.Errorf("third Tab = (%q, %d, %q), want (\"/model-x\", 8, \"/model-x\")", line, pos, row)
		}
		line, pos, row = cm.Tab("/model-x", 8, get)
		if line != "/model-y" || pos != 8 || row != "/model-y" {
			t.Errorf("fourth Tab = (%q, %d, %q), want (\"/model-y\", 8, \"/model-y\")", line, pos, row)
		}
		line, pos, row = cm.Tab("/model-y", 8, get)
		if line != "/model" || pos != 6 || row != "/model" {
			t.Errorf("fifth Tab (wraparound) = (%q, %d, %q), want (\"/model\", 6, \"/model\")", line, pos, row)
		}
	})

	t.Run("cycling splices the candidate in place of the mention word, keeping surrounding text", func(t *testing.T) {
		// The word-level contract: candidates replace the word ending at the
		// cursor; text before and after the word survives every cycle Tab.
		// Path candidates carry the "@" marker (a true replacement for the
		// typed @-word), so the mention stays a mention end to end.
		pathCands := []string{"@src/main.go", "@src/notes.md"}
		getPath := func(line string, cursor int) []string { return pathCands }

		cm := NewCompletions()
		line, pos, _ := cm.Tab("see @src/", 9, getPath) // first: no common prefix beyond "@src/" → no fill
		if line != "see @src/" || pos != 9 {
			t.Errorf("first Tab: line=%q pos=%d, want unchanged (no common prefix to fill)", line, pos)
		}
		line, pos, row := cm.Tab(line, pos, getPath) // cycle 1: first candidate
		if line != "see @src/main.go" || pos != 16 || row != "@src/main.go" {
			t.Errorf("cycle Tab = (%q, %d, %q), want (\"see @src/main.go\", 16, \"@src/main.go\") — \"see \" must survive", line, pos, row)
		}
		line, pos, row = cm.Tab(line, pos, getPath) // cycle 2: second
		if line != "see @src/notes.md" || pos != 17 || row != "@src/notes.md" {
			t.Errorf("cycle Tab = (%q, %d, %q), want (\"see @src/notes.md\", 17, \"@src/notes.md\")", line, pos, row)
		}
		line, pos, _ = cm.Tab(line, pos, getPath) // wraparound
		if line != "see @src/main.go" || pos != 16 {
			t.Errorf("wraparound Tab = (%q, %d), want (\"see @src/main.go\", 16)", line, pos)
		}
	})

	t.Run("text after the mention word also survives cycling", func(t *testing.T) {
		pathCands := []string{"@src/main.go", "@src/notes.md"}
		getPath := func(line string, cursor int) []string { return pathCands }

		// Drive from a known filled state: the mention word "@src/" with text
		// after it; every cycle Tab must rewrite only the word. The cursor
		// sits at the END of the mention word (pos 9, right after "@src/"),
		// so the engine's word boundary finds "@src/" — not the following
		// prose.
		cm := NewCompletions()
		cm.Change()
		line, pos, _ := cm.Tab("see @src/ and @x", 9, getPath) // first: no common prefix beyond "@src/" — just offer
		if line != "see @src/ and @x" || pos != 9 {
			t.Errorf("first Tab: line=%q pos=%d, want unchanged (no new prefix to fill)", line, pos)
		}
		line, pos, _ = cm.Tab(line, pos, getPath) // cycle 1
		if line != "see @src/main.go and @x" || pos != 16 {
			t.Errorf("cycle Tab = (%q, %d), want (\"see @src/main.go and @x\", 16) — text after the word must survive", line, pos)
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
		// The list changed to a SINGLE candidate: the reset re-fills with the
		// single-match contract (the candidate splices the word in place), and
		// the row shows the new list.
		if line != "/hook" || pos != 5 || row != "/hook" {
			t.Errorf("Tab after list change = (%q, %d, %q), want (\"/hook\", 5, \"/hook\") — single candidate splices the word", line, pos, row)
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
			// Word-level contract: the bare model id replaces the argument
			// word; the "/model " prefix stays in the line untouched.
			return []string{"qwen/qwen3-coder:free"}
		},
	}

	t.Run("a known command plus space triggers Sub", func(t *testing.T) {
		got := s.Candidates("/model ", 7)
		want := []string{"qwen/qwen3-coder:free"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model \", 7) = %v, want %v", got, want)
		}
		if subCalls == 0 {
			t.Error("Sub was not consulted")
		}
	})

	t.Run("a known command plus space with a typed argument narrows Sub", func(t *testing.T) {
		before := subCalls
		got := s.Candidates("/model q", 8)
		want := []string{"qwen/qwen3-coder:free"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model q\", 8) = %v, want %v", got, want)
		}
		if subCalls == before {
			t.Error("Sub was not consulted for the argument position")
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
		before := subCalls
		got := s.Candidates("/model", 6)
		want := []string{"/model"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model\", 6) = %v, want %v", got, want)
		}
		if subCalls != before {
			t.Error("Sub was consulted for a bare command")
		}
	})
}

// --- ModelCompleter ----------------------------------------------------------

func TestModelCompleter(t *testing.T) {
	m := ModelCompleter{Names: func() []string {
		return []string{"qwen/qwen3-coder:free", "tencent/hy3:free"}
	}}

	t.Run("completes after /model with the bare id (word-level contract)", func(t *testing.T) {
		got := m.Candidates("/model q", 8)
		want := []string{"qwen/qwen3-coder:free"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model q\", 8) = %v, want %v (the bare id — it replaces the argument word)", got, want)
		}
	})

	t.Run("an empty argument offers every name as a bare id", func(t *testing.T) {
		got := m.Candidates("/model ", 7)
		want := []string{"qwen/qwen3-coder:free", "tencent/hy3:free"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"/model \", 7) = %v, want %v", got, want)
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

// TestModelCandidateFill drives the real engine against the /model
// continuation: the candidates are bare ids (word-level), so the first Tab
// fills the common model-id prefix INTO the argument word, leaving "/model
// " intact — and every candidate appears exactly once (the single-source
// wiring cmd/cortex uses; the old double source listed each id twice).
func TestModelCandidateFill(t *testing.T) {
	nameCands := []string{"qwen/qwen3-coder:free", "qwen/qwen3-coder-pro:free"}
	get := func(line string, cursor int) []string { return nameCands }

	// "/model q" + first Tab: common prefix of the two ids is "qwen/qwen3-"
	// ... wait, the common prefix of "qwen/qwen3-coder:free" and
	// "qwen/qwen3-coder-pro:free" is "qwen/qwen3-coder" — fill splices it in
	// place of "q".
	cm := NewCompletions()
	line, pos, _ := cm.Tab("/model q", 8, get)
	if line != "/model qwen/qwen3-coder" || pos != 23 {
		t.Errorf("first Tab = (%q, %d), want (\"/model qwen/qwen3-coder\", 23) — the id prefix fills the argument, /model untouched", line, pos)
	}
	// Cycle: first candidate, spliced in place of the argument word.
	line, pos, _ = cm.Tab(line, pos, get)
	if line != "/model qwen/qwen3-coder:free" || pos != 28 {
		t.Errorf("cycle Tab = (%q, %d), want the first candidate spliced in place", line, pos)
	}

	// No duplicates when both sources of a mis-wired map are the SAME id list:
	// the engine's merge would double them — the single-source wiring is what
	// prevents that, and the first-Tab fill (above) is the behavior that only
	// works when candidates are word-level.
	seen := map[string]bool{}
	for _, c := range nameCands {
		if seen[c] {
			t.Errorf("duplicate candidate %q", c)
		}
		seen[c] = true
	}
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
		"debug.log":       "log\n",
		"a/x.log":         "log\n",
		"a/keep.log":      "kept\n",
		"keep.log":        "kept\n",
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
	ig := "build/\nvendor/\n*.log\n!keep.log\n"
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
		// build/ and vendor/ are gitignored (directory rules); *.log hides
		// debug.log (non-directory rule) but NOT the rest of the tree — the
		// old bug where one *.log rule hid every path. a/ and keep.log are
		// offered; every candidate carries the "@" marker exactly once (a
		// candidate is a true replacement for the whole typed @-word, so the
		// buffer always holds a real mention).
		want := []string{"@.gitignore", "@a/", "@keep.log", "@main.go", "@notes.txt", "@src/"}
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

	t.Run("a partial name at the root is prefix-matched", func(t *testing.T) {
		// Root-level partial: relAt is bare "@" (no trailing separator), so
		// the candidate is "@main.go", not "@/main.go".
		got := sortedCands(t, "@ma", 3, p)
		want := []string{"@main.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@ma\", 3) = %v, want %v", got, want)
		}
		got = sortedCands(t, "@no", 3, p)
		want = []string{"@notes.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@no\", 3) = %v, want %v", got, want)
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
			// Candidates carry the "@" marker: strip it so the gitignore
			// shape checks below apply to the path itself.
			path := strings.TrimPrefix(c, "@")
			if strings.Contains(path, "vendor") || strings.Contains(path, "build") || strings.HasSuffix(path, ".log") && !strings.HasPrefix(path, "keep.log") {
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

	t.Run("a missing directory spanning levels is descended into, not faked", func(t *testing.T) {
		// The typed tail spans two missing levels ("src" exists,
		// "src/nope" doesn't): the completer must descend the tail segment
		// by segment — no directory matches "nope", so nothing is offered,
		// and in particular no candidate may re-emit the typed missing
		// segments ("@src/nope/al/..." — a path that does not exist).
		got := sortedCands(t, "@src/nope/al", 11, p)
		if got != nil {
			t.Errorf("Candidates(\"@src/nope/al\", 11) = %v, want nil", got)
		}
		// A matching FIRST segment descends: "@sr" matches the directory
		// "src", and the remaining tail "al" is completed under it. The
		// descent returns only the deeper candidates — the directory itself
		// is not offered (it would discard the "al" typed after the slash) —
		// so a single real match fills on the first Tab.
		got = sortedCands(t, "@sr/al", 5, p)
		want := []string{"@src/alpha.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@sr/al\", 5) = %v, want %v", got, want)
		}
	})

	t.Run("descent covers sibling directories, not just the first match", func(t *testing.T) {
		// With both "scripts" and "src" present, "@s/al" must descend into
		// EVERY directory whose name starts with "s" — the earlier sibling
		// ("scripts") that gives no match must not hide the real match in
		// "src".
		if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
			t.Fatalf("mkdir scripts: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "scripts", "check.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write scripts/check.sh: %v", err)
		}
		got := sortedCands(t, "@s/al", 5, p)
		want := []string{"@src/alpha.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(\"@s/al\", 5) = %v, want %v", got, want)
		}
	})

	t.Run("acceptance splices the candidate in place of the @-word", func(t *testing.T) {
		// The word-level contract, end-to-end: with "@src/" typed, the
		// candidates are the src children (each carrying the "@" marker);
		// the first Tab offers them, the cycle Tabs splice them in place of
		// the @-word, keeping "see " — and the mention stays a mention.
		cands := p.Candidates("see @src/", 9)
		want := []string{"@src/alpha.go", "@src/internal.go"}
		if !reflect.DeepEqual(sortedCands(t, "@src/", 5, p), want) {
			t.Fatalf("candidates = %v, want %v (sanity)", cands, want)
		}
		cm := NewCompletions()
		line, pos, _ := cm.Tab("see @src/", 9, func(string, int) []string { return cands }) // first: no common prefix beyond "@src/" → no fill
		if line != "see @src/" || pos != 9 {
			t.Errorf("first Tab = (%q, %d), want unchanged (no common prefix to fill)", line, pos)
		}
		line, pos, _ = cm.Tab(line, pos, func(string, int) []string { return cands }) // cycle 1: first candidate
		if line != "see @src/alpha.go" || pos != 17 {
			t.Errorf("cycle Tab = (%q, %d), want (\"see @src/alpha.go\", 17)", line, pos)
		}
		line, pos, _ = cm.Tab(line, pos, func(string, int) []string { return cands }) // cycle 2: second
		if line != "see @src/internal.go" || pos != 20 {
			t.Errorf("cycle Tab = (%q, %d), want (\"see @src/internal.go\", 20)", line, pos)
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
		// Non-directory rule: *.log hides the .log files only — NOT every
		// path in the tree (the old bug: one *.log rule hid everything,
		// which is why @-completion offered nothing in a repo whose
		// .gitignore has *.py[cod]).
		{"debug.log", true},
		{"a/x.log", true},
		// Negation: !keep.log un-hides keep.log despite *.log.
		{"keep.log", false},
		{"a/keep.log", false},
		// A directory rule does not match a FILE named like the directory.
		// (writeTree's build/ is a directory; a file named "build" would not
		// be ignored by the "build/" rule alone — covered here via the
		// pattern half: build/out.bin is ignored by POSITION, and the dir
		// itself is ignored by name.)
		{"build", true}, // the directory itself (gitignoreMatch on the dir)
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := gitignored(root, filepath.Join(root, tt.path)); got != tt.want {
				t.Errorf("gitignored(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}

	// A directory rule must not match a file named like the directory:
	// "build/" in .gitignore does not ignore a FILE called "build".
	root2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(root2, "build"), []byte("a file named build\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root2, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := gitignored(root2, filepath.Join(root2, "build")); got {
		t.Errorf("gitignored on a FILE named build = true, want false (the \"build/\" rule is dirs-only)")
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
