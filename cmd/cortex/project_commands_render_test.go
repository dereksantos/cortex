// project_commands_render_test.go covers step 5 (issue #129): the `cortex
// project commands` render functions and report builder. The render functions
// are pure and golden-pinned (exact text / exact JSON, the same convention as
// renderProjectListGolden); buildProjectCommandsReport is exercised
// end-to-end against a fixture tree to prove the CLI shows the SAME resolved
// commands the post-edit hook runs (discovery + config + AGENTS.md precedence).
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
	// runs_now follows the HOOK's rules: the per-file inert format/lint runs
	// now, test/build are never hook-run (tier "", runs_now false).
	want := []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, Tier: "inert", RunsNow: true},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, Tier: "inert", RunsNow: true},
		{Role: "test", Command: "go test ./...", Source: "go.mod", Tier: "", RunsNow: false},
		{Role: "build", Command: "go build ./...", Source: "go.mod", Tier: "", RunsNow: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commands[%d] = %+v, want %+v", i, got[i], want[i])
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
	// A make target is not on the allowlist: no tier, never auto-run.
	if got[0].Tier != "" || got[0].RunsNow {
		t.Errorf("make test must have no tier and runs_now=false, got %+v", got[0])
	}
}

// TestRenderProjectCommandsTextGolden pins the exact text layout `cortex
// project commands` prints for a discovered Go project — tier and
// runs-now included (issue #129's trust gate: inert per-edit commands run
// now, code-executing ones need a trusted workspace, and test/build —
// never hook-run — read "not run by the post-edit hook", which is what
// trusting the workspace would NOT change).
func TestRenderProjectCommandsTextGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, Tier: "inert", RunsNow: true},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, Tier: "inert", RunsNow: true},
			{Role: "test", Command: "go test ./...", Source: "go.mod", Tier: "", RunsNow: false},
			{Role: "build", Command: "go build ./...", Source: "go.mod", Tier: "", RunsNow: false},
		},
	}, false)
	want := "Project commands (/fixture/go):\n" +
		"  format  gofmt -w {file} (go.mod) [per-file] — inert: runs now\n" +
		"  lint    go vet {file} (go.mod) [per-file] — inert: runs now\n" +
		"  test    go test ./... (go.mod) — not allowlisted: not run by the post-edit hook\n" +
		"  build   go build ./... (go.mod) — not allowlisted: not run by the post-edit hook\n" +
		"  source: manifest name = discovered; config.json / AGENTS.md = declared\n" +
		"  runs now / trusted-workspace required: the post-edit hook's call for this workspace (cortex project trust); test/build and non-allowlisted commands are never auto-run\n"
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

// TestProjectCommandsReportJSON pins the exact --json payload for a discovered
// Go project — the shape external consumers (run.sh) parse. per_file and
// tier are omitempty (whole-project commands omit per_file; only allowlisted
// commands carry a tier), runs_now is always present. The report is
// built directly (the serialization shape, not the discovery, is under test).
func TestProjectCommandsReportJSON(t *testing.T) {
	report := ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", PerFile: true, Tier: "inert", RunsNow: true},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", PerFile: true, Tier: "inert", RunsNow: true},
			{Role: "test", Command: "go test ./...", Source: "go.mod", Tier: "", RunsNow: false},
			{Role: "build", Command: "go build ./...", Source: "go.mod", Tier: "", RunsNow: false},
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
      "tier": "inert",
      "runs_now": true
    },
    {
      "role": "lint",
      "command": "go vet {file}",
      "source": "go.mod",
      "per_file": true,
      "tier": "inert",
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
// declaration shows config > AGENTS.md > discovery, exactly the commands the
// post-edit hook resolves. A controlled *Config is passed (not LoadConfig) so
// the test is deterministic and independent of the caller's user config.
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
	}
	// npm scripts execute repository code: every one of them is the code
	// tier, and in an UNTRUSTED workspace none of them would auto-run —
	// the report must say so (runs_now=false), not just show the command.
	// (All three roles are per-project whole scripts here: lint has no
	// {file}, test/build are never hook-run — so runs_now=false holds even
	// trusted; see TestRunsNowFollowsHookApplicability for the flip.)
	for role := range want {
		c := got[role]
		if c.Tier != "runs repo code" || c.RunsNow {
			t.Errorf("%s = %+v: an npm script runs repository code and must not auto-run untrusted", role, c)
		}
	}
	// The text render (what `cortex project commands` prints) shows the
	// same runnable form — and the honest runs label per role: only the
	// per-file allowlisted lint (code tier, untrusted workspace) reads
	// "trusted-workspace required"; the whole-project lint and the
	// never-hook-run test/build roles read "not run by the post-edit hook"
	// (trusting the workspace would not change them).
	text := renderProjectCommandsText(report, false)
	for _, wantLine := range []string{
		"lint    npm run lint (package.json) — runs repo code: not run by the post-edit hook",
		"test    npm test (package.json) — runs repo code: not run by the post-edit hook",
		"build   npm run build (package.json) — runs repo code: not run by the post-edit hook",
	} {
		if !strings.Contains(text, wantLine) {
			t.Errorf("text render missing %q:\n%s", wantLine, text)
		}
	}
	// A TRUSTED workspace does not flip runs_now for these: the whole-
	// project lint carries no {file}/{dir} and test/build are never
	// hook-run — the trust gate only gates the tier.
	trusted := buildProjectCommandsReport(dir, nil, true)
	for _, c := range trusted.Commands {
		if c.Tier != "runs repo code" || c.RunsNow {
			t.Errorf("trusted workspace: %s = %+v, want code tier and runs_now=false (no per-file lint, no test/build auto-run)", c.Role, c)
		}
	}
}

// TestRunsNowFollowsHookApplicability pins runs_now against the HOOK's own
// applicability rules (tools.HookWouldRun), not just the allowlist: an
// allowlisted PER-FILE code-tier lint flips to runs_now=true in a trusted
// workspace, while a whole-project format (no {file}), a whole-project lint
// (no {file}/{dir}), and test/build roles NEVER do — no matter how
// allowlisted or how trusted.
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
			name: "per-file-code-lint-trusted-flips",
			cmds: cmds, trusted: true,
			want: map[string]bool{"format": true, "lint": true, "test": false, "build": false},
		},
		{
			name: "per-file-code-lint-untrusted",
			cmds: cmds, trusted: false,
			want: map[string]bool{"format": true, "lint": false, "test": false, "build": false},
		},
		{
			name: "whole-project-format-never-runs",
			cmds: projectcmd.Commands{
				// Whole-project (no {file}): allowlisted as an inert go fmt,
				// but the hook never runs a format with no per-file target.
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
// AGENTS.md declarations over discovery, because resolveProjectCommands reads
// AGENTS.md from the root directly — a nil config drops nothing.
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
// a root with no recognized manifest and no declarations resolves to an empty
// Commands — the report shows the "no commands" message and an empty JSON list.
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
