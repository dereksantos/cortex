package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/agent"
)

func TestStudyRegisteredOnRole(t *testing.T) {
	sa, ok := Lookup(FunctionStudy)
	if !ok {
		t.Fatal("expected \"study\" to be registered")
	}
	if sa.Name != "study" || sa.Role != "study" {
		t.Errorf("got Name=%q Role=%q, want both \"study\"", sa.Name, sa.Role)
	}
	if sa.AsTool().Function.Name != FunctionStudy {
		t.Errorf("Study.AsTool() name = %q, want %q", sa.AsTool().Function.Name, FunctionStudy)
	}
}

func TestRegisterLookupRoundTrip(t *testing.T) {
	sa := Subagent{Name: "probe", Role: "test-probe-agent", System: "sys"}
	Register(sa)

	got, ok := Lookup("test-probe-agent")
	if !ok {
		t.Fatal("expected registered profile to be found")
	}
	if got.Name != "probe" {
		t.Errorf("got Name %q, want %q", got.Name, "probe")
	}

	if _, ok := Lookup("no-such-agent"); ok {
		t.Error("expected unregistered name to miss")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register(Subagent{Role: "test-dup-agent"})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	Register(Subagent{Role: "test-dup-agent"})
}

func TestRegisterEmptyRolePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty Role")
		}
	}()
	Register(Subagent{Name: "no-role"})
}

// TestStudyAndAgentSystemForbidBashFileReads pins issue #142's locate-first
// guidance at the subagent seam: BOTH the Study and Agent system prompts must
// tell the model not to read files with bash shell commands — use
// read_file/outline/grep instead. Study has no bash tool, so the guidance is
// framed as "don't read files with shell commands"; Agent has bash, so the
// guidance rides on its bash bullet. The check is on a stable substring that
// must survive in both (the "read_file/outline/grep" reader trio), not the
// exact wording, so a rephrase keeps the test green as long as the principle
// does.
func TestSubagentShellStance(t *testing.T) {
	tests := []struct {
		name    string
		sys     string
		want    []string // substrings that must appear (lowercased)
		notWant []string
	}{
		// Study has no bash tool: its readers are read_file/outline/grep.
		{"study names its readers", studySystem, []string{"read_file/outline/grep"}, nil},
		// The agent subagent has bash: shell reads are allowed, repository
		// edits still go through the editing tools (diff + post-edit hook).
		{"agent allows shell reads, steers repo edits", agentSystem,
			[]string{"reading and searching with the shell is fine", "edit_file/write_file"},
			[]string{"never use bash shell readers"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lower := strings.ToLower(tt.sys)
			for _, w := range tt.want {
				if !strings.Contains(lower, w) {
					t.Errorf("prompt missing %q:\n%s", w, tt.sys)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(lower, w) {
					t.Errorf("prompt still contains %q:\n%s", w, tt.sys)
				}
			}
		})
	}
}

// fakeSubagentDeps embeds headlessDeps for every method Execute needs but this
// test doesn't care about, and overrides just the two the subagent dispatch
// path calls — a minimal double, not a full session.
type fakeSubagentDeps struct {
	headlessDeps
	outline  string
	digest   string
	gotRole  string
	gotSeed  string
	gotModel string
}

func (f *fakeSubagentDeps) Outline(path string, budget int) (string, error) {
	return f.outline, nil
}

func (f *fakeSubagentDeps) RunSubagent(ctx context.Context, sa Subagent, seed string) (string, error) {
	f.gotRole = sa.Role
	f.gotSeed = seed
	f.gotModel = sa.Model
	return f.digest, nil
}

func TestExecuteDispatchesRegisteredSubagentGenerically(t *testing.T) {
	Register(Subagent{Name: "probe-agent", Role: "test-exec-agent", System: "sys"})

	deps := &fakeSubagentDeps{outline: "OUTLINE", digest: "DIGEST"}
	tc := agent.ToolCall{Function: agent.FunctionCall{
		Name:      "test-exec-agent",
		Arguments: `{"path":".","goal":"find it"}`,
	}}

	out, _, err := Execute(context.Background(), tc, deps)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "DIGEST" {
		t.Errorf("got %q, want %q", out, "DIGEST")
	}
	if deps.gotRole != "test-exec-agent" {
		t.Errorf("RunSubagent got Role %q, want %q", deps.gotRole, "test-exec-agent")
	}
}

func TestExecuteStudyDispatchUnchanged(t *testing.T) {
	// study still routes through the same generic path, via the registry
	// entry installed at init() — behavior is unchanged from the pre-registry
	// hardcoded dispatch: headlessDeps.Outline errors before any model call.
	tc := agent.ToolCall{Function: agent.FunctionCall{
		Name:      FunctionStudy,
		Arguments: `{"path":".","goal":"x"}`,
	}}
	_, _, err := Execute(context.Background(), tc, headlessDeps{})
	if err == nil {
		t.Fatal("expected an error with no session (outline unavailable)")
	}
	want := "outline unavailable: no session"
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}

func TestExecuteUnknownToolStillErrors(t *testing.T) {
	tc := agent.ToolCall{Function: agent.FunctionCall{Name: "not-a-real-tool"}}
	_, _, err := Execute(context.Background(), tc, headlessDeps{})
	if err == nil {
		t.Fatal("expected error for unregistered, unknown tool name")
	}
	want := fmt.Sprintf(`no available tools matching name "%s"`, "not-a-real-tool")
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}

// TestProfileSeedSeam covers GOAL.md §3 slice 1: seed construction moved from
// a hardwired StudySeed call in runSubagent onto a per-profile Seed field.
func TestProfileSeedSeam(t *testing.T) {
	const goal, path, ol = "find the bug", "internal/tools", "OUTLINE-FIXTURE"

	t.Run("study seed byte-identical to StudySeed", func(t *testing.T) {
		got := Study.Seed(goal, path, ol)
		want := StudySeed(goal, path, ol)
		if got != want {
			t.Errorf("Study.Seed(...) = %q, want byte-identical to StudySeed(...) = %q", got, want)
		}
	})

	t.Run("custom profile seed overrides StudySeed", func(t *testing.T) {
		const customSeed = "CUSTOM SEED, NOT STUDY'S"
		Register(Subagent{
			Name: "custom-seed-agent",
			Role: "test-custom-seed-agent",
			Seed: func(goal, path, outline string) string { return customSeed },
		})

		deps := &fakeSubagentDeps{outline: ol, digest: "DIGEST"}
		tc := agent.ToolCall{Function: agent.FunctionCall{
			Name:      "test-custom-seed-agent",
			Arguments: fmt.Sprintf(`{"path":%q,"goal":%q}`, path, goal),
		}}

		if _, _, err := Execute(context.Background(), tc, deps); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if deps.gotSeed != customSeed {
			t.Errorf("RunSubagent got seed %q, want the profile's own seed %q", deps.gotSeed, customSeed)
		}
		if studySeed := StudySeed(goal, path, ol); deps.gotSeed == studySeed {
			t.Error("RunSubagent got StudySeed's output, want the custom profile's own seed")
		}
	})
}
