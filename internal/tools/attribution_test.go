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
	"github.com/dereksantos/cortex/internal/journal"
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
		name        string
		command     string
		trailer     string
		want        string
		wantNote    bool
		wantOutcome string
	}{
		{
			name:        "plain commit - trailer added after commit",
			wantOutcome: outcomeAdded,
			command:     "git commit",
			trailer:     tr,
			want:        "git commit --trailer=" + trQ,
		},
		{
			name:        "-m with double quotes",
			wantOutcome: outcomeAdded,
			command:     `git commit -m "fix: login bug"`,
			trailer:     tr,
			want:        `git commit --trailer=` + trQ + ` -m "fix: login bug"`,
		},
		{
			name:        "-m with single quotes and an escaped quote",
			wantOutcome: outcomeAdded,
			command:     `git commit -m 'it'\''s fixed'`,
			trailer:     tr,
			want:        `git commit --trailer=` + trQ + ` -m 'it'\''s fixed'`,
		},
		{
			name:        "-- pathspecs - trailer lands before the --",
			wantOutcome: outcomeAdded,
			command:     `git commit -m "fix" -- a.go b.go`,
			trailer:     tr,
			want:        `git commit --trailer=` + trQ + ` -m "fix" -- a.go b.go`,
		},
		{
			name:        "pathspecs without -- ",
			wantOutcome: outcomeAdded,
			command:     `git commit a.go -m x`,
			trailer:     tr,
			want:        `git commit --trailer=` + trQ + ` a.go -m x`,
		},
		{
			name:        "a different --trailer - ours is still added",
			wantOutcome: outcomeAdded,
			command:     `git commit -m "fix" --trailer="Reviewed-By: x"`,
			trailer:     tr,
			want:        `git commit --trailer=` + trQ + ` -m "fix" --trailer="Reviewed-By: x"`,
		},
		{
			name:        "already has the trailer as a flag - untouched",
			wantOutcome: outcomeAlreadyPresent,
			command:     "git commit -m \"fix\" --trailer=" + trQ,
			trailer:     tr,
			want:        "git commit -m \"fix\" --trailer=" + trQ,
		},
		{
			name:        "already has the trailer in the message - untouched",
			wantOutcome: outcomeAlreadyPresent,
			command:     "git commit -m \"fix\n\n" + tr + "\"",
			trailer:     tr,
			want:        "git commit -m \"fix\n\n" + tr + "\"",
		},
		{
			name:        "attribution disabled (empty trailer) - untouched",
			wantOutcome: outcomeDisabled,
			command:     `git commit -m "fix"`,
			trailer:     "",
			want:        `git commit -m "fix"`,
		},
		{
			name:        "&& chain - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `git add -A && git commit -m "fix"`,
			trailer:     tr,
			want:        `git add -A && git commit -m "fix"`,
			wantNote:    true,
		},
		{
			name:        "commit then push - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `git commit -m "fix"; git push`,
			trailer:     tr,
			want:        `git commit -m "fix"; git push`,
			wantNote:    true,
		},
		{
			name:        "pipeline - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `git commit -m "fix" | tee log`,
			trailer:     tr,
			want:        `git commit -m "fix" | tee log`,
			wantNote:    true,
		},
		{
			name:        "subshell - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `(git commit -m "fix")`,
			trailer:     tr,
			want:        `(git commit -m "fix")`,
			wantNote:    true,
		},
		{
			name:        "command substitution in the message - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `git commit -m "$(cat msg.txt)"`,
			trailer:     tr,
			want:        `git commit -m "$(cat msg.txt)"`,
			wantNote:    true,
		},
		{
			name:        "git commit not the first command - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `cd sub && git commit -m "fix"`,
			trailer:     tr,
			want:        `cd sub && git commit -m "fix"`,
			wantNote:    true,
		},
		{
			name:        "git option before commit - untouched",
			wantOutcome: outcomeNotACommit,
			command:     `git -C sub commit -m "fix"`,
			trailer:     tr,
			want:        `git -C sub commit -m "fix"`,
		},
		{
			name:        "--amend - untouched",
			wantOutcome: outcomeSkippedAmend,
			command:     `git commit --amend --no-edit`,
			trailer:     tr,
			want:        `git commit --amend --no-edit`,
		},
		{
			name:        "-F - (message on stdin) - untouched, noted",
			wantOutcome: outcomeSkippedStdin,
			command:     `git commit -F -`,
			trailer:     tr,
			want:        `git commit -F -`,
			wantNote:    true,
		},
		{
			name:        "heredoc message - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     "git commit -F - <<'EOF'\nfix\nEOF",
			trailer:     tr,
			want:        "git commit -F - <<'EOF'\nfix\nEOF",
			wantNote:    true,
		},
		{
			name:        "unterminated quote - untouched, noted",
			wantOutcome: outcomeSkippedUnparsed,
			command:     `git commit -m "fix`,
			trailer:     tr,
			want:        `git commit -m "fix`,
			wantNote:    true,
		},
		{
			name:        "non-commit git command - untouched",
			wantOutcome: outcomeNotACommit,
			command:     "git log --grep commit",
			trailer:     tr,
			want:        "git log --grep commit",
		},
		{
			name:        "echo mentioning git commit - untouched",
			wantOutcome: outcomeNotACommit,
			command:     `echo "run git commit later"`,
			trailer:     tr,
			want:        `echo "run git commit later"`,
		},
		{
			name:        "template with $, backtick and ' - single-quoted, never expanded",
			wantOutcome: outcomeAdded,
			command:     `git commit -m "fix"`,
			trailer:     "Co-Authored-By: $USER `id` O'Brien",
			want:        `git commit --trailer='Co-Authored-By: $USER ` + "`id`" + ` O'\''Brien' -m "fix"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &mockDeps{attribution: &mockAttributionProvider{trailer: tt.trailer}}
			got, note, outcome := classifyAttribution(tt.command, deps)
			if got != tt.want {
				t.Errorf("classifyAttribution(%q) = %q, want %q", tt.command, got, tt.want)
			}
			if (note != "") != tt.wantNote {
				t.Errorf("classifyAttribution(%q) note = %q, want note: %v", tt.command, note, tt.wantNote)
			}
			if note != "" && !strings.Contains(note, tt.trailer) {
				t.Errorf("note %q does not name the trailer %q", note, tt.trailer)
			}
			if outcome != tt.wantOutcome {
				t.Errorf("classifyAttribution(%q) outcome = %q, want %q", tt.command, outcome, tt.wantOutcome)
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

// mockDeps is the tools test double WITHOUT the optional AttributionJournaler
// capability: it implements ToolDeps and nothing more, so attributionJournalerOf
// finds no journaler and bash() writes no attribution event for it. That is what
// keeps every non-attribution bash test off the machine-level journal — a method
// on the type, not a nil field, is what makes the assertion hold; a test that
// wants receipts wraps it in journalingMockDeps.
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

// --- Journaling the backstop's decision (issue #146) -------------------------

// recordingJournaler is an AttributionJournaler test double reporting fixed
// session coordinates, so a test can assert the coordinate pair rides along.
// The events themselves are asserted from the journal on disk (readAttributionEvents),
// which is the record that matters — not from what the double was handed.
type recordingJournaler struct {
	session string
	turn    int
	project string
}

func (r *recordingJournaler) AttributionSession() (string, int) {
	return r.session, r.turn
}

func (r *recordingJournaler) AttributionProject() string { return r.project }

// journalingDeps returns deps that journal attributions through a fixed
// recordingJournaler scoped to project, wrapped so ONLY the returned value
// carries the capability, with the machine-level journal redirected at a temp
// CORTEX_HOME so a test never writes the real ~/.cortex. The second return is
// the plain *mockDeps underneath, for the knobs a test still needs (gate).
func journalingDeps(t *testing.T, project, trailer string) (ToolDeps, *mockDeps) {
	t.Helper()
	withIsolatedUserHome(t)
	rec := &recordingJournaler{session: "sess-1", turn: 4, project: project}
	mock := &mockDeps{attribution: &mockAttributionProvider{trailer: trailer}, workdir: project}
	return journalingMockDeps{mockDeps: mock, j: rec}, mock
}

// readAttributionEvents reads back everything the isolated journal holds, so a
// test asserts what landed on disk rather than what a double was handed.
func readAttributionEvents(t *testing.T) []journal.AttributionCommitPayload {
	t.Helper()
	got, _, err := journal.LatestAttributionCommits()
	if err != nil {
		t.Fatalf("LatestAttributionCommits: %v", err)
	}
	return got
}

// TestClassifyAttributionOutcomeVocabulary pins the classifier's outcome to
// the journal's own constants, so a rename on either side fails the build
// rather than quietly writing an outcome string no reader knows.
func TestClassifyAttributionOutcomeVocabulary(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{outcomeAdded, journal.AttributionOutcomeAdded},
		{outcomeAlreadyPresent, journal.AttributionOutcomeAlreadyPresent},
		{outcomeSkippedUnparsed, journal.AttributionOutcomeSkippedUnparseable},
		{outcomeSkippedAmend, journal.AttributionOutcomeSkippedAmend},
		{outcomeSkippedStdin, journal.AttributionOutcomeSkippedStdin},
		{outcomeDisabled, journal.AttributionOutcomeDisabled},
	} {
		if c.got != c.want {
			t.Errorf("classifier outcome %q != journal constant %q", c.got, c.want)
		}
	}
}

// TestBashAttributionJournalesOneEventPerOutcome runs each shape of commit
// command through bash() in a throwaway repo and checks the journal holds
// exactly one intent event naming the right outcome — plus the verified
// receipt when a commit really landed. Attribution being off is an outcome
// too: it is recorded, so the off periods are visible in the record.
func TestBashAttributionJournalesOneEventPerOutcome(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	const tr = "Co-Authored-By: Cortex (m1)"
	tests := []struct {
		name        string
		trailer     string
		command     string
		wantOutcome string
		// wantVerified is the number of verified receipts expected (1 when
		// the command actually creates a commit in the temp repo).
		wantVerified int
		wantTrailer  bool
		// wantNoGitProcess marks a command that isn't a commit at all, for which
		// nothing may be recorded — and is always paired with wantBogusGit,
		// because zero journal events alone prove nothing (an empty-Outcome
		// append is rejected by the journal regardless).
		wantNoGitProcess bool
		// wantBogusGit runs the command under a PATH shadowing git with a stub
		// printing a fixed HEAD, so a verification read that should not happen
		// becomes observable: it turns the command's output into the stub's, and
		// a receipt out of the shadow HEAD. wantOutput is then the verbatim
		// output showing what really ran. bash resolves its own interpreter by
		// absolute path, so the command under test is unaffected by the shadow.
		wantBogusGit bool
		wantOutput   string
		// committed makes the repo hold one commit before the command runs, the
		// shape a false receipt needs to be possible: an unattributed HEAD the
		// command can be caught naming as its own work.
		committed bool
	}{
		{
			name:         "simple commit - added, then verified present",
			trailer:      tr,
			command:      `git commit -q -m "fix"`,
			wantOutcome:  journal.AttributionOutcomeAdded,
			wantVerified: 1,
			wantTrailer:  true,
		},
		{
			name:        "trailer already present - already_present, nothing runs",
			trailer:     tr,
			command:     "git commit -q -m \"fix\" --trailer='" + tr + "' --dry-run",
			wantOutcome: journal.AttributionOutcomeAlreadyPresent,
		},
		{
			// The backstop refuses to rewrite a pipeline, but git still
			// commits through it — so the verified receipt is expected and
			// must report the trailer as absent. That is the measurement.
			name:         "pipeline - skipped_unparseable, commit lands unattributed",
			trailer:      tr,
			command:      `git commit -q -m "fix" | tee /dev/null`,
			wantOutcome:  journal.AttributionOutcomeSkippedUnparseable,
			wantVerified: 1,
		},
		{
			name:        "amend - skipped_amend",
			trailer:     tr,
			command:     "git commit -q --amend --no-edit",
			wantOutcome: journal.AttributionOutcomeSkippedAmend,
		},
		{
			// git commit is the tail of a pipeline, so the whole command is
			// unparseable to the backstop — skipped_unparseable is the honest
			// reason (it never reaches the word-level stdin check).
			name:         "stdin message in a pipeline - skipped_unparseable",
			trailer:      tr,
			command:      "echo msg | git commit -q -F -",
			wantOutcome:  journal.AttributionOutcomeSkippedUnparseable,
			wantVerified: 1,
		},
		{
			// --file=- parses as one simple `git commit …`, so the word-level
			// stdin check is what names the reason.
			name:        "--file=- message - skipped_stdin",
			trailer:     tr,
			command:     "git commit -q --file=-",
			wantOutcome: journal.AttributionOutcomeSkippedStdin,
		},
		{
			name:         "attribution off - disabled",
			trailer:      "",
			command:      `git commit -q -m "unattributed"`,
			wantOutcome:  journal.AttributionOutcomeDisabled,
			wantVerified: 0,
		},
		{
			name:             "not a commit at all - nothing recorded",
			trailer:          tr,
			command:          "git log --oneline",
			wantOutcome:      "",
			wantNoGitProcess: true,
			wantBogusGit:     true,
			wantOutput:       "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\x00bogus commit HEAD\n",
		},
		{
			// --dry-run is one simple `git commit …` the backstop rewrites (so
			// the intent is added) and git exits 0 for, yet it creates no commit.
			// In a repo that already HAS one, a post-run read of HEAD alone would
			// receipt that pre-existing commit as this command's work — the wrong
			// compliance fact. Only a moved HEAD earns a receipt, so the intent
			// stands alone here.
			name:         "added but makes no commit - intent only, no verified receipt",
			trailer:      tr,
			command:      `git commit -q -m x --dry-run`,
			wantOutcome:  journal.AttributionOutcomeAdded,
			wantVerified: 0,
			committed:    true,
		},
		{
			// The command exits 0 and mentions a commit it never made (`true`
			// swallows git's nothing-to-commit failure). A chain, so the backstop
			// leaves it alone — and with HEAD unmoved there is still no receipt.
			name:         "chain makes no commit - skipped_unparseable, no receipt",
			trailer:      tr,
			command:      `git commit -q -m x; true`,
			wantOutcome:  journal.AttributionOutcomeSkippedUnparseable,
			wantVerified: 0,
			committed:    true,
		},
		{
			// The shape that actually matters: a plain command in a repo that
			// already HAS a commit. Only journalAttributionVerified's own intent
			// guard keeps it from reading HEAD here, so the shadow git makes the
			// difference observable — a read would answer with the stub's HEAD and
			// print its listing, not the directory's.
			name:             "non-commit in a repo with a commit - nothing recorded",
			trailer:          tr,
			command:          "ls",
			wantOutcome:      "",
			wantNoGitProcess: true,
			wantBogusGit:     true,
			wantOutput:       "LSRAN\n",
			committed:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newAttributionRepo(t)
			if tt.committed {
				commitInRepo(t, dir, "prior work")
			}
			baseline := headSHA(t, dir)
			deps, mock := journalingDeps(t, dir, tt.trailer)

			var bogusGitCleanup func(t *testing.T)
			if tt.wantBogusGit {
				bogusGitCleanup = withBogusGit(t)
			}
			out, err := bash(context.Background(), bashCall(t, tt.command), deps)
			if err != nil {
				t.Fatalf("bash: %v", err)
			}

			events := readAttributionEvents(t)
			// An intent event is exactly one per observed commit command; a
			// second of either kind is the double receipt the record must not
			// read as two commits.
			var intents []journal.AttributionCommitPayload
			for _, e := range events {
				if !e.Verified {
					intents = append(intents, e)
				}
			}
			if len(intents) != 1 && tt.wantOutcome != "" {
				t.Errorf("intent events = %d, want exactly 1: %+v", len(intents), events)
			}
			if tt.wantNoGitProcess {
				if cmds := mock.gated; len(cmds) != 1 || cmds[0] != tt.command {
					t.Fatalf("commands the gate saw = %q, want exactly the recorded command run once", cmds)
				}
				if out != tt.wantOutput {
					t.Errorf("bash(%q) output = %q, want %q", tt.command, out, tt.wantOutput)
				}
				if events := readAttributionEvents(t); len(events) != 0 {
					t.Fatalf("events = %+v, want none for a non-commit command", events)
				}
				if bogusGitCleanup != nil {
					defer bogusGitCleanup(t)
				}
			}
			if tt.wantOutcome == "" {
				if len(events) != 0 {
					t.Fatalf("events = %+v, want none for a non-commit command", events)
				}
				return
			}
			if len(events) == 0 {
				t.Fatalf("no attribution events, want an intent with outcome %q", tt.wantOutcome)
			}
			intent := events[0]
			if intent.Outcome != tt.wantOutcome {
				t.Errorf("intent outcome = %q, want %q", intent.Outcome, tt.wantOutcome)
			}
			if intent.Verified {
				t.Errorf("intent event is marked verified, want the intent-only write")
			}
			if intent.SessionID != "sess-1" || intent.Turn != 4 {
				t.Errorf("intent coordinates = %q/%d, want sess-1/4", intent.SessionID, intent.Turn)
			}
			if intent.Project != dir {
				t.Errorf("intent project = %q, want %q", intent.Project, dir)
			}
			if intent.Command == "" {
				t.Error("intent event carries no command")
			}

			var verified []journal.AttributionCommitPayload
			for _, e := range events {
				if e.Verified {
					verified = append(verified, e)
				}
			}
			if len(verified) != tt.wantVerified {
				t.Fatalf("verified receipts = %d, want %d: %+v", len(verified), tt.wantVerified, events)
			}
			if tt.wantVerified == 0 {
				// Nothing was committed, so HEAD must still be the baseline the
				// command started from — except when the backstop was disabled
				// (attribution off): a real commit made then is correctly
				// unattributed, and correctly gets no receipt because there is
				// no trailer to verify against.
				if after := headSHA(t, dir); after != baseline && tt.trailer != "" {
					t.Errorf("HEAD moved (%s -> %s) but the command was to commit nothing", baseline, after)
				}
				return
			}
			got := verified[0]
			if got.SHA == "" {
				t.Error("verified receipt has no SHA")
			}
			if got.SHA == baseline {
				t.Errorf("verified receipt names the pre-existing HEAD %s, not a commit this command made", got.SHA)
			}
			if got.SHA != headSHA(t, dir) {
				t.Errorf("verified SHA = %q, want HEAD %q", got.SHA, headSHA(t, dir))
			}
			if got.TrailerPresent != tt.wantTrailer {
				t.Errorf("verified TrailerPresent = %v, want %v", got.TrailerPresent, tt.wantTrailer)
			}
		})
	}
}

// TestBashAttributionVerifiedRecordsUnattributedCommit is the compliance fact
// the issue asks for: when the backstop could not rewrite (here, a chain),
// the command may still commit — and the verified receipt must say the
// trailer is NOT there, which is the measurement, not an assumption.
func TestBashAttributionVerifiedRecordsUnattributedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := newAttributionRepo(t)
	deps, _ := journalingDeps(t, dir, "Co-Authored-By: Cortex (m1)")

	// A chain the backstop refuses to rewrite; git still commits.
	cmd := `git commit -q -m "by hand" ; git log -1 --format=%H >/dev/null`
	if _, err := bash(context.Background(), bashCall(t, cmd), deps); err != nil {
		t.Fatalf("bash: %v", err)
	}

	events := readAttributionEvents(t)
	if len(events) < 2 {
		t.Fatalf("events = %+v, want an intent plus a verified receipt", events)
	}
	if events[0].Outcome != journal.AttributionOutcomeSkippedUnparseable {
		t.Errorf("intent outcome = %q, want %q", events[0].Outcome, journal.AttributionOutcomeSkippedUnparseable)
	}
	got := events[len(events)-1]
	if !got.Verified {
		t.Fatalf("last event not verified: %+v", got)
	}
	if got.SHA != headSHA(t, dir) {
		t.Errorf("verified SHA = %q, want HEAD %q", got.SHA, headSHA(t, dir))
	}
	if got.TrailerPresent {
		t.Error("TrailerPresent = true, want false for a commit the backstop left alone")
	}
}

// TestBashAttributionRefusedCommandJournalsNothing pins that the intent event
// is written before the risk gate but no verified receipt follows a command
// that never ran — nothing was committed, so there is no SHA to report and an
// "added" intent must not read as a commit that happened.
func TestBashAttributionRefusedCommandJournalsNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := newAttributionRepo(t)
	deps, mock := journalingDeps(t, dir, "Co-Authored-By: Cortex (m1)")
	mock.gateRefuse = true

	if _, err := bash(context.Background(), bashCall(t, `git commit -q -m "fix"`), deps); err != nil {
		t.Fatalf("bash: %v", err)
	}
	before := headSHA(t, dir)

	events := readAttributionEvents(t)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the intent (no verified receipt for a refused command)", events)
	}
	if events[0].Verified || events[0].SHA != "" {
		t.Errorf("refused command produced a verified receipt: %+v", events[0])
	}
	if after := headSHA(t, dir); after != before {
		t.Errorf("HEAD moved (%s -> %s): a refused command must not commit", before, after)
	}
}

// TestBashAttributionNoJournalerIsNoop proves the optional capability stays
// optional: a ToolDeps that does NOT implement AttributionJournaler runs the
// backstop exactly as before and writes nothing. The double here implements
// Workdirer but not the journaler, so the dynamic assertion is what's under
// test — and a second subtest points the journaler at coordinates it can't
// name (""/0) to show an unnamed session still records the commit.
func TestBashAttributionNoJournalerIsNoop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Run("deps without the capability write nothing", func(t *testing.T) {
		dir := newAttributionRepo(t)
		withIsolatedUserHome(t)
		deps := ToolDeps(noJournalerDeps{
			mockDeps: &mockDeps{workdir: dir},
			trailer:  "Co-Authored-By: Cortex (m1)",
		})
		if _, err := bash(context.Background(), bashCall(t, `git commit -q -m "fix"`), deps); err != nil {
			t.Fatalf("bash: %v", err)
		}
		if headSHA(t, dir) == "" {
			t.Fatal("commit did not land: the backstop must still run without a journaler")
		}
		if events := readAttributionEvents(t); len(events) != 0 {
			t.Errorf("events = %+v, want none without a journaler", events)
		}
	})
	t.Run("journaler with no coordinates still records", func(t *testing.T) {
		dir := newAttributionRepo(t)
		withIsolatedUserHome(t)
		mock := &mockDeps{attribution: &mockAttributionProvider{trailer: "Co-Authored-By: Cortex (m1)"}, workdir: dir}
		deps := ToolDeps(journalingMockDeps{mockDeps: mock, j: &recordingJournaler{}})
		if _, err := bash(context.Background(), bashCall(t, `git commit -q -m "fix"`), deps); err != nil {
			t.Fatalf("bash: %v", err)
		}
		events := readAttributionEvents(t)
		if len(events) != 2 {
			t.Fatalf("events = %+v, want intent + verified", events)
		}
		if events[0].SessionID != "" || events[0].Turn != 0 || events[0].Project != "" {
			t.Errorf("coordinates = %q/%d/%q, want empty", events[0].SessionID, events[0].Turn, events[0].Project)
		}
		if !events[1].Verified || events[1].SHA == "" {
			t.Errorf("verified receipt = %+v, want one with a SHA", events[1])
		}
	})
}

// The bash tool spawns its command with exec.CommandContext(ctx, "bash", "-c",
// command) — a literal interpreter path, never resolved through PATH — while
// attributionHead builds exec.Command("git", …), which IS a PATH lookup. That
// asymmetry is what makes the wantBogusGit cases above decidable: with git
// shadowed, the command under test still runs for real while an attribution
// HEAD read returns the shadow's fixed answer, so "the command ran" and "the
// repository was probed" leave different output and different journal events.

// withBogusGit runs the rest of the test under a PATH that shadows ls(1) with a
// stub printing LSRAN and git with a stub printing a fixed HEAD (shaped like
// `git log -1 --format=%H%x00%B`), so a case can tell "the command ran" from
// "the attribution HEAD read was attempted": their output and the events they
// leave behind are different, and neither can pass for the other. Only git is
// load-bearing: bash() spawns the command under test as `bash -c …` with a
// literal interpreter path, so shadowing it would replace the command itself.
// Returns the PATH-restoring cleanup, to be deferred where the assertions on the
// command's output happen.
func withBogusGit(t *testing.T) func(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("ls", "#!/bin/sh\necho LSRAN\n")
	write("git", "#!/bin/sh\nprintf 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\\000bogus commit HEAD\\n'\nexit 0\n")
	old := os.Getenv("PATH")
	os.Setenv("PATH", bin+string(os.PathListSeparator)+old)
	return func(t *testing.T) {
		t.Helper()
		os.Setenv("PATH", old)
	}
}

// journalingMockDeps adds the optional AttributionJournaler capability to a
// mockDeps by EMBEDDING it: the two journaler methods live here, not on
// mockDeps, so plain *mockDeps really does not satisfy the interface — a Go type
// assertion checks the method set, never a field. Wrapping is therefore the only
// way a test gets attribution receipts, and every other bash test stays off the
// machine-level journal by construction.
type journalingMockDeps struct {
	*mockDeps
	j AttributionJournaler
}

func (d journalingMockDeps) AttributionSession() (string, int) {
	return d.j.AttributionSession()
}

func (d journalingMockDeps) AttributionProject() string { return d.j.AttributionProject() }

// noJournalerDeps is a ToolDeps double WITHOUT the AttributionJournaler
// capability: it embeds mockDeps for the inert ToolDeps surface and shadows the
// three methods bash() reads, so it is what headlessDeps and every other
// non-journaling double look like — which is what makes the dynamic assertion in
// attributionJournalerOf worth testing.
type noJournalerDeps struct {
	*mockDeps
	trailer string
}

func (d noJournalerDeps) AttributionCommit() string { return d.trailer }

// TestBashAttributionPlainMockDepsHasNoJournaler pins the double's shape the
// moment before it matters: *mockDeps must NOT satisfy AttributionJournaler, so
// a bash test that never wraps it cannot write an attribution event to the real
// ~/.cortex/journal no matter which command it runs.
func TestBashAttributionPlainMockDepsHasNoJournaler(t *testing.T) {
	if _, ok := AttributionJournaler(nil).(interface{ AttributionProject() string }); ok {
		t.Fatal("nil must not satisfy AttributionJournaler")
	}
	var plain any = &mockDeps{}
	if _, ok := plain.(AttributionJournaler); ok {
		t.Fatal("*mockDeps satisfies AttributionJournaler: every bash test using it would journal")
	}
	var wrapped any = journalingMockDeps{mockDeps: &mockDeps{}}
	if _, ok := wrapped.(AttributionJournaler); !ok {
		t.Fatal("journalingMockDeps must satisfy AttributionJournaler")
	}
}

// newAttributionRepo buildss a throwaway repo with one staged file, ready for a
// commit, and returns its root.
func newAttributionRepo(t *testing.T) string {
	t.Helper()
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
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	return dir
}

// commitInRepo makes one commit in dir, so a test's repo has a HEAD to start
// from — the precondition for telling a commit the command made apart from one
// it merely mentions.
func commitInRepo(t *testing.T, dir, message string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-q", "-m", message)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit %q: %v\n%s", message, err, out)
	}
}

// headSHA returns dir's full HEAD commit hash, or "" when it has none yet.
func headSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%H")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
