package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestConfinedPath and TestConfinedPathSymlinkEscape moved to cmd/cortex/tools,
// alongside the unexported confinedPath helper they exercise.

func TestRemovePathTool(t *testing.T) {
	root := t.TempDir()
	cs := &CortexSession{allowDelete: true, deleteRoot: root}
	call := func(p string) (string, error) {
		args, _ := json.Marshal(map[string]string{"path": p})
		return tools.Execute(context.Background(), tc(FunctionRemove, string(args)), cs)
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
		if _, err := tools.Execute(context.Background(), tc(FunctionRemove, string(args)), off); err == nil {
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

	t.Run("tool options merge independently", func(t *testing.T) {
		yes, no := true, false
		base := ToolConfig{
			AllowDelete:        &yes,
			EnableWeb:          &yes,
			EnableContextEvict: &no,
			EnableContextMerge: &yes,
		}
		over := ToolConfig{
			EnableWeb:                     &no,
			EnableContextAdjustWatermarks: &no,
		}
		got := mergeTools(base, over)
		if got.AllowDelete != &yes || got.EnableContextEvict != &no || got.EnableContextMerge != &yes {
			t.Fatalf("base options were lost: %#v", got)
		}
		if got.EnableWeb != &no || got.EnableContextAdjustWatermarks != &no {
			t.Fatalf("override options were not applied: %#v", got)
		}
	})

	t.Run("web tools share one execution gate", func(t *testing.T) {
		no := false
		cs := &CortexSession{Config: &Config{Tools: ToolConfig{EnableWeb: &no}}}
		if cs.IsToolEnabled(tools.FunctionWebSearch) || cs.IsToolEnabled(tools.FunctionFetchURL) {
			t.Fatal("web tools should be disabled")
		}
		if !cs.IsToolEnabled(tools.FunctionReadFile) {
			t.Fatal("unrelated tools should remain enabled")
		}
	})

	t.Run("toolsExcept drops the named tool", func(t *testing.T) {
		out := toolsExcept(toolSet, FunctionRemove)
		for _, tl := range out {
			if tl.Function.Name == FunctionRemove {
				t.Error("remove_path should have been dropped")
			}
		}
		if len(out) != len(toolSet)-1 {
			t.Errorf("len = %d, want %d", len(out), len(toolSet)-1)
		}
	})
}
