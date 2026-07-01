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

// TestGrepCentersOnMatch locks the fix for journal recall: a match deep in a very
// long line (minified JSON / JSONL) must stay VISIBLE in the hit, not be truncated
// away with the line's leading metadata. Regression: capLine showed the line's
// first grepLineCap bytes, so a needle at byte 1000 of a 2 KB line vanished.
func TestGrepCentersOnMatch(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 1200) + "NEEDLE" + strings.Repeat("y", 1200)
	padding := strings.Repeat("padding\n", (1<<20)/len("padding\n"))
	os.WriteFile(filepath.Join(dir, "huge.jsonl"), []byte(line+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "large.jsonl"), []byte(padding+"LARGE_NEEDLE\n"), 0o644)
	out, err := grepFiles(context.Background(), dir, mustRe(t, "NEEDLE"), grepMaxHits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NEEDLE") {
		t.Errorf("match deep in a long line must stay visible, got:\n%s", out)
	}
	if !strings.Contains(out, "LARGE_NEEDLE") {
		t.Errorf("match in a large text file must not be skipped, got:\n%s", out)
	}
}

func TestGrepLongJSONLKeepsNearbyFact(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 450) + "AGENTS.md" + strings.Repeat("y", 450) + "seed context" + strings.Repeat("z", 450)
	os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(line+"\n"), 0o644)
	out, err := grepFiles(context.Background(), dir, mustRe(t, "seed context"), grepMaxHits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AGENTS.md") || !strings.Contains(out, "seed context") {
		t.Errorf("nearby facts in long JSONL records must stay visible, got:\n%s", out)
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

func TestGrepJournalBroadPatternRedirect(t *testing.T) {
	for _, pattern := range []string{"seed context", "seed.*context|context.*seed", "repository root"} {
		out, err := grep(context.Background(), grepCall(pattern, ".cortex/journal"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "too broad") || !strings.Contains(out, "Root-file candidate hits") {
			t.Errorf("broad journal grep %q should redirect to a narrower pattern, got %q", pattern, out)
		}
	}
}

func TestGrepJournalBroadPatternReturnsCompactCandidates(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, ".cortex", "journal")
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(journal, "0001.jsonl"), []byte(`{"msg":"uses AGENTS.md as seed context with lots of extra words that should not be returned"}`+"\n"+`{"msg":"unrelated old CLAUDE.md mention"}`+"\n"), 0o644)
	out, err := grep(context.Background(), grepCall("seed context", journal))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0001.jsonl:1:AGENTS.md") || !strings.Contains(out, "summary: AGENTS.md=1") {
		t.Errorf("broad journal redirect should summarize root-file candidates, got %q", out)
	}
	if !strings.Contains(out, "most_frequent_candidate: AGENTS.md") || !strings.Contains(out, "stop and answer") {
		t.Errorf("broad journal redirect should make the bounded answer path explicit, got %q", out)
	}
	if strings.Contains(out, "CLAUDE.md") {
		t.Errorf("broad journal redirect should ignore unrelated candidate lines, got %q", out)
	}
	if strings.Contains(out, "lots of extra words") {
		t.Errorf("broad journal redirect should not return full JSONL context, got %q", out)
	}
}

func TestGrepJournalFilenamePatternAllowed(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, ".cortex", "journal")
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(journal, "0001.jsonl"), []byte(`{"msg":"uses AGENTS.md as context"}`+"\n"), 0o644)
	out, err := grep(context.Background(), grepCall(`[A-Za-z0-9_.-]+\.(go|md|json|yaml|toml)`, journal))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Errorf("filename-shaped journal grep should run, got %q", out)
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
