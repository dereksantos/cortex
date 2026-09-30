package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

// project_workspace_test.go proves M3.5: --project <name> resolves via the
// registry (M3.3) and runs against that root — mirroring M3.1's
// CWD-vs-explicit-root equivalence pattern. newFixtureRepo/resolvedPath are
// shared helpers from workspace_test.go (same package).

// TestApplyProjectByNameRunsAgainstRegisteredRootFromUnrelatedCWD registers a
// fixture repo under a name via a temp registry, applies it from a CWD
// unrelated to the fixture (no .cortex/AGENTS.md anywhere up its chain), and
// asserts the session ends up identical to what WorkspaceFromCWD() would
// resolve to had the process actually been launched from inside the
// fixture: same root, same ContextDir, same confinement root (cs.root()),
// and a system prompt carrying the fixture's AGENTS.md instructions.
func TestApplyProjectByNameRunsAgainstRegisteredRootFromUnrelatedCWD(t *testing.T) {
	fixture := newFixtureRepo(t)

	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.NewAt(regPath)
	if err := reg.Save(registry.Project{Name: "blog", Root: fixture}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	cs := &CortexSession{Request: &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: systemPromptContent("", "")}}}}
	if err := applyProjectByName(cs, reg, "blog"); err != nil {
		t.Fatalf("applyProjectByName: %v", err)
	}

	// What WorkspaceFromCWD() would give had the process actually been
	// launched from inside the fixture — the equivalence proof.
	t.Chdir(fixture)
	want := WorkspaceFromCWD()

	if got, wantRoot := resolvedPath(t, cs.workspace.Root), resolvedPath(t, want.Root); got != wantRoot {
		t.Errorf("workspace.Root = %q, want %q", got, wantRoot)
	}
	gotCtx, wantCtx := resolvedPath(t, cs.ContextDir()), resolvedPath(t, want.ContextDir())
	if gotCtx != wantCtx {
		t.Errorf("ContextDir() = %q, want %q", gotCtx, wantCtx)
	}
	// SessionsDir() (.cortex/sessions) isn't created by the fixture — compare
	// the already-resolved ContextDir + "sessions" rather than
	// EvalSymlinks-ing cs.SessionsDir() directly, which doesn't exist on disk
	// yet (mirrors workspace_test.go's
	// TestWorkspaceFromCWDMatchesExplicitRootContextDir).
	if got, wantSessions := filepath.Join(gotCtx, "sessions"), filepath.Join(wantCtx, "sessions"); got != wantSessions {
		t.Errorf("SessionsDir-equivalent = %q, want %q", got, wantSessions)
	}
	if got, wantRoot := resolvedPath(t, cs.root()), resolvedPath(t, want.Root); got != wantRoot {
		t.Errorf("root() = %q, want %q (confinement root must follow --project, not CWD)", got, wantRoot)
	}

	_, wantInst := want.Instructions()
	if wantInst == "" {
		t.Fatal("fixture AGENTS.md instructions unexpectedly empty")
	}
	if !strings.Contains(cs.Request.Messages[0].Content, wantInst) {
		t.Errorf("system prompt does not carry the project's AGENTS.md instructions %q: got %q", wantInst, cs.Request.Messages[0].Content)
	}
	// #147: the section header names the loaded file.
	if !strings.Contains(cs.Request.Messages[0].Content, "# Project instructions (AGENTS.md)") {
		t.Errorf("system prompt section header should name AGENTS.md; got %q", cs.Request.Messages[0].Content)
	}
}

// TestApplyProjectByNameLabelsCopilotInstructionsFromUnrelatedCWD pins the
// fileLabel fix on the --project leg: a project whose only instruction file
// is .github/copilot-instructions.md gets the header
// ".github/copilot-instructions.md" — the same label the CWD leg produces
// for the same file — even when the process runs from an unrelated
// directory (the file is labeled relative to ws.Root, not the CWD).
func TestApplyProjectByNameLabelsCopilotInstructionsFromUnrelatedCWD(t *testing.T) {
	fixture := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fixture, ".cortex"), 0o755); err != nil {
		t.Fatal(err)
	}
	copilot := filepath.Join(fixture, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(copilot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copilot, []byte("copilot conventions\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.NewAt(regPath)
	if err := reg.Save(registry.Project{Name: "blog", Root: fixture}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Chdir(t.TempDir()) // unrelated CWD

	cs := &CortexSession{Request: &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: systemPromptContent("", "")}}}}
	if err := applyProjectByName(cs, reg, "blog"); err != nil {
		t.Fatalf("applyProjectByName: %v", err)
	}

	sys := cs.Request.Messages[0].Content
	if !strings.Contains(sys, "copilot conventions") {
		t.Errorf("system prompt must carry the copilot-instructions body: %q", sys)
	}
	if !strings.Contains(sys, "# Project instructions (.github/copilot-instructions.md)") {
		t.Errorf("header must label the file root-relative, even from an unrelated CWD; got %q", sys)
	}
	if strings.Contains(sys, "# Project instructions (copilot-instructions.md)") {
		t.Errorf("header must not drop the .github/ prefix: %q", sys)
	}
}

// TestApplyProjectByNameUnknownProjectReturnsTypedError pins that resolving
// an unregistered name surfaces registry.ErrProjectNotFound (unwrapped via
// errors.Is), not a generic error string a caller can't branch on.
func TestApplyProjectByNameUnknownProjectReturnsTypedError(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.NewAt(regPath)

	cs := &CortexSession{Request: &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	err := applyProjectByName(cs, reg, "nope")
	if err == nil {
		t.Fatal("applyProjectByName with an unregistered name: want an error, got nil")
	}
	if !errors.Is(err, registry.ErrProjectNotFound) {
		t.Errorf("applyProjectByName error = %v, want errors.Is(..., registry.ErrProjectNotFound)", err)
	}
}

// TestApplyProjectFlagNoopWhenNameEmpty pins that the shared CLI-facing
// helper leaves an already-constructed session's CWD-implicit workspace
// untouched when --project was not given — every existing turn/resume/study
// invocation without the flag must behave exactly as before M3.5.
func TestApplyProjectFlagNoopWhenNameEmpty(t *testing.T) {
	cs := &CortexSession{Request: &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	if err := applyProjectFlag(cs, ""); err != nil {
		t.Fatalf("applyProjectFlag(\"\"): %v", err)
	}
	if cs.workspace != nil {
		t.Errorf("workspace = %+v, want nil (untouched)", cs.workspace)
	}
	if cs.Request.Messages[0].Content != "sys" {
		t.Errorf("system prompt mutated by a no-op call: %q", cs.Request.Messages[0].Content)
	}
}

// TestParseProjectFlagExtractsNameAndRest covers --project appearing in
// various positions relative to a command's own flags/positional args, so
// turn/resume/study can each strip it before doing their own parsing.
func TestParseProjectFlagExtractsNameAndRest(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantRest []string
	}{
		{"absent", []string{"fix the bug"}, "", []string{"fix the bug"}},
		{"leading", []string{"--project", "blog", "fix the bug"}, "blog", []string{"fix the bug"}},
		{"trailing", []string{"fix the bug", "--project", "blog"}, "blog", []string{"fix the bug"}},
		{"only flag", []string{"--project", "blog"}, "blog", []string{}},
		{"empty args", []string{}, "", []string{}},
		{"dangling flag no value", []string{"--project"}, "", []string{"--project"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotRest := parseProjectFlag(tc.args)
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
			if len(gotRest) != len(tc.wantRest) {
				t.Fatalf("rest = %v, want %v", gotRest, tc.wantRest)
			}
			for i := range gotRest {
				if gotRest[i] != tc.wantRest[i] {
					t.Errorf("rest[%d] = %q, want %q", i, gotRest[i], tc.wantRest[i])
				}
			}
		})
	}
}
