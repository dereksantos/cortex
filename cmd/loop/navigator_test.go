package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/cmd/loop/tools"
)

func TestNavSeedIncludesGoalPathMap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "srv.go")
	if err := os.WriteFile(p, []byte("package p\n\nfunc Start() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := navSeed(p, "how does startup work")
	for _, want := range []string{"GOAL: how does startup work", "PATH: " + p, "MAP", "Start"} {
		if !strings.Contains(seed, want) {
			t.Errorf("seed missing %q; got:\n%s", want, seed)
		}
	}
}

func TestNavMapBounded(t *testing.T) {
	dir := t.TempDir()
	// A file with far more declarations than the budget can hold.
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&b, "func F%d() {}\n", i)
	}
	p := filepath.Join(dir, "big.go")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	m := navMap(p)
	if len(m) > navMapBudget+128 {
		t.Errorf("navMap not bounded: %d bytes (budget %d)", len(m), navMapBudget)
	}
	if !strings.Contains(m, "truncated") {
		t.Errorf("oversized map should note truncation; got tail: %q", m[max(0, len(m)-80):])
	}
}

func TestNavToolRefusesDisallowed(t *testing.T) {
	cs := &CortexSession{}
	call := ToolCall{Function: tools.FunctionCall{Name: tools.FunctionBash, Arguments: `{"command":"rm -rf /"}`}}
	out := cs.runNavTool(context.Background(), call)
	if !strings.Contains(out, "not available to the navigator") {
		t.Errorf("disallowed tool should be refused, got: %q", out)
	}
}

func TestNavToolAllowsRangedRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := &CortexSession{}
	call := ToolCall{Function: tools.FunctionCall{
		Name: tools.FunctionReadFile, Arguments: fmt.Sprintf(`{"path":%q,"start":2,"end":3}`, p),
	}}
	out := cs.runNavTool(context.Background(), call)
	if !strings.Contains(out, "b\nc") || strings.Contains(out, "Error") {
		t.Errorf("ranged read through navigator failed: %q", out)
	}
}

func TestNavigateDisabledByDefault(t *testing.T) {
	t.Setenv("CORTEX_STUDY_NAV", "") // empty == disabled
	cs := &CortexSession{}
	_, ok, err := cs.Navigate(context.Background(), ".", "")
	if ok || err != nil {
		t.Errorf("navigator should be off by default; got ok=%v err=%v", ok, err)
	}
}
