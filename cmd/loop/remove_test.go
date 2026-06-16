package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfinedPath(t *testing.T) {
	root := t.TempDir()
	ok := []struct{ in, wantRel string }{
		{"file.go", "file.go"},
		{"sub/dir", "sub/dir"},
		{"./a/b/../c", "a/c"},
	}
	for _, tt := range ok {
		got, err := confinedPath(root, tt.in)
		if err != nil {
			t.Errorf("confinedPath(%q) errored: %v", tt.in, err)
			continue
		}
		if want := filepath.Join(root, tt.wantRel); got != want {
			t.Errorf("confinedPath(%q) = %q, want %q", tt.in, got, want)
		}
	}

	bad := []string{
		"",                  // empty
		".",                 // the root itself
		"..",                // escape up
		"../sibling",        // escape up
		"sub/../../escape",  // traversal escape
		"/etc/passwd",       // absolute outside
		".git",              // protected
		".git/config",       // protected subtree
		".cortex",           // protected
		".cortex/journal/x", // protected subtree
	}
	for _, in := range bad {
		if _, err := confinedPath(root, in); err == nil {
			t.Errorf("confinedPath(%q) should have been refused", in)
		}
	}
}

func TestConfinedPathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// "link/x" is lexically in-root but its real parent is outside.
	if _, err := confinedPath(root, "link/x"); err == nil {
		t.Error("expected symlink-escape refusal for link/x")
	}
}

func TestRemovePathTool(t *testing.T) {
	root := t.TempDir()
	cs := &CortexSession{allowDelete: true, deleteRoot: root}
	call := func(p string) (string, error) {
		args, _ := json.Marshal(map[string]string{"path": p})
		return tc(FunctionRemove, string(args)).Execute(context.Background(), cs)
	}

	t.Run("deletes a file in the workspace", func(t *testing.T) {
		f := filepath.Join(root, "victim.txt")
		os.WriteFile(f, []byte("x"), 0644)
		if _, err := call("victim.txt"); err != nil {
			t.Fatalf("remove failed: %v", err)
		}
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Error("file should be gone")
		}
	})

	t.Run("deletes a directory tree", func(t *testing.T) {
		os.MkdirAll(filepath.Join(root, "pkg/sub"), 0755)
		os.WriteFile(filepath.Join(root, "pkg/sub/a.go"), []byte("x"), 0644)
		if _, err := call("pkg"); err != nil {
			t.Fatalf("remove dir failed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg")); !os.IsNotExist(err) {
			t.Error("dir should be gone")
		}
	})

	t.Run("refuses to escape the workspace", func(t *testing.T) {
		// A real file one level above the workspace must survive an escape attempt.
		outside := filepath.Join(filepath.Dir(root), "keep.txt")
		os.WriteFile(outside, []byte("x"), 0644)
		defer os.Remove(outside)
		for _, esc := range []string{"../keep.txt", "/etc/hosts", ".git", ".cortex/journal"} {
			if _, err := call(esc); err == nil {
				t.Errorf("expected refusal for %q", esc)
			}
		}
		if _, err := os.Stat(outside); err != nil {
			t.Error("outside file must be untouched")
		}
	})

	t.Run("disabled session refuses", func(t *testing.T) {
		off := &CortexSession{allowDelete: false, deleteRoot: root}
		args, _ := json.Marshal(map[string]string{"path": "x"})
		if _, err := tc(FunctionRemove, string(args)).Execute(context.Background(), off); err == nil {
			t.Error("disabled remove_path should error")
		}
	})
}

func TestConfigToolMerges(t *testing.T) {
	t.Run("deleteEnabled defaults true", func(t *testing.T) {
		if !(*Config)(nil).deleteEnabled() {
			t.Error("nil config should default delete enabled")
		}
		no := false
		if (&Config{Tools: ToolConfig{AllowDelete: &no}}).deleteEnabled() {
			t.Error("explicit false should disable")
		}
	})

	t.Run("bashAllowExtra from config and env", func(t *testing.T) {
		t.Setenv("CORTEX_BASH_ALLOW", "make, npm")
		cfg := &Config{Tools: ToolConfig{BashAllow: []string{"docker"}}}
		got := cfg.bashAllowExtra()
		joined := strings.Join(got, ",")
		for _, want := range []string{"docker", "make", " npm"} {
			if !strings.Contains(joined, want) {
				t.Errorf("bashAllowExtra = %v, missing %q", got, want)
			}
		}
	})

	t.Run("toolsExcept drops the named tool", func(t *testing.T) {
		out := toolsExcept(tools, FunctionRemove)
		for _, tl := range out {
			if tl.Function.Name == FunctionRemove {
				t.Error("remove_path should have been dropped")
			}
		}
		if len(out) != len(tools)-1 {
			t.Errorf("len = %d, want %d", len(out), len(tools)-1)
		}
	})
}
