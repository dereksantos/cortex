package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

// TestAttributionPromptLine covers the system-prompt line the issue asks for
// (item 3): present when attribution is on, absent when disabled, and sized
// to the surfaces actually configured (commit, PR, or both).
func TestAttributionPromptLine(t *testing.T) {
	no := false
	yes := true
	empty := ""
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"nil config", nil, "Attribute the work you author: git commits and pull request bodies end with the attribution marker your configuration specifies."},
		{"zero config", &Config{}, "Attribute the work you author: git commits and pull request bodies end with the attribution marker your configuration specifies."},
		{"disabled", &Config{Attribution: AttributionConfig{Enabled: &no}}, ""},
		{"commit disabled, PR on", &Config{Attribution: AttributionConfig{Enabled: &yes, Commit: &empty}}, "Attribute the work you author: pull request bodies end with the attribution marker your configuration specifies."},
		{"PR disabled, commit on", &Config{Attribution: AttributionConfig{Enabled: &yes, PR: &empty}}, "Attribute the work you author: git commits end with the attribution marker your configuration specifies."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.attributionPromptLine(); got != tt.want {
				t.Errorf("attributionPromptLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAttributionCommitResolvesOwnModel is the regression the reviewer
// caught: the session resolves the model ITSELF (the code role's resolved
// model, the same one the Turn uses), never a literal "<model>" placeholder —
// and a nil Config still yields a clean default template.
func TestAttributionCommitResolvesOwnModel(t *testing.T) {
	// No config at all: default-enabled, no model to substitute — the
	// "<model>" token must not survive into a real commit message.
	cs := &CortexSession{}
	if got := cs.AttributionCommit(); got != "Co-Authored-By: Cortex" {
		t.Errorf("nil-config AttributionCommit() = %q, want %q", got, "Co-Authored-By: Cortex")
	}

	// The Turn's own model is the one substituted — not a caller argument.
	cs = &CortexSession{
		Request: &AgentRequest{Model: "qwen3-coder"},
		Config:  &Config{Models: map[string]ModelSpec{"code": {Model: "qwen3-coder"}}},
	}
	if got := cs.AttributionCommit(); got != "Co-Authored-By: Cortex (qwen3-coder)" {
		t.Errorf("AttributionCommit() = %q, want %q", got, "Co-Authored-By: Cortex (qwen3-coder)")
	}

	// include_model=false strips the model part even when a model resolves.
	no := false
	cs = &CortexSession{
		Request: &AgentRequest{Model: "qwen3-coder"},
		Config:  &Config{Attribution: AttributionConfig{IncludeModel: &no}},
	}
	if got := cs.AttributionCommit(); got != "Co-Authored-By: Cortex" {
		t.Errorf("include_model=false AttributionCommit() = %q, want %q", got, "Co-Authored-By: Cortex")
	}
}

// TestAttributionCommitEmptyModelRealConfig feeds the real Config through the
// default template with an empty model (the zero-config fleet default the
// reviewer flagged) and asserts no literal "<model>" is left behind.
func TestAttributionCommitEmptyModelRealConfig(t *testing.T) {
	for _, cfg := range []*Config{nil, {}} {
		got := cfg.attributionCommit("")
		if strings.Contains(got, "<model>") {
			t.Errorf("attributionCommit(\"\") on %p left a literal placeholder: %q", cfg, got)
		}
		if got != "Co-Authored-By: Cortex" {
			t.Errorf("attributionCommit(\"\") = %q, want %q", got, "Co-Authored-By: Cortex")
		}
	}
	// A non-default template with an empty model also never ships the token.
	tmpl := "Generated-With: Agent (<model>)"
	cfg := &Config{Attribution: AttributionConfig{Commit: &tmpl}}
	if got := cfg.attributionCommit(""); got != "Generated-With: Agent" {
		t.Errorf("custom template, empty model = %q, want %q", got, "Generated-With: Agent")
	}
}

// writeFileAndCaptureServer is a two-round scripted backend that records the
// raw request bodies it receives (so a test can assert on what the turn's
// prompt actually carried) while round 1 issues a write_file tool call (so
// the firing actually dirties the target project's worktree and lands a
// commit) and round 2 answers with a final reply.
func writeFileAndCaptureServer(t *testing.T, bodies *[]string) *httptest.Server {
	t.Helper()
	var round int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, string(b))
		round++
		if round == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"notes.md\",\"content\":\"loop notes\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunLoopFiringJournalsAttributedCommit proves the issue's items 3+4 end
// to end: a loop firing whose session has a model and attribution on (a)
// sends a prompt that carries the attribution line, (b) lands its commit
// WITH the trailer, and (c) records Attributed=true on the loop.run journal
// event.
func TestRunLoopFiringJournalsAttributedCommit(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note"}

	var bodies []string
	srv := writeFileAndCaptureServer(t, &bodies)
	wrapped := func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		cs.Config = &Config{Models: map[string]ModelSpec{"code": {Model: "loop-model"}}}
		cs.Request.Model = "loop-model"
		return cs
	}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, wrapped); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Outcome != "success" {
		t.Fatalf("Outcome = %q, want success", got.Outcome)
	}
	if !got.Attributed {
		t.Errorf("Attributed = false, want true (attribution on, model set)")
	}
	if got.ChangeRef == "" {
		t.Fatal("ChangeRef is empty, want a branch@hash reference")
	}

	// The commit message itself must carry the trailer with the model name.
	body, err := gitCmdOutput(t, root, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(body, "Co-Authored-By: Cortex (loop-model)") {
		t.Errorf("commit message lacks the attributed trailer:\n%s", body)
	}

	// The loop's prompt carried the attribution line (item 3).
	if len(bodies) == 0 {
		t.Fatal("no backend requests recorded")
	}
	if !strings.Contains(bodies[0], "attribution marker") {
		t.Errorf("loop prompt did not carry the attribution line; request body:\n%s", bodies[0])
	}
}

// TestRunLoopFiringNoModelJournalsUnattributedCommit is the zero-config case:
// with no model resolvable the commit is made plain (no literal "<model>"),
// and the journal records Attributed=false rather than a false positive.
func TestRunLoopFiringNoModelJournalsUnattributedCommit(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note"}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, writeFileTurnTestSessionFactory(t)); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Outcome != "success" {
		t.Fatalf("Outcome = %q, want success", got.Outcome)
	}
	if got.Attributed {
		t.Errorf("Attributed = true, want false (no model resolvable)")
	}
	if got.ChangeRef == "" {
		t.Fatal("ChangeRef is empty, want a branch@hash reference")
	}

	// The plain commit must NOT carry a literal "<model>" placeholder.
	body, err := gitCmdOutput(t, root, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.Contains(body, "<model>") {
		t.Errorf("commit message contains a literal <model> placeholder:\n%s", body)
	}
}
