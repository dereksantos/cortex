// project_commands_render_test.go covers step 5 (issue #129): the `cortex
// project commands` render functions and report builder. The render
// functions are pure and golden-pinned (exact text / exact JSON, the same
// convention as renderProjectListGolden); buildProjectCommandsReport is
// exercised end-to-end against a fixture tree to prove the CLI shows the
// SAME resolved commands the post-edit hook runs (discovery + config +
// instruction-file declarations) and the same "when it runs now" verdict
// the hook applies in that workspace.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/tools"
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
	// Untrusted: nothing runs, whatever the mode says.
	got := commandsReportInfo(cmds, false, tools.HookModeAll)
	want := []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "inactive: workspace untrusted"},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "inactive: workspace untrusted"},
		{Role: "test", Command: "go test ./...", Source: "go.mod", When: "inactive: workspace untrusted"},
		{Role: "build", Command: "go build ./...", Source: "go.mod", When: "inactive: workspace untrusted"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commands[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Trusted, mode all: the per-file format runs per edit, the lint at the
	// turn end; test/build never do.
	got = commandsReportInfo(cmds, true, tools.HookModeAll)
	want = []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "per-edit"},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "turn-end"},
		{Role: "test", Command: "go test ./...", Source: "go.mod", When: "never"},
		{Role: "build", Command: "go build ./...", Source: "go.mod", When: "never"},
	}
	if len(got) != len(want) {
		t.Fatalf("trusted/all: got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trusted/all commands[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Trusted, mode format: per-file format still runs per edit; the turn-end
	// lint is skipped by the mode; test/build still never.
	got = commandsReportInfo(cmds, true, tools.HookModeFormat)
	want = []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "per-edit"},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "inactive: hook mode format"},
		{Role: "test", Command: "go test ./...", Source: "go.mod", When: "never"},
		{Role: "build", Command: "go build ./...", Source: "go.mod", When: "never"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trusted/format commands[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Trusted, mode off: nothing runs, and it's the mode that keeps it off.
	got = commandsReportInfo(cmds, true, tools.HookModeOff)
	want = []ProjectCommandInfo{
		{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "inactive: hook mode off"},
		{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "inactive: hook mode off"},
		{Role: "test", Command: "go test ./...", Source: "go.mod", When: "never"},
		{Role: "build", Command: "go build ./...", Source: "go.mod", When: "never"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trusted/off commands[%d] = %+v, want %+v", i, got[i], want[i])
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
	got := commandsReportInfo(cmds, false, tools.HookModeAll)
	if len(got) != 1 || got[0].Role != "test" || got[0].Command != "make test" {
		t.Errorf("got %+v, want only the discovered test role", got)
	}
	// A test target is never hook-run in a TRUSTED workspace (any mode); in
	// an untrusted one, trust is the first gate and nothing runs at all.
	if got[0].When != "inactive: workspace untrusted" {
		t.Errorf("untrusted make test must read when=inactive: workspace untrusted, got %+v", got)
	}
	// Trusted, any mode: the test role is never auto-run.
	got = commandsReportInfo(cmds, true, tools.HookModeAll)
	if got[0].When != "never" {
		t.Errorf("trusted make test must read when=never, got %+v", got)
	}
}

// TestRenderProjectCommandsTextGolden pins the exact text layout `cortex
// project commands` prints for a discovered Go project in an UNTRUSTED
// workspace (the default): the hook runs nothing, so every command reads
// "inactive: workspace untrusted".
func TestRenderProjectCommandsTextGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "inactive: workspace untrusted"},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "inactive: workspace untrusted"},
			{Role: "test", Command: "go test ./...", Source: "go.mod", When: "inactive: workspace untrusted"},
			{Role: "build", Command: "go build ./...", Source: "go.mod", When: "inactive: workspace untrusted"},
		},
	})
	want := "Project commands (/fixture/go):\n" +
		"  format gofmt -w {file} (go.mod) — inactive: workspace untrusted\n" +
		"  lint   go vet {file} (go.mod) — inactive: workspace untrusted\n" +
		"  test   go test ./... (go.mod) — inactive: workspace untrusted\n" +
		"  build  go build ./... (go.mod) — inactive: workspace untrusted\n" +
		"  source: manifest name = discovered; config.json / instruction file (AGENTS.md, CLAUDE.md, …) = declared\n" +
		"  when: per-edit (the post-edit hook, after each write/edit), turn-end (the turn-end lint pass), never (the hook never auto-runs it), or inactive (this workspace or hook mode does not run it)\n"
	if got != want {
		t.Errorf("renderProjectCommandsText = %q, want %q", got, want)
	}
}

// TestRenderProjectCommandsTextTrustedGolden pins the TRUSTED workspace's
// text layout in mode all: the per-file format reads "per-edit", the lint
// "turn-end", and test/build read "never".
func TestRenderProjectCommandsTextTrustedGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "per-edit"},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "turn-end"},
			{Role: "test", Command: "go test ./...", Source: "go.mod", When: "never"},
			{Role: "build", Command: "go build ./...", Source: "go.mod", When: "never"},
		},
	})
	want := "Project commands (/fixture/go):\n" +
		"  format gofmt -w {file} (go.mod) — per-edit\n" +
		"  lint   go vet {file} (go.mod) — turn-end\n" +
		"  test   go test ./... (go.mod) — never\n" +
		"  build  go build ./... (go.mod) — never\n" +
		"  source: manifest name = discovered; config.json / instruction file (AGENTS.md, CLAUDE.md, …) = declared\n" +
		"  when: per-edit (the post-edit hook, after each write/edit), turn-end (the turn-end lint pass), never (the hook never auto-runs it), or inactive (this workspace or hook mode does not run it)\n"
	if got != want {
		t.Errorf("renderProjectCommandsText = %q, want %q", got, want)
	}
}

// TestRenderProjectCommandsTextEmptyGolden pins the empty-report message.
func TestRenderProjectCommandsTextEmptyGolden(t *testing.T) {
	got := renderProjectCommandsText(ProjectCommandsReport{Root: "/fixture/empty"})
	want := "No project commands discovered for /fixture/empty\n"
	if got != want {
		t.Errorf("renderProjectCommandsText(empty) = %q, want %q", got, want)
	}
}

// TestProjectCommandsReportJSON pins the exact --json payload for a
// discovered Go project — the shape external consumers (run.sh) parse. The
// report is built directly (the serialization shape, not the discovery, is
// under test).
func TestProjectCommandsReportJSON(t *testing.T) {
	report := ProjectCommandsReport{
		Root: "/fixture/go",
		Commands: []ProjectCommandInfo{
			{Role: "format", Command: "gofmt -w {file}", Source: "go.mod", When: "per-edit"},
			{Role: "lint", Command: "go vet {file}", Source: "go.mod", When: "turn-end"},
			{Role: "test", Command: "go test ./...", Source: "go.mod", When: "never"},
			{Role: "build", Command: "go build ./...", Source: "go.mod", When: "never"},
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
      "when": "per-edit"
    },
    {
      "role": "lint",
      "command": "go vet {file}",
      "source": "go.mod",
      "when": "turn-end"
    },
    {
      "role": "test",
      "command": "go test ./...",
      "source": "go.mod",
      "when": "never"
    },
    {
      "role": "build",
      "command": "go build ./...",
      "source": "go.mod",
      "when": "never"
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
// report for a Go project with a config declaration AND an instruction-file
// declaration shows config > instruction file > discovery, exactly the
// commands the post-edit hook resolves. A controlled *Config is passed (not
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

	report := buildProjectCommandsReport(dir, cfg, false, tools.HookModeAll)

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
	if c := got["lint"]; c.Command != "my-lint {file}" || c.Source != "AGENTS.md" {
		t.Errorf("lint = %+v, want AGENTS.md to beat discovery", c)
	}
	if c := got["test"]; c.Command != "go test ./..." || c.Source != "go.mod" {
		t.Errorf("test = %+v, want discovery to stand", c)
	}
	if c := got["build"]; c.Command != "go build ./..." || c.Source != "go.mod" {
		t.Errorf("build = %+v, want discovery to stand", c)
	}
	// The text render reflects the same precedence (format is the config's).
	if !strings.Contains(renderProjectCommandsText(report), "cfg-fmt {file} (config.json)") {
		t.Errorf("text render should show the config-declared format, got:\n%s", renderProjectCommandsText(report))
	}
	// Untrusted: nothing runs.
	for role, c := range got {
		if c.When != "inactive: workspace untrusted" {
			t.Errorf("%s when = %q, want the untrusted label in an untrusted workspace, got %+v", role, c.When, c)
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
	report := buildProjectCommandsReport(dir, nil, false, tools.HookModeAll)
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
		if c.When != "inactive: workspace untrusted" {
			t.Errorf("%s = %+v: an untrusted workspace must not auto-run anything", role, c)
		}
	}
	// The text render (what `cortex project commands` prints) shows the
	// same runnable form — and, untrusted, every role reads "inactive:
	// workspace untrusted".
	text := renderProjectCommandsText(report)
	for _, wantLine := range []string{
		"lint   npm run lint (package.json) — inactive: workspace untrusted",
		"test   npm test (package.json) — inactive: workspace untrusted",
		"build  npm run build (package.json) — inactive: workspace untrusted",
	} {
		if !strings.Contains(text, wantLine) {
			t.Errorf("text render missing %q:\n%s", wantLine, text)
		}
	}
	// A TRUSTED workspace in mode all flips the verdict, but only for a
	// per-file command — the npm lint here is a whole-project script (no
	// {file}/{dir}), so even trusted it is never auto-run: the hook never
	// runs a whole-project lint. test/build never run either.
	trusted := buildProjectCommandsReport(dir, nil, true, tools.HookModeAll)
	for _, c := range trusted.Commands {
		if c.When != "never" {
			t.Errorf("trusted workspace: %s = %+v, want when=never (no per-file command, no test/build auto-run)", c.Role, c)
		}
	}
}

// TestWhenFollowsHookApplicability pins the When verdict against the HOOK's
// own applicability rules (tools.HookWouldRun / roleRunnable): trust is the
// first gate (an untrusted workspace runs nothing), and on a trusted
// workspace the per-file format runs per-edit (mode format/all), a lint with
// {file} or {dir} runs at the turn end (mode all only), while a
// whole-project format (no {file}), a whole-project lint (no {file}/{dir}),
// and the test/build roles NEVER do — no matter how trusted or which mode.
func TestWhenFollowsHookApplicability(t *testing.T) {
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
		mode    tools.HookMode
		want    map[string]string // role → when
	}{
		{
			name: "per-file-trusted-all",
			cmds: cmds, trusted: true, mode: tools.HookModeAll,
			want: map[string]string{"format": "per-edit", "lint": "turn-end", "test": "never", "build": "never"},
		},
		{
			name: "per-file-untrusted-runs-nothing",
			cmds: cmds, trusted: false, mode: tools.HookModeAll,
			want: map[string]string{"format": "inactive: workspace untrusted", "lint": "inactive: workspace untrusted", "test": "inactive: workspace untrusted", "build": "inactive: workspace untrusted"},
		},
		{
			name: "per-file-trusted-off",
			cmds: cmds, trusted: true, mode: tools.HookModeOff,
			want: map[string]string{"format": "inactive: hook mode off", "lint": "inactive: hook mode off", "test": "never", "build": "never"},
		},
		{
			name: "whole-project-format-never-runs",
			cmds: projectcmd.Commands{
				// Whole-project (no {file}): even trusted, the hook never
				// runs a format with no per-file target.
				Format: projectcmd.Command{Cmd: "go fmt ./...", Source: "go.mod"},
				Lint:   projectcmd.Command{Cmd: "eslint {file}", PerFile: true, Source: "go.mod"},
			},
			trusted: true, mode: tools.HookModeAll,
			want: map[string]string{"format": "never", "lint": "turn-end"},
		},
		{
			name:    "whole-project-lint-never-runs",
			cmds:    projectcmd.Commands{Lint: projectcmd.Command{Cmd: "cargo clippy --all-targets", Source: "go.mod"}},
			trusted: true, mode: tools.HookModeAll,
			want: map[string]string{"lint": "never"},
		},
		{
			name: "dir-lint-trusted-all",
			// A {dir} lint (PerFile=false, like `go vet {dir}`) is per-package
			// work: roleRunnable covers it, so trusted in mode all it runs at
			// the turn end — the branch that {file}-only lint tests miss.
			cmds:    projectcmd.Commands{Lint: projectcmd.Command{Cmd: "go vet {dir}"}},
			trusted: true, mode: tools.HookModeAll,
			want: map[string]string{"lint": "turn-end"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range commandsReportInfo(tc.cmds, tc.trusted, tc.mode) {
				want, ok := tc.want[c.Role]
				if !ok {
					continue
				}
				if c.When != want {
					t.Errorf("%s when = %q, want %q (%+v)", c.Role, c.When, want, c)
				}
			}
		})
	}
}

// TestBuildProjectCommandsReportAgentsMDWithoutConfig pins the external
// consumer scenario (run.sh on a project that declares its commands only in
// its instruction file, with no .cortex/config.json): the report must still
// surface the instruction-file declarations over discovery, because
// resolveProjectCommands reads the RESOLVED instruction file from the root
// directly — a nil config drops nothing.
func TestBuildProjectCommandsReportAgentsMDWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("## Commands\n\n- format: agents-fmt {file}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .cortex/config.json at all → a nil config (what configForRoot yields).
	report := buildProjectCommandsReport(dir, nil, false, tools.HookModeAll)
	for _, c := range report.Commands {
		if c.Role == "format" && (c.Command != "agents-fmt {file}" || c.Source != "AGENTS.md") {
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
	report := buildProjectCommandsReport(dir, nil, false, tools.HookModeAll)
	if len(report.Commands) != 0 {
		t.Errorf("expected no commands, got %+v", report.Commands)
	}
	if !strings.HasPrefix(renderProjectCommandsText(report), "No project commands discovered") {
		t.Errorf("text should report no commands, got:\n%s", renderProjectCommandsText(report))
	}
	// JSON must carry an explicit empty list (not null) so consumers can rely
	// on the field being present.
	b, _ := json.Marshal(report)
	if !strings.Contains(string(b), `"commands":[]`) {
		t.Errorf("JSON should carry an empty commands list, got: %s", b)
	}
}
