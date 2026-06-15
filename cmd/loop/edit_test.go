package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runEdit invokes the edit_file tool with raw JSON args and returns its result.
func runEdit(args map[string]any) (string, error) {
	b, _ := json.Marshal(args)
	return tc(FunctionEditFile, string(b)).Execute(context.Background(), nil)
}

func TestEditFileWhitespaceTolerant(t *testing.T) {
	t.Run("leading indentation may differ", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.go")
		// File is tab-indented; the model supplies space-indented old_string.
		os.WriteFile(path, []byte("func f() {\n\treturn 1\n}\n"), 0644)

		if _, err := runEdit(map[string]any{
			"path": path, "old_string": "    return 1", "new_string": "    return 2",
		}); err != nil {
			t.Fatalf("tolerant edit failed: %v", err)
		}
		got, _ := os.ReadFile(path)
		// Replacement is re-indented to the file's actual (tab) indentation.
		if want := "func f() {\n\treturn 2\n}\n"; string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("multi-line block with wrong indent re-indents new text", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.go")
		os.WriteFile(path, []byte("if x {\n\t\ta := 1\n\t\tb := 2\n}\n"), 0644)

		_, err := runEdit(map[string]any{
			"path":       path,
			"old_string": "a := 1\nb := 2",   // no indentation at all
			"new_string": "a := 10\nb := 20", // model writes it flat
		})
		if err != nil {
			t.Fatalf("tolerant multi-line edit failed: %v", err)
		}
		got, _ := os.ReadFile(path)
		if want := "if x {\n\t\ta := 10\n\t\tb := 20\n}\n"; string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("exact match wins over tolerant", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		os.WriteFile(path, []byte("  keep\nkeep\n"), 0644)
		// "keep" exact-matches line 2 once (line 1 is "  keep"); exact path used.
		if _, err := runEdit(map[string]any{
			"path": path, "old_string": "\nkeep\n", "new_string": "\ndone\n",
		}); err != nil {
			t.Fatalf("edit failed: %v", err)
		}
		got, _ := os.ReadFile(path)
		if want := "  keep\ndone\n"; string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestEditFileReplaceAll(t *testing.T) {
	t.Run("replaces every occurrence", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		os.WriteFile(path, []byte("a a a"), 0644)
		out, err := runEdit(map[string]any{
			"path": path, "old_string": "a", "new_string": "b", "replace_all": true,
		})
		if err != nil {
			t.Fatalf("replace_all failed: %v", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "b b b" {
			t.Errorf("got %q, want %q", got, "b b b")
		}
		if !strings.Contains(out, "3 replacement") {
			t.Errorf("result %q should report 3 replacements", out)
		}
	})

	t.Run("without replace_all ambiguity still errors", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		os.WriteFile(path, []byte("a a a"), 0644)
		if _, err := runEdit(map[string]any{"path": path, "old_string": "a", "new_string": "b"}); err == nil {
			t.Fatal("expected ambiguity error without replace_all")
		}
	})
}

func TestEditFileMultiEdit(t *testing.T) {
	t.Run("applies edits in order, atomically", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.go")
		os.WriteFile(path, []byte("var a = 1\nvar b = 2\n"), 0644)

		out, err := runEdit(map[string]any{
			"path": path,
			"edits": []map[string]any{
				{"old_string": "var a = 1", "new_string": "var a = 10"},
				{"old_string": "var b = 2", "new_string": "var b = 20"},
			},
		})
		if err != nil {
			t.Fatalf("multi-edit failed: %v", err)
		}
		got, _ := os.ReadFile(path)
		if want := "var a = 10\nvar b = 20\n"; string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if !strings.Contains(out, "2 edits") {
			t.Errorf("result %q should report 2 edits", out)
		}
	})

	t.Run("a failing edit leaves the file untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.go")
		original := "var a = 1\nvar b = 2\n"
		os.WriteFile(path, []byte(original), 0644)

		_, err := runEdit(map[string]any{
			"path": path,
			"edits": []map[string]any{
				{"old_string": "var a = 1", "new_string": "var a = 10"},
				{"old_string": "does not exist", "new_string": "x"},
			},
		})
		if err == nil {
			t.Fatal("expected error from the second edit")
		}
		if !strings.Contains(err.Error(), "edit 2") {
			t.Errorf("error %q should identify the failing edit", err)
		}
		if got, _ := os.ReadFile(path); string(got) != original {
			t.Errorf("file must be untouched, got %q", got)
		}
	})
}

func TestEditFileNearMissHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.go")
	os.WriteFile(path, []byte("func handleRequest(w http.ResponseWriter) {\n}\n"), 0644)

	// Same words, different exact text — should not match, but should hint.
	_, err := runEdit(map[string]any{
		"path": path, "old_string": "func handleRequest(w http.Response) {", "new_string": "x",
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "closest is line 1") {
		t.Errorf("error %q should point at the closest line", err)
	}
}
