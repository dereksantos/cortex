package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
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
	out := cs.runNavTool(context.Background(), call, 0)
	if !strings.Contains(out, "not available to the navigator") {
		t.Errorf("disallowed tool should be refused, got: %q", out)
	}
}

func TestNavStudySpawnDepthCapped(t *testing.T) {
	// study is allowed, but spawning at the depth cap is refused (no runaway
	// recursion) — and the refusal doesn't need a live model.
	cs := &CortexSession{}
	call := ToolCall{Function: tools.FunctionCall{Name: tools.FunctionStudy, Arguments: `{"path":"cmd/loop","goal":"x"}`}}
	out := cs.runNavTool(context.Background(), call, navMaxDepth)
	if !strings.Contains(out, "max study depth") {
		t.Errorf("study at the depth cap should be refused, got: %q", out)
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
	out := cs.runNavTool(context.Background(), call, 0)
	if !strings.Contains(out, "b\nc") || strings.Contains(out, "Error") {
		t.Errorf("ranged read through navigator failed: %q", out)
	}
}

// TestClampNavRead checks the per-read clamp that keeps one navigator read_file
// from pulling a huge swath (which would blow the read budget on a single call).
func TestClampNavRead(t *testing.T) {
	readArg := func(c ToolCall) (start, end int) {
		s, _ := c.IntArg("start")
		e, _ := c.IntArg("end")
		return s, e
	}
	t.Run("wide range is clamped", func(t *testing.T) {
		c := clampNavRead(ToolCall{Function: tools.FunctionCall{
			Name: tools.FunctionReadFile, Arguments: `{"path":"x.go","start":1,"end":3000}`,
		}})
		if s, e := readArg(c); s != 1 || e != navReadLines {
			t.Errorf("want 1..%d, got %d..%d", navReadLines, s, e)
		}
	})
	t.Run("tight range is left alone", func(t *testing.T) {
		c := clampNavRead(ToolCall{Function: tools.FunctionCall{
			Name: tools.FunctionReadFile, Arguments: `{"path":"x.go","start":40,"end":92}`,
		}})
		if s, e := readArg(c); s != 40 || e != 92 {
			t.Errorf("tight range should be unchanged, got %d..%d", s, e)
		}
	})
	t.Run("start-only is bounded to the cap", func(t *testing.T) {
		c := clampNavRead(ToolCall{Function: tools.FunctionCall{
			Name: tools.FunctionReadFile, Arguments: `{"path":"x.go","start":10}`,
		}})
		if s, e := readArg(c); s != 10 || e != 10+navReadLines-1 {
			t.Errorf("want 10..%d, got %d..%d", 10+navReadLines-1, s, e)
		}
	})
	t.Run("whole-file read passes through (size gate handles it)", func(t *testing.T) {
		in := ToolCall{Function: tools.FunctionCall{
			Name: tools.FunctionReadFile, Arguments: `{"path":"x.go"}`,
		}}
		if got := clampNavRead(in); got.Function.Arguments != in.Function.Arguments {
			t.Errorf("whole-file read should be unchanged, got %q", got.Function.Arguments)
		}
	})
	t.Run("non-read_file call is untouched", func(t *testing.T) {
		in := ToolCall{Function: tools.FunctionCall{
			Name: tools.FunctionProjectIndex, Arguments: `{"path":"."}`,
		}}
		if got := clampNavRead(in); got.Function.Arguments != in.Function.Arguments {
			t.Errorf("non-read_file call should be unchanged, got %q", got.Function.Arguments)
		}
	})
}

// TestNavReadActuallyClamped drives a wide range through runNavTool against a
// real file and confirms only navReadLines lines come back, not the whole range.
func TestNavReadActuallyClamped(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	p := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := &CortexSession{}
	call := ToolCall{Function: tools.FunctionCall{
		Name: tools.FunctionReadFile, Arguments: fmt.Sprintf(`{"path":%q,"start":1,"end":1000}`, p),
	}}
	out := cs.runNavTool(context.Background(), call, 0)
	if strings.Contains(out, "Error") {
		t.Fatalf("clamped read errored: %q", out)
	}
	// The 200th line is included; the 201st is not (clamped to navReadLines=200).
	if !strings.Contains(out, "line 200") {
		t.Errorf("expected the clamped window to reach line %d", navReadLines)
	}
	if strings.Contains(out, "line 201") {
		t.Errorf("read was not clamped — line beyond the cap leaked: %q", out[len(out)-60:])
	}
}
