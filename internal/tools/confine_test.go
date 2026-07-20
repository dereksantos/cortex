package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/agent"
)

func readCallArgs(m map[string]any) agent.ToolCall {
	b, _ := json.Marshal(m)
	return agent.ToolCall{Function: agent.FunctionCall{Name: FunctionReadFile, Arguments: string(b)}}
}

func TestConfinePathAllowsInRoot(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a.go", "sub/b.go", ".cortex/journal/x.jsonl", "."} {
		call := readCallArgs(map[string]any{"path": p})
		if _, err := ConfinePath(call, root); err != nil {
			t.Errorf("path %q should be allowed in root: %v", p, err)
		}
	}
}

func TestConfinePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"/etc/passwd", "../secret", "../../x", "sub/../../out"} {
		call := readCallArgs(map[string]any{"path": p})
		if _, err := ConfinePath(call, root); err == nil {
			t.Errorf("path %q should be rejected as an escape", p)
		}
	}
}

func TestConfinePathRejectsSymlinkEscapes(t *testing.T) {
	// A symlink planted inside root that resolves outside it must be
	// refused: the lexical Abs+Clean+Rel guard alone never touches the
	// filesystem, so containment has to be re-checked on real paths.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatalf("failed to plant dir symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "filelink")); err != nil {
		t.Fatalf("failed to plant file symlink: %v", err)
	}

	for _, tc := range []struct{ name, path string }{
		{"through symlinked dir", "dirlink/secret.txt"},
		{"symlinked file", "filelink"},
		{"nonexistent under symlinked dir", "dirlink/newfile.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := readCallArgs(map[string]any{"path": tc.path})
			if _, err := ConfinePath(call, root); err == nil {
				t.Errorf("path %q resolves outside root via symlink and should be rejected", tc.path)
			}
		})
	}
}

func TestConfinePathAllowsJournal(t *testing.T) {
	// The journal lives inside .cortex (delete-protected) but reads must reach it.
	root := t.TempDir()
	call := readCallArgs(map[string]any{"path": ".cortex/journal"})
	if _, err := ConfinePath(call, root); err != nil {
		t.Errorf(".cortex/journal must be readable: %v", err)
	}
}

func TestConfinePathNoPathArg(t *testing.T) {
	// grep with default path (no "path") passes through.
	call := agent.ToolCall{Function: agent.FunctionCall{Name: FunctionGrep, Arguments: `{"pattern":"x"}`}}
	if _, err := ConfinePath(call, t.TempDir()); err != nil {
		t.Errorf("a call with no path should pass: %v", err)
	}
}

func TestTargetedReadSmallWhole(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "small.txt")
	os.WriteFile(p, []byte(strings.Repeat("line\n", 10)), 0o644)
	call := readCallArgs(map[string]any{"path": p})
	out, refusal := TargetedRead(call)
	if refusal != "" {
		t.Errorf("a small file should read whole, got refusal: %q", refusal)
	}
	// No range injected → call unchanged.
	if _, ok := out.IntArg("start"); ok {
		t.Errorf("small whole-file read should not be rewritten to a range")
	}
}

func TestTargetedReadLargeRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	os.WriteFile(p, []byte(strings.Repeat("line\n", maxReadLines+50)), 0o644)
	call := readCallArgs(map[string]any{"path": p})
	_, refusal := TargetedRead(call)
	if refusal == "" || !strings.Contains(refusal, "outline") {
		t.Errorf("a large whole-file read should be refused with an outline hint, got %q", refusal)
	}
}

func TestTargetedReadClampsRange(t *testing.T) {
	call := readCallArgs(map[string]any{"path": "f.go", "start": 10, "end": 10000})
	out, refusal := TargetedRead(call)
	if refusal != "" {
		t.Fatalf("a ranged read should clamp, not refuse: %q", refusal)
	}
	start, _ := out.IntArg("start")
	end, _ := out.IntArg("end")
	if start != 10 || end != 10+maxReadLines-1 {
		t.Errorf("range = %d-%d, want 10-%d (clamped to maxReadLines)", start, end, 10+maxReadLines-1)
	}
}

func TestTargetedReadNonReadNoop(t *testing.T) {
	call := grepCall("x", ".")
	out, refusal := TargetedRead(call)
	if refusal != "" || out.Function.Arguments != call.Function.Arguments {
		t.Errorf("TargetedRead must be a no-op for non-read_file calls")
	}
}

func TestReadRangeByteCap(t *testing.T) {
	dir := t.TempDir()
	// 50 lines of ~3 KB each = ~150 KB — a 200-line clamp wouldn't bound it, but
	// the byte cap must (journal JSONL / minified files have huge lines).
	p := filepath.Join(dir, "huge.jsonl")
	line := strings.Repeat("x", 3000) + "\n"
	os.WriteFile(p, []byte(strings.Repeat(line, 50)), 0o644)
	out, err := readRange(headlessDeps{}, p, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > defaultMaxReadBytes+200 { // header + note slack
		t.Errorf("read returned %d bytes, want bounded near defaultMaxReadBytes %d", len(out), defaultMaxReadBytes)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("a huge-line span should carry a truncation note:\n%.200s", out)
	}
}
