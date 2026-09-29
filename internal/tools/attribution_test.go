package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/agent"
)

// TestMaybeAddAttributionTrailer covers the git-commit trailer backstop:
// the trailer is spliced in right after `commit` (so it precedes a bare `--`
// and pathspecs), skipped when the trailer text is already present, and
// never applied to a chained/piped/substituted command, --amend, or a
// stdin message — those come back unchanged, with a note when a commit is
// still evidently being made. Shell-hostile templates ($, ', backticks)
// must come out single-quoted so bash -c cannot expand them.
func TestMaybeAddAttributionTrailer(t *testing.T) {
	const tr = "Co-Authored-By: Cortex (qwen3-coder)"
	const trQ = "'Co-Authored-By: Cortex (qwen3-coder)'"
	tests := []struct {
		name     string
		command  string
		trailer  string
		want     string
		wantNote bool
	}{
		{
			name:    "plain commit - trailer added after commit",
			command: "git commit",
			trailer: tr,
			want:    "git commit --trailer=" + trQ,
		},
		{
			name:    "-m with double quotes",
			command: `git commit -m "fix: login bug"`,
			trailer: tr,
			want:    `git commit --trailer=` + trQ + ` -m "fix: login bug"`,
		},
		{
			name:    "-m with single quotes and an escaped quote",
			command: `git commit -m 'it'\''s fixed'`,
			trailer: tr,
			want:    `git commit --trailer=` + trQ + ` -m 'it'\''s fixed'`,
		},
		{
			name:    "-- pathspecs - trailer lands before the --",
			command: `git commit -m "fix" -- a.go b.go`,
			trailer: tr,
			want:    `git commit --trailer=` + trQ + ` -m "fix" -- a.go b.go`,
		},
		{
			name:    "pathspecs without -- ",
			command: `git commit a.go -m x`,
			trailer: tr,
			want:    `git commit --trailer=` + trQ + ` a.go -m x`,
		},
		{
			name:    "a different --trailer - ours is still added",
			command: `git commit -m "fix" --trailer="Reviewed-By: x"`,
			trailer: tr,
			want:    `git commit --trailer=` + trQ + ` -m "fix" --trailer="Reviewed-By: x"`,
		},
		{
			name:    "already has the trailer as a flag - untouched",
			command: "git commit -m \"fix\" --trailer=" + trQ,
			trailer: tr,
			want:    "git commit -m \"fix\" --trailer=" + trQ,
		},
		{
			name:    "already has the trailer in the message - untouched",
			command: "git commit -m \"fix\n\n" + tr + "\"",
			trailer: tr,
			want:    "git commit -m \"fix\n\n" + tr + "\"",
		},
		{
			name:    "attribution disabled (empty trailer) - untouched",
			command: `git commit -m "fix"`,
			trailer: "",
			want:    `git commit -m "fix"`,
		},
		{
			name:     "&& chain - untouched, noted",
			command:  `git add -A && git commit -m "fix"`,
			trailer:  tr,
			want:     `git add -A && git commit -m "fix"`,
			wantNote: true,
		},
		{
			name:     "commit then push - untouched, noted",
			command:  `git commit -m "fix"; git push`,
			trailer:  tr,
			want:     `git commit -m "fix"; git push`,
			wantNote: true,
		},
		{
			name:     "pipeline - untouched, noted",
			command:  `git commit -m "fix" | tee log`,
			trailer:  tr,
			want:     `git commit -m "fix" | tee log`,
			wantNote: true,
		},
		{
			name:     "subshell - untouched, noted",
			command:  `(git commit -m "fix")`,
			trailer:  tr,
			want:     `(git commit -m "fix")`,
			wantNote: true,
		},
		{
			name:     "command substitution in the message - untouched, noted",
			command:  `git commit -m "$(cat msg.txt)"`,
			trailer:  tr,
			want:     `git commit -m "$(cat msg.txt)"`,
			wantNote: true,
		},
		{
			name:     "git commit not the first command - untouched, noted",
			command:  `cd sub && git commit -m "fix"`,
			trailer:  tr,
			want:     `cd sub && git commit -m "fix"`,
			wantNote: true,
		},
		{
			name:    "git option before commit - untouched",
			command: `git -C sub commit -m "fix"`,
			trailer: tr,
			want:    `git -C sub commit -m "fix"`,
		},
		{
			name:    "--amend - untouched",
			command: `git commit --amend --no-edit`,
			trailer: tr,
			want:    `git commit --amend --no-edit`,
		},
		{
			name:     "-F - (message on stdin) - untouched, noted",
			command:  `git commit -F -`,
			trailer:  tr,
			want:     `git commit -F -`,
			wantNote: true,
		},
		{
			name:     "heredoc message - untouched, noted",
			command:  "git commit -F - <<'EOF'\nfix\nEOF",
			trailer:  tr,
			want:     "git commit -F - <<'EOF'\nfix\nEOF",
			wantNote: true,
		},
		{
			name:     "unterminated quote - untouched, noted",
			command:  `git commit -m "fix`,
			trailer:  tr,
			want:     `git commit -m "fix`,
			wantNote: true,
		},
		{
			name:    "non-commit git command - untouched",
			command: "git log --grep commit",
			trailer: tr,
			want:    "git log --grep commit",
		},
		{
			name:    "echo mentioning git commit - untouched",
			command: `echo "run git commit later"`,
			trailer: tr,
			want:    `echo "run git commit later"`,
		},
		{
			name:    "template with $, backtick and ' - single-quoted, never expanded",
			command: `git commit -m "fix"`,
			trailer: "Co-Authored-By: $USER `id` O'Brien",
			want:    `git commit --trailer='Co-Authored-By: $USER ` + "`id`" + ` O'\''Brien' -m "fix"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &mockDeps{attribution: &mockAttributionProvider{trailer: tt.trailer}}
			got, note := maybeAddAttributionTrailer(tt.command, deps)
			if got != tt.want {
				t.Errorf("maybeAddAttributionTrailer(%q) = %q, want %q", tt.command, got, tt.want)
			}
			if (note != "") != tt.wantNote {
				t.Errorf("maybeAddAttributionTrailer(%q) note = %q, want note: %v", tt.command, note, tt.wantNote)
			}
			if note != "" && !strings.Contains(note, tt.trailer) {
				t.Errorf("note %q does not name the trailer %q", note, tt.trailer)
			}
		})
	}
}

// TestBashAttributionRewriteBeforeGate pins the ordering: the shell-risk
// gate must classify the rewritten command (the one that would actually
// run), not the model's original.
func TestBashAttributionRewriteBeforeGate(t *testing.T) {
	deps := &mockDeps{
		attribution: &mockAttributionProvider{trailer: "Co-Authored-By: Cortex"},
		gateRefuse:  true,
	}
	tc := bashCall(t, `git commit -m "fix"`)
	if _, err := bash(context.Background(), tc, deps); err != nil {
		t.Fatalf("bash: %v", err)
	}
	want := `git commit --trailer='Co-Authored-By: Cortex' -m "fix"`
	if len(deps.gated) != 1 || deps.gated[0] != want {
		t.Errorf("gate saw %q, want exactly [%q]", deps.gated, want)
	}
}

// TestBashAttributionCommitsForReal runs rewritten commands through bash()
// in a throwaway repo and checks git's view of the result: exactly one
// trailer, the message intact, and a `-- <paths>` commit that still commits
// only the named path (the bug the append-at-end rewrite had). A template
// with shell metacharacters must reach git verbatim.
func TestBashAttributionCommitsForReal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tests := []struct {
		name        string
		command     string
		trailer     string
		wantSubject string
		wantFiles   string // `git show --name-only` file list, sorted by git
	}{
		{
			name:        "-m with quotes",
			command:     `git commit -q -m "it's fixed"`,
			trailer:     "Co-Authored-By: Cortex (m1)",
			wantSubject: "it's fixed",
			wantFiles:   "a.txt\nb.txt",
		},
		{
			name:        "-- pathspec",
			command:     `git commit -q -m "only a" -- a.txt`,
			trailer:     "Co-Authored-By: Cortex (m1)",
			wantSubject: "only a",
			wantFiles:   "a.txt",
		},
		{
			name:        "template with $, backtick and '",
			command:     `git commit -q -m "meta"`,
			trailer:     "Co-Authored-By: $HOME `id` O'Brien",
			wantSubject: "meta",
			wantFiles:   "a.txt\nb.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("init", "-q")
			run("config", "user.name", "Test")
			run("config", "user.email", "test@example.com")
			for _, f := range []string{"a.txt", "b.txt"} {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			run("add", "a.txt", "b.txt")

			deps := &mockDeps{attribution: &mockAttributionProvider{trailer: tt.trailer}, workdir: dir}
			tc := bashCall(t, tt.command)
			out, err := bash(context.Background(), tc, deps)
			if err != nil {
				t.Fatalf("bash: %v", err)
			}
			if strings.Contains(out, "exit error") {
				t.Fatalf("commit failed: %s", out)
			}
			if got := run("log", "-1", "--format=%s"); got != tt.wantSubject {
				t.Errorf("subject = %q, want %q", got, tt.wantSubject)
			}
			body := run("log", "-1", "--format=%B")
			if n := strings.Count(body, tt.trailer); n != 1 {
				t.Errorf("trailer %q appears %d times, want 1:\n%s", tt.trailer, n, body)
			}
			if got := run("show", "--name-only", "--format=", "HEAD"); got != tt.wantFiles {
				t.Errorf("committed files = %q, want %q", got, tt.wantFiles)
			}
		})
	}
}

// bashCall builds a bash tool call for command.
func bashCall(t *testing.T, command string) ToolCall {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return agent.ToolCall{Function: agent.FunctionCall{Name: FunctionBash, Arguments: string(b)}}
}

// TestQuoteShellArg pins the POSIX single-quote contract: the result must be
// inert to bash -c — no $-expansion, backticks, or & in it can ever run. A
// single quote inside the value uses the end-quote / escaped-quote / restart
// idiom ('end' + '\” + 'restart'), not a bare backslash, which would be
// literal inside single quotes and leave the string unterminated.
func TestQuoteShellArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"plain", "'plain'"},
		{"a b", "'a b'"},
		{"a'b", `'a'\''b'`},
		{"$HOME", `'$HOME'`},
		{"`id`", "'`id`'"},
		{"a\"b", `'a"b'`},
		{"$(rm -rf x)", `'$(rm -rf x)'`},
	}
	for _, c := range cases {
		if got := quoteShellArg(c.in); got != c.want {
			t.Errorf("quoteShellArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// mockAttributionProvider returns a fixed trailer ("" = attribution off).
type mockAttributionProvider struct {
	trailer string
}

func (m *mockAttributionProvider) AttributionCommit() string { return m.trailer }

type mockDeps struct {
	attribution AttributionProvider
	gated       []string // every command GateShell was asked to classify
	gateRefuse  bool     // GateShell refuses (nothing runs)
	workdir     string   // Workdirer: where bash runs ("" = process CWD)
}

func (m *mockDeps) Workdir() string { return m.workdir }

func (m *mockDeps) AttributionCommit() string {
	if m.attribution != nil {
		return m.attribution.AttributionCommit()
	}
	return ""
}

func (m *mockDeps) MemoryWrite(name, content, scope string) (string, error) { return "", nil }
func (m *mockDeps) MemoryRead(name, scope string) (string, error)           { return "", nil }
func (m *mockDeps) MemorySearch(query, scope string) (string, error)        { return "", nil }
func (m *mockDeps) MemoryForget(name, scope string) (string, error)         { return "", nil }
func (m *mockDeps) Recall(citation string) (string, error)                  { return "", nil }
func (m *mockDeps) Outline(path string, budget int) (string, error)         { return "", nil }
func (m *mockDeps) SeedBudget() int                                         { return 0 }
func (m *mockDeps) RunSubagent(ctx context.Context, sa Subagent, seed string) (string, error) {
	return "", nil
}
func (m *mockDeps) Summarize(ctx context.Context, text string, role string, max int) (string, bool, error) {
	return "", false, nil
}
func (m *mockDeps) SummarizeText(ctx context.Context, text string, role string, max int) (string, bool, error) {
	return "", false, nil
}
func (m *mockDeps) GateShell(ctx context.Context, command string) (string, bool) {
	m.gated = append(m.gated, command)
	if m.gateRefuse {
		return "refused", false
	}
	return "", true
}
func (m *mockDeps) AllowDelete() (string, bool)                 { return "", false }
func (m *mockDeps) Quiet() bool                                 { return true }
func (m *mockDeps) ValidateToolCall(tc ToolCall) (bool, string) { return true, "" }
func (m *mockDeps) RemoveOutlineEntry(citation string) bool     { return false }
func (m *mockDeps) MergeOutlineEntries(startCitation, endCitation string) (string, error) {
	return "", nil
}
func (m *mockDeps) OutlineLen() int { return 0 }
func (m *mockDeps) AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error) {
	return 0, 0, 0, 0, nil
}
func (m *mockDeps) IsToolEnabled(toolName string) bool { return true }

// ConfigProvider stubs
func (m *mockDeps) MemoryIndexCap() int                 { return 0 }
func (m *mockDeps) UserMemoryIndexCap() int             { return 0 }
func (m *mockDeps) CaptureExcerptCap() int              { return 0 }
func (m *mockDeps) MaxTaskContextChars() int            { return 0 }
func (m *mockDeps) MaxToolOutput() int                  { return 0 }
func (m *mockDeps) MaxToolIterations() int              { return 0 }
func (m *mockDeps) InstructionBytesCap() int            { return 0 }
func (m *mockDeps) MaxServedModelsShown() int           { return 0 }
func (m *mockDeps) FleetDiscoveryTimeout() int          { return 0 }
func (m *mockDeps) PreflightTimeout() int               { return 0 }
func (m *mockDeps) SelfHealEnabled() bool               { return false }
func (m *mockDeps) ToolLimits() Limits                  { return Limits{} }
func (m *mockDeps) TailHighWatermark() int              { return 0 }
func (m *mockDeps) TailDrainWatermark() int             { return 0 }
func (m *mockDeps) OutlineBudget() int                  { return 0 }
func (m *mockDeps) SeedBudgetTokens() int               { return 0 }
func (m *mockDeps) MaxToolOutputBytes() int             { return 0 }
func (m *mockDeps) MaxReadBytes() int                   { return 0 }
func (m *mockDeps) DefaultRangeLines() int              { return 0 }
func (m *mockDeps) MaxRangeLines() int                  { return 0 }
func (m *mockDeps) MaxHits() int                        { return 0 }
func (m *mockDeps) LineCap() int                        { return 0 }
func (m *mockDeps) MaxOutputBytes() int                 { return 0 }
func (m *mockDeps) FetchTimeoutSec() int                { return 0 }
func (m *mockDeps) FetchMaxRedirects() int              { return 0 }
func (m *mockDeps) FetchMaxBodyBytes() int              { return 0 }
func (m *mockDeps) WebSearchDefaultMaxResults() int     { return 0 }
func (m *mockDeps) WebSearchMaximumMaxResults() int     { return 0 }
func (m *mockDeps) EnableWeb() bool                     { return false }
func (m *mockDeps) EnableAgent() bool                   { return false }
func (m *mockDeps) EnableScan() bool                    { return false }
func (m *mockDeps) EnableEffortEscalation() bool        { return false }
func (m *mockDeps) EnableContextEvict() bool            { return false }
func (m *mockDeps) EnableContextMerge() bool            { return false }
func (m *mockDeps) EnableContextAdjustWatermarks() bool { return false }
func (m *mockDeps) EnableDelete() bool                  { return false }
func (m *mockDeps) DeleteRoot() string                  { return "" }
func (m *mockDeps) CurationBudgetTokens() int           { return 0 }
func (m *mockDeps) OutlineDefaultBudget() int           { return 0 }
func (m *mockDeps) ReadDefaultRangeLines() int          { return 0 }
func (m *mockDeps) ReadMaxRangeLines() int              { return 0 }
func (m *mockDeps) ReadMaxReadBytes() int               { return 0 }
func (m *mockDeps) GrepMaxHits() int                    { return 0 }
func (m *mockDeps) GrepLineCap() int                    { return 0 }
func (m *mockDeps) GrepMaxOutputBytes() int             { return 0 }
func (m *mockDeps) FetchURLTimeoutSec() int             { return 0 }
func (m *mockDeps) FetchURLMaxRedirects() int           { return 0 }
func (m *mockDeps) FetchURLMaxBodyBytes() int           { return 0 }
