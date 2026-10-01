// project_commands_render_test.go covers step 5 (issue #129): the `cortex
// project commands` render functions and report builder. The render
// functions are pure and golden-pinned (exact text / exact JSON, the same
// convention as renderProjectListGolden); buildProjectCommandsReport is
// exercised end-to-end against a fixture tree to prove the CLI shows the
// SAME resolved commands the post-edit hook runs (discovery + config +
// AGENTS.md precedence).
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// TestCommandsReportInfoProjectsInRoleOrder pins the pure projection: a
// resolved Commands value → display slice in canonical role order, skipping
// roles no source supplied.
func TestCommandsReportInfoProjectsInRoleOrder(t *testing.T) {
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Source: "go.mod"},
		Lint:   projectcmd.Command{Cmd: "go vet {file}", PerFile: true, Source: "go.mod"},
		Test:   projectcmd.Command{Cmd: "go test ./...", Source: "go.mod"},
		Build:  projectcmd.Command{Cmd: "go build ./...", Source: "go.mod"},
	}
	got := commandsReportInfo(cmds, false)
	// Trust is the only gate: untrusted → nothing runs.
	want := []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, RunsNow: false},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, RunsNow: false},
		{Role: "test", Command: "go test ./...", Source: "go.mod", RunsNow: false},
		{Role: "build", Command: "go build ./...", Source: "go.mod", RunsNow: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commands[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Trusted: the per-file format/lint run; test/build never do.
	got = commandsReportInfo(cmds, true)
	want = []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, RunsNow: true},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, RunsNow: true},
		{Role: "test", Command: "go test ./...", Source: "go.mod", RunsNow: false},
		{Role: "build", Command: "go build ./...", Source: "go.mod", RunsNow: false},
	}
	if len(got) != len(want) {
		t.Fatalf("trusted: got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trusted commands[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCommandsReportInfoSkipsUnsuppliedRoles pins that a partially-discovered
// set (e.g. a Makefile that only supplies a test target) projects only the
// roles that are present.
func TestCommandsReportInfoSkipsUnsuppliedRoles(t *testing.T) {
	cmds := projectcmd.Commands{
		Test: projectcmd.Command{Cmd: "make test", Source: "Makefile"},
	}
	got := commandsReportInfo(cmds, false)
	if len(got) != 1 || got[0].Role != "test" || got[0].Command != "make test" {
		t.Errorf("got %+v, want only the discovered test role", got)
	}
	// A test target is never hook-run — untrusted or trusted.
	if got[0].RunsNow {
		t.Errorf("make test must have runs_now=false, got %+v", got)
	}
}

// TestRenderProjectCommandsTextGolden pins the exact text layout `cortex
// project commands` prints for a discovered Go project in an UNTRUSTED
// workspace (the default): the hook runs nothing, so every per-edit command
// reads "not run by the post-edit hook".
func TestRenderProjectCommandsTextGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, RunsNow: false},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, RunsNow: false},
			{Role: "test", Command: "go test ./...", Source: "go.mod", RunsNow: false},
			{Role: "build", Command: "go build ./...", Source: "go.mod", RunsNow: false},
		},
	}, false)
	want := "Project commands (/fixture/go):\n" +
		"  format  gofmt -w {file} (go.mod) [per-file] — not run by the post-edit hook\n" +
		"  lint    go vet {file} (go.mod) [per-file] — not run by the post-edit hook\n" +
		"  test    go test ./... (go.mod) — not run by the post-edit hook\n" +
		"  build   go build ./... (go.mod) — not run by the post-edit hook\n" +
		"  source: manifest name = discovered; config.json / AGENTS.md = declared\n" +
		"  runs now / trusted-workspace required: the post-edit hook's call for this workspace (cortex project trust); the hook runs only in a trusted workspace, and never runs test/build or whole-project commands per edit\n"
	if got != want {
		t.Errorf("renderProjectCommandsText = %q, want %q", got, want)
	}
}

// TestRenderProjectCommandsTextTrustedGolden pins the TRUSTED workspace's
// text layout: per-edit commands read "runs now", test/build still read
// "not run by the post-edit hook".
func TestRenderProjectCommandsTextTrustedGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, RunsNow: true},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, RunsNow: true},
			{Role: "test", Command: "go test ./...", Source: "go.mod", RunsNow: false},
			{Role: "build", Command: "go build ./...", Source: "go.mod", RunsNow: false},
		},
	}, true)
	want := "Project commands (/fixture/go):\n" +
		"  format  gofmt -w {file} (go.mod) [per-file] — runs now\n" +
		"  lint    go vet {file} (go.mod) [per-file] — runs now\n" +
		"  test    go test ./... (go.mod) — not run by the post-edit hook\n" +
		"  build   go build ./... (go.mod) — not run by the post-edit hook\n" +
		"  source: manifest name = discovered; config.json / AGENTS.md = declared\n" +
		"  runs now / trusted-workspace required: the post-edit hook's call for this workspace (cortex project trust); the hook runs only in a trusted workspace, and never runs test/build or whole-project commands per edit\n"
	if got != want {
		t.Errorf("renderProjectCommandsText = %q, want %q", got, want)
	}
}

// TestRenderProjectCommandsTextEmptyGolden pins the empty-report message.
func TestRenderProjectCommandsTextEmptyGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{Root: "/fixture/empty"}, false)
	want := "No project commands discovered for /fixture/empty\n"
	if got != want {
		t.Errorf("renderProjectCommandsText(empty) = %q, want %q", got, want)
	}
}

// TestProjectCommandsReportJSON pins the exact --json payload for a
// discovered Go project — the shape external consumers (run.sh) parse.
// per_file is omitempty (whole-project commands omit it); runs_now is
// always present. The report is built directly (the serialization shape,
// not the discovery, is under test).
func TestProjectCommandsReportJSON(t *testing.T) {
	report := ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, RunsNow: true},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, RunsNow: true},
			{Role: "test", Command: "go test ./...", Source: "go.mod", RunsNow: false},
			{Role: "build", Command: "go build ./...", Source: "go.mod", RunsNow: false},
		},
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "root": "/fixture/go",
  "commands": [
    {
      "role": "format",
      "command": "gofmt -w {file}",
      "source": "go.mod",
      "per_file": true,
      "runs_now": true
    },
    {
      "role": "lint",
      "command": "go vet {file}",
      "source": "go.mod",
      "per_file": true,
      "runs_now": true
    },
    {
      "role": "test",
      "command": "go test ./...",
      "source": "go.mod",
      "runs_now": false
    },
    {
      "role": "build",
      "command": "go build ./...",
      "source": "go.mod",
      "runs_now": false
    }
  ]
}`
	if string(b) != want {
		t.Errorf("JSON = %s, want %s", b, want)
	}
	// The payload must round-trip back to the report shape.
	var back ProjectCommandsReport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, report) {
		t.Errorf("round-trip changed the report: %+v vs %+v", back, report)
	}
}

// TestBuildProjectCommandsReportPrecedence is the end-to-end proof: the CLI
// report for a Go project with a config declaration AND an AGENTS.md
// declaration shows config > AGENTS.md > discovery, exactly the commands
// the post-edit hook resolves. A controlled *Config is passed (not
// LoadConfig) so the test is deterministic and independent of the caller's
// user config.
func TestBuildProjectCommandsReportPrecedence(t *testing.T) {
	// go.mod (discovery), an AGENTS.md `## Commands` section (declares lint),
	// and a controlled config (declares format).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := "## Commands\n\n- lint: my-lint {file}\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Project: ProjectConfig{Commands: map[string]string{"format": "cfg-fmt {file}"}}}

	report := buildProjectCommandsReport(dir, cfg, false)

	got := map[string]ProjectCommandInfo{}
	for _, c := range report.Commands {
		got[c.Role] = c
	}
	if len(report.Commands) != 4 {
		t.Fatalf("got %d commands, want 4: %+v", len(report.Commands), report.Commands)
	}
	if c := got["format"]; c.Command != "cfg-fmt {file}" || c.Source != projectcmd.SourceConfig {
		t.Errorf("format = %+v, want the config declaration to win", c)
	}
	if c := got["lint"]; c.Command != "my-lint {file}" || c.Source != projectcmd.SourceAgents {
		t.Errorf("lint = %+v, want AGENTS.md to beat discovery", c)
	}
	if c := got["test"]; c.Command != "go test ./..." || c.Source != "go.mod" {
		t.Errorf("test = %+v, want discovery to stand", c)
	}
	if c := got["build"]; c.Command != "go build ./..." || c.Source != "go.mod" {
		t.Errorf("build = %+v, want discovery to stand", c)
	}
	// The text render reflects the same precedence (format is the config's).
	if !strings.Contains(renderProjectCommandsText(report, false), "cfg-fmt {file} (config.json)") {
		t.Errorf("text render should show the config-declared format, got:\n%s", renderProjectCommandsText(report, false))
	}
	// Untrusted: nothing runs.
	for role, c := range got {
		if c.RunsNow {
			t.Errorf("%s runs_now must be false in an untrusted workspace, got %+v", role, c)
		}
	}
}

// TestBuildProjectCommandsReportPackageJSONRunnableForm pins the step that
// #129's "reuse them everywhere" relies on for node projects: the report
// (text and --json, the external consumer's input) must show the RUNNABLE
// form of a package.json script — "npm run <script>" (or "npm test" for
// the test script) — not the raw script body, which only runs inside npm
// (node_modules/.bin on PATH, pre/post hooks) and fails from a shell with
// "tsc: command not found".
func TestBuildProjectCommandsReportPackageJSONRunnableForm(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name": "proj", "scripts": {"lint": "eslint src", "test": "node --test", "build": "tsc -p tsconfig.json"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	report := buildProjectCommandsReport(dir, nil, false)
	got := map[string]ProjectCommandInfo{}
	for _, c := range report.Commands {
		got[c.Role] = c
	}
	want := map[string]struct {
		cmd    string
		source string
	}{
		"lint":  {"npm run lint", "package.json"},
		"test":  {"npm test", "package.json"},
		"build": {"npm run build", "package.json"},
	}
	if len(report.Commands) != len(want) {
		t.Fatalf("got %d commands, want %d: %+v", len(report.Commands), len(want), report.Commands)
	}
	for role, w := range want {
		c := got[role]
		if c.Command != w.cmd || c.Source != w.source {
			t.Errorf("%s = %+v, want command %q source %q (the runnable npm form, not the script body)", role, c, w.cmd, w.source)
		}
		if strings.Contains(c.Command, "tsc") || strings.Contains(c.Command, "node --test") || strings.Contains(c.Command, "eslint src") {
			t.Errorf("%s = %q must not echo the raw script body", role, c.Command)
		}
		// Untrusted: nothing runs — npm scripts included.
		if c.RunsNow {
			t.Errorf("%s = %+v: an untrusted workspace must not auto-run anything", role, c)
		}
	}
	// The text render (what `cortex project commands` prints) shows the
	// same runnable form — and, untrusted, every role reads "not run by
	// the post-edit hook".
	text := renderProjectCommandsText(report, false)
	for _, wantLine := range []string{
		"lint    npm run lint (package.json) — not run by the post-edit hook",
		"test    npm test (package.json) — not run by the post-edit hook",
		"build   npm run build (package.json) — not run by the post-edit hook",
	} {
		if !strings.Contains(text, wantLine) {
			t.Errorf("text render missing %q:\n%s", wantLine, text)
		}
	}
	// A TRUSTED workspace flips runs_now for the PER-FILE lint — the npm
	// lint here is a whole-project script (no {file}/{dir}), so even
	// trusted it does not auto-run: the hook never runs a whole-project
	// lint per edit. test/build never run either.
	trusted := buildProjectCommandsReport(dir, nil, true)
	for _, c := range trusted.Commands {
		if c.RunsNow {
			t.Errorf("trusted workspace: %s = %+v, want runs_now=false (no per-file lint, no test/build auto-run)", c.Role, c)
		}
	}
}

// TestRunsNowFollowsHookApplicability pins runs_now against the HOOK's own
// applicability rules (tools.HookWouldRun): trust is the first gate (an
// untrusted workspace runs nothing), and on a trusted workspace the
// per-file format/lint run while a whole-project format (no {file}), a
// whole-project lint (no {file}/{dir}), and the test/build roles NEVER do —
// no matter how trusted.
func TestRunsNowFollowsHookApplicability(t *testing.T) {
	cmds := projectcmd.Commands{
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Source: "go.mod"},
		Lint:   projectcmd.Command{Cmd: "cargo clippy {file}", PerFile: true, Source: "go.mod"},
		Test:   projectcmd.Command{Cmd: "go test ./...", Source: "go.mod"},
		Build:  projectcmd.Command{Cmd: "go build ./...", Source: "go.mod"},
	}
	cases := []struct {
		name    string
		cmds    projectcmd.Commands
		trusted bool
		want    map[string]bool // role → runs_now
	}{
		{
			name: "per-file-trusted-flips",
			cmds: cmds, trusted: true,
			want: map[string]bool{"format": true, "lint": true, "test": false, "build": false},
		},
		{
			name: "per-file-untrusted-runs-nothing",
			cmds: cmds, trusted: false,
			want: map[string]bool{"format": false, "lint": false, "test": false, "build": false},
		},
		{
			name: "whole-project-format-never-runs",
			cmds: projectcmd.Commands{
				// Whole-project (no {file}): even trusted, the hook never
				// runs a format with no per-file target.
				Format: projectcmd.Command{Cmd: "go fmt ./...", Source: "go.mod"},
				Lint:   projectcmd.Command{Cmd: "eslint {file}", PerFile: true, Source: "go.mod"},
			},
			trusted: true,
			want:    map[string]bool{"format": false, "lint": true},
		},
		{
			name:    "whole-project-lint-never-runs",
			cmds:    projectcmd.Commands{Lint: projectcmd.Command{Cmd: "cargo clippy --all-targets", Source: "go.mod"}},
			trusted: true,
			want:    map[string]bool{"lint": false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range commandsReportInfo(tc.cmds, tc.trusted) {
				want, ok := tc.want[c.Role]
				if !ok {
					continue
				}
				if c.RunsNow != want {
					t.Errorf("%s runs_now = %v, want %v (%+v)", c.Role, c.RunsNow, want, c)
				}
			}
		})
	}
}

// TestBuildProjectCommandsReportAgentsMDWithoutConfig pins the external
// consumer scenario (run.sh on a project that declares its commands only in
// AGENTS.md, with no .cortex/config.json): the report must still surface the
// AGENTS.md declarations over discovery, because resolveProjectCommands
// reads AGENTS.md from the root directly — a nil config drops nothing.
func TestBuildProjectCommandsReportAgentsMDWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("## Commands\n\n- format: agents-fmt {file}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .cortex/config.json at all → a nil config (what configForRoot yields).
	report := buildProjectCommandsReport(dir, nil, false)
	for _, c := range report.Commands {
		if c.Role == "format" && (c.Command != "agents-fmt {file}" || c.Source != projectcmd.SourceAgents) {
			t.Errorf("format = %+v, want the AGENTS.md declaration over go.mod discovery", c)
		}
	}
}

// TestBuildProjectCommandsReportNoManifestNoDeclarations pins the empty case:
// a root with no recognized manifest and no declarations resolves to an
// empty Commands — the report shows the "no commands" message and an empty
// JSON list.
func TestBuildProjectCommandsReportNoManifestNoDeclarations(t *testing.T) {
	dir := t.TempDir() // no go.mod, no AGENTS.md
	report := buildProjectCommandsReport(dir, nil, false)
	if len(report.Commands) != 0 {
		t.Errorf("expected no commands, got %+v", report.Commands)
	}
	if !strings.HasPrefix(renderProjectCommandsText(report, false), "No project commands discovered") {
		t.Errorf("text should report no commands, got:\n%s", renderProjectCommandsText(report, false))
	}
	// JSON must carry an explicit empty list (not null) so consumers can rely
	// on the field being present.
	b, _ := json.Marshal(report)
	if !strings.Contains(string(b), `"commands":[]`) {
		t.Errorf("JSON should carry an empty commands list, got: %s", b)
	}
}
