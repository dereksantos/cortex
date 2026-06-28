package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/agent"
)

func grepCall(pattern, path string) agent.ToolCall {
	args, _ := json.Marshal(map[string]any{"pattern": pattern, "path": path})
	return agent.ToolCall{Function: agent.FunctionCall{Name: FunctionGrep, Arguments: string(args)}}
}

func TestGrepFindsMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc Resolve() {}\nvar x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := grep(context.Background(), grepCall("func.*Resolve", dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go:2:") || !strings.Contains(out, "func Resolve") {
		t.Errorf("grep output = %q, want a.go:2 with the match", out)
	}
}

func TestGrepNoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("nothing here\n"), 0o644)
	out, _ := grep(context.Background(), grepCall("zzz_no_such", dir))
	if !strings.Contains(out, "no matches") {
		t.Errorf("expected (no matches), got %q", out)
	}
}

func TestGrepCap(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < grepMaxHits+50; i++ {
		b.WriteString("match here\n")
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(b.String()), 0o644)
	out, err := grepFiles(context.Background(), dir, mustRe(t, "match"), grepMaxHits)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "big.txt:"); n > grepMaxHits {
		t.Errorf("got %d hits, want <= cap %d", n, grepMaxHits)
	}
	if !strings.Contains(out, "capped at") {
		t.Errorf("expected a capped note:\n%s", out)
	}
}

func TestGrepBinarySkip(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bin"), []byte("match\x00match\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "text.txt"), []byte("match\n"), 0o644)
	out, _ := grepFiles(context.Background(), dir, mustRe(t, "match"), grepMaxHits)
	if strings.Contains(out, "bin:") {
		t.Errorf("binary file should be skipped:\n%s", out)
	}
	if !strings.Contains(out, "text.txt:") {
		t.Errorf("text file should match:\n%s", out)
	}
}

func TestGrepRespectsIgnoreSet(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "secret.txt"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("needle\n"), 0o644)
	out, _ := grepFiles(context.Background(), dir, mustRe(t, "needle"), grepMaxHits)
	if strings.Contains(out, ".git/") {
		t.Errorf(".git should be ignored:\n%s", out)
	}
	if !strings.Contains(out, "visible.txt:") {
		t.Errorf("visible file should match:\n%s", out)
	}
}

func TestGrepBadRegexIsObservation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644)
	// A PCRE lookahead — invalid in RE2. Must come back as an observation the
	// model can fix, not a tool error.
	out, err := grep(context.Background(), grepCall("(?=foo)", dir))
	if err != nil {
		t.Fatalf("bad regex should be an observation, not an error: %v", err)
	}
	if !strings.Contains(out, "invalid regex") {
		t.Errorf("expected an invalid-regex observation, got %q", out)
	}
}

func TestGrepSingleFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "one.go")
	os.WriteFile(p, []byte("alpha\nbeta\n"), 0o644)
	out, _ := grep(context.Background(), grepCall("beta", p))
	if !strings.Contains(out, "one.go:2:beta") {
		t.Errorf("single-file grep = %q", out)
	}
}

func mustRe(t *testing.T, p string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
