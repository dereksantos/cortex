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

// TestAttributionPromptLine covers the system-prompt line: it spells out the
// resolved trailer (model substituted) and the PR footer verbatim, is sized
// to the surfaces actually configured (commit, PR, or both), and is absent
// when attribution is disabled or both surfaces are "".
func TestAttributionPromptLine(t *testing.T) {
	no := false
	yes := true
	empty := ""
	commit := "Signed-off-by: Bot <bot@example.com>"
	footer := "Made by a bot"
	both := `Attribution: end every git commit message you author with the trailer line "Co-Authored-By: Cortex (qwen3-coder)", and end every pull request body you write with the line "Generated with Cortex".`
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"nil config", nil, both},
		{"zero config", &Config{}, both},
		{"disabled", &Config{Attribution: AttributionConfig{Enabled: &no}}, ""},
		{"both surfaces empty", &Config{Attribution: AttributionConfig{Commit: &empty, PR: &empty}}, ""},
		{"commit disabled, PR on", &Config{Attribution: AttributionConfig{Enabled: &yes, Commit: &empty}},
			`Attribution: end every pull request body you write with the line "Generated with Cortex".`},
		{"PR disabled, commit on", &Config{Attribution: AttributionConfig{Enabled: &yes, PR: &empty}},
			`Attribution: end every git commit message you author with the trailer line "Co-Authored-By: Cortex (qwen3-coder)".`},
		{"custom templates", &Config{Attribution: AttributionConfig{Commit: &commit, PR: &footer}},
			`Attribution: end every git commit message you author with the trailer line "Signed-off-by: Bot <bot@example.com>", and end every pull request body you write with the line "Made by a bot".`},
		{"include_model false", &Config{Attribution: AttributionConfig{IncludeModel: &no, PR: &empty}},
			`Attribution: end every git commit message you author with the trailer line "Co-Authored-By: Cortex".`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.attributionPromptLine("qwen3-coder"); got != tt.want {
				t.Errorf("attributionPromptLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSystemPromptCarriesAttribution checks the composed coder system prompt
// (systemPromptContent — what CortexArgs.Request() and applyProjectByName
// build every session's system message from) carries the trailer and the PR
// footer when attribution is on, and neither when it is disabled.
func TestSystemPromptCarriesAttribution(t *testing.T) {
	no := false
	empty := ""
	tests := []struct {
		name        string
		cfg         *Config
		wantTrailer bool
		wantFooter  bool
	}{
		{"enabled by default (no config)", nil, true, true},
		{"enabled explicitly", &Config{Attribution: AttributionConfig{Enabled: boolPtr(true)}}, true, true},
		{"disabled", &Config{Attribution: AttributionConfig{Enabled: &no}}, false, false},
		{"pr footer off", &Config{Attribution: AttributionConfig{PR: &empty}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetPrompt(t)
			configurePrompt(tt.cfg)
			configureAttributionPrompt(tt.cfg, "m-code")
			got := systemPromptContent("agents body")
			if has := strings.Contains(got, "Co-Authored-By: Cortex (m-code)"); has != tt.wantTrailer {
				t.Errorf("system prompt has trailer = %v, want %v", has, tt.wantTrailer)
			}
			if has := strings.Contains(got, "Generated with Cortex"); has != tt.wantFooter {
				t.Errorf("system prompt has PR footer = %v, want %v", has, tt.wantFooter)
			}
			if !tt.wantTrailer && !tt.wantFooter && strings.Contains(got, "Attribution:") {
				t.Errorf("disabled attribution still left a line in the system prompt")
			}
			if !strings.HasPrefix(got, SystemPrompt) || !strings.Contains(got, agentsMarker+"agents body") {
				t.Errorf("attribution line must sit between the base prompt and AGENTS.md without displacing either")
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
	// A bare (unparenthesized) token is dropped too, as is it with
	// include_model=false and a known model.
	bare := "Assisted-by: Cortex <model>"
	no := false
	for _, c := range []struct {
		cfg   *Config
		model string
	}{
		{&Config{Attribution: AttributionConfig{Commit: &bare}}, ""},
		{&Config{Attribution: AttributionConfig{Commit: &bare, IncludeModel: &no}}, "m1"},
	} {
		if got := c.cfg.attributionCommit(c.model); got != "Assisted-by: Cortex" {
			t.Errorf("bare-token template, model %q = %q, want %q", c.model, got, "Assisted-by: Cortex")
		}
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
// sends a system prompt that carries the attribution line with the resolved
// trailer, (b) lands its commit WITH the trailer, and (c) records
// Attributed=true on the loop.run journal event.
func TestRunLoopFiringJournalsAttributedCommit(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)
	resetPrompt(t)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note"}

	var bodies []string
	srv := writeFileAndCaptureServer(t, &bodies)
	wrapped := func() *CortexSession {
		cfg := &Config{Models: map[string]ModelSpec{"code": {Model: "loop-model"}}}
		// The same order NewCortexSession uses: the attribution line is
		// configured from the resolved code model before Request() builds
		// the system message.
		configureAttributionPrompt(cfg, "loop-model")
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		cs.Config = cfg
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

	// The session's system prompt carried the attribution line, naming the
	// resolved trailer and the PR footer verbatim (item 3).
	if len(bodies) == 0 {
		t.Fatal("no backend requests recorded")
	}
	for _, want := range []string{"Co-Authored-By: Cortex (loop-model)", "Generated with Cortex"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("first request did not carry %q; request body:\n%s", want, bodies[0])
		}
	}
}

// TestRunLoopFiringNoModelJournalsAttributedCommit is the zero-config case
// (no config at all, so no model resolvable from config): attribution is on
// by default, the commit carries the model-less default trailer (never a
// literal "<model>"), and the journal records Attributed=true.
func TestRunLoopFiringNoModelJournalsAttributedCommit(t *testing.T) {
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
	if !got.Attributed {
		t.Errorf("Attributed = false, want true (attribution is on by default)")
	}
	if got.ChangeRef == "" {
		t.Fatal("ChangeRef is empty, want a branch@hash reference")
	}

	// The commit carries the model-less default trailer exactly once, and
	// never a literal "<model>" placeholder.
	body, err := gitCmdOutput(t, root, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.Contains(body, "<model>") {
		t.Errorf("commit message contains a literal <model> placeholder:\n%s", body)
	}
	if n := countTrailers(body, "Co-Authored-By"); n != 1 || !strings.Contains(body, "Co-Authored-By: Cortex") {
		t.Errorf("commit message should carry exactly one default trailer, got %d:\n%s", n, body)
	}
}
