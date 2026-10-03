package projectcmd

import (
	"strings"
	"testing"
)

func TestParseAgentsCommands(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		want  map[Role]string
		wantR []Role // expected Roles() order; nil = don't check
	}{
		{
			name: "full-section",
			body: `# Project

Some prose about commands in general.

## Commands

- format: gofmt -w {file}
- lint: go vet {file}
- test: go test ./...
- build: go build ./...

## Notes

- format: must-not-leak-through
`,
			want: map[Role]string{
				RoleFormat: "gofmt -w {file}",
				RoleLint:   "go vet {file}",
				RoleTest:   "go test ./...",
				RoleBuild:  "go build ./...",
			},
			wantR: []Role{RoleFormat, RoleLint, RoleTest, RoleBuild},
		},
		{
			name: "header-case-insensitive-and-star-bullets",
			body: `##   COMMANDS
* FORMAT: black {file}
* Lint: flake8 {file}
`,
			want:  map[Role]string{RoleFormat: "black {file}", RoleLint: "flake8 {file}"},
			wantR: []Role{RoleFormat, RoleLint},
		},
		{
			name: "last-section-wins",
			body: `## Commands

- format: first-one {file}

## Other

- format: prose-adjacent

## Commands

- format: second-one {file}
`,
			want:  map[Role]string{RoleFormat: "second-one {file}"},
			wantR: []Role{RoleFormat},
		},
		{
			name:  "no-section",
			body:  "# Project\n\nRun `make test` to check things. See Commands elsewhere.\n\n## Notes\n\n- test: nope\n",
			want:  map[Role]string{},
			wantR: []Role{},
		},
		{
			name: "unknown-roles-and-malformed-items-ignored",
			body: `## Commands

- deploy: helm up
- format:
- lint
- test: pytest
`,
			want:  map[Role]string{RoleTest: "pytest"},
			wantR: []Role{RoleTest},
		},
		{
			name:  "empty-body",
			body:  "",
			want:  map[Role]string{},
			wantR: []Role{},
		},
		{
			name: "deeper-header-ends-section",
			body: `## Commands

- test: go test ./...

### Subsection inside commands
- lint: should-not-count (a ### header ends the section)
`,
			want:  map[Role]string{RoleTest: "go test ./..."},
			wantR: []Role{RoleTest},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAgentsCommands(tt.body)
			if len(got) != len(tt.want) {
				t.Fatalf("declared %v, want %v", got, tt.want)
			}
			for role, wantCmd := range tt.want {
				if cmd, ok := got.Get(role); !ok || cmd != wantCmd {
					t.Errorf("role %s = %q (ok=%v), want %q", role, cmd, ok, wantCmd)
				}
			}
			if tt.wantR != nil {
				if roles := got.Roles(); len(roles) != len(tt.wantR) {
					t.Fatalf("roles = %v, want %v", roles, tt.wantR)
				} else {
					for i := range roles {
						if roles[i] != tt.wantR[i] {
							t.Errorf("roles = %v, want %v", roles, tt.wantR)
						}
					}
				}
			}
		})
	}
}

// goDiscovered is a fixture discovery result: the full Go convention set
// from go.mod.
func goDiscovered() Commands {
	return Commands{
		Format: Command{Cmd: "gofmt -w {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
		Lint:   Command{Cmd: "go vet {dir}", Extends: []string{".go"}, Source: "go.mod"},
		Test:   Command{Cmd: "go test ./...", Source: "go.mod"},
		Build:  Command{Cmd: "go build ./...", Source: "go.mod"},
		Roots:  []string{"go.mod"},
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name       string
		discovered Commands
		config     Declared
		agents     Declared
		// want pins each role's final (cmd, source); absent roles are
		// checked for absence when wantAbsent is set.
		want       map[Role]Command
		wantAbsent []Role
		wantRoots  []string
		// instructionsFile is the resolved instruction file's name (the
		// Source label for a winning instruction-file declaration).
		instructionsFile string
	}{
		{
			name:       "discovery-only",
			discovered: goDiscovered(),
			want: map[Role]Command{
				RoleFormat: {Cmd: "gofmt -w {file}", PerFile: true, Source: "go.mod"},
				RoleLint:   {Cmd: "go vet {dir}", Source: "go.mod"},
				RoleTest:   {Cmd: "go test ./...", Source: "go.mod"},
				RoleBuild:  {Cmd: "go build ./...", Source: "go.mod"},
			},
			wantRoots: []string{"go.mod"},
		},
		{
			name:             "agents-md-beats-discovery",
			discovered:       goDiscovered(),
			agents:           Declared{RoleFormat: "prettier --write {file}"},
			instructionsFile: "AGENTS.md",
			want: map[Role]Command{
				RoleFormat: {Cmd: "prettier --write {file}", PerFile: true, Source: "AGENTS.md"},
				RoleLint:   {Cmd: "go vet {dir}", Source: "go.mod"},
				RoleTest:   {Cmd: "go test ./...", Source: "go.mod"},
				RoleBuild:  {Cmd: "go build ./...", Source: "go.mod"},
			},
			wantRoots: []string{"go.mod"},
		},
		{
			name:             "config-beats-agents-beats-discovery",
			discovered:       goDiscovered(),
			config:           Declared{RoleFormat: "custom-fmt -w {file}", RoleTest: "go test -race ./..."},
			agents:           Declared{RoleFormat: "prettier --write {file}", RoleLint: "golangci-lint run"},
			instructionsFile: "CLAUDE.md",
			want: map[Role]Command{
				// config wins over the instruction file for format.
				RoleFormat: {Cmd: "custom-fmt -w {file}", PerFile: true, Source: SourceConfig},
				// config silent, the instruction file (CLAUDE.md) wins over
				// discovery for lint and is labeled by the file it came from.
				RoleLint: {Cmd: "golangci-lint run", Source: "CLAUDE.md"},
				// config wins over discovery for test.
				RoleTest: {Cmd: "go test -race ./...", Source: SourceConfig},
				// nobody declared: discovery.
				RoleBuild: {Cmd: "go build ./...", Source: "go.mod"},
			},
			wantRoots: []string{"go.mod"},
		},
		{
			name:             "declaration-adding-a-role",
			discovered:       Commands{Test: Command{Cmd: "go test ./...", Source: "go.mod"}, Roots: []string{"go.mod"}},
			agents:           Declared{RoleFormat: "black {file}", RoleBuild: "python -m build"},
			instructionsFile: "AGENTS.md",
			want: map[Role]Command{
				RoleFormat: {Cmd: "black {file}", PerFile: true, Source: "AGENTS.md"},
				RoleTest:   {Cmd: "go test ./...", Source: "go.mod"},
				RoleBuild:  {Cmd: "python -m build", Source: "AGENTS.md"},
			},
			wantAbsent: []Role{RoleLint},
			wantRoots:  []string{"go.mod"},
		},
		{
			name:       "fully-overridden-manifest-drops-from-roots",
			discovered: goDiscovered(),
			config:     Declared{RoleFormat: "a", RoleLint: "b", RoleTest: "c", RoleBuild: "d"},
			want: map[Role]Command{
				RoleFormat: {Cmd: "a", Source: SourceConfig},
				RoleLint:   {Cmd: "b", Source: SourceConfig},
				RoleTest:   {Cmd: "c", Source: SourceConfig},
				RoleBuild:  {Cmd: "d", Source: SourceConfig},
			},
			wantRoots: nil,
		},
		{
			name:             "empty-discovery-pure-declaration",
			discovered:       Commands{},
			agents:           Declared{RoleTest: "node --test"},
			config:           Declared{RoleFormat: "npx prettier --write {file}"},
			instructionsFile: "AGENTS.md",
			want: map[Role]Command{
				RoleTest:   {Cmd: "node --test", Source: "AGENTS.md"},
				RoleFormat: {Cmd: "npx prettier --write {file}", PerFile: true, Source: SourceConfig},
			},
			wantAbsent: []Role{RoleLint, RoleBuild},
			wantRoots:  nil,
		},
		{
			name:       "blank-declaration-falls-through",
			discovered: goDiscovered(),
			config:     Declared{RoleFormat: "   "},
			want: map[Role]Command{
				RoleFormat: {Cmd: "gofmt -w {file}", PerFile: true, Source: "go.mod"},
			},
			wantRoots: []string{"go.mod"},
		},
		{
			name:             "resolve-does-not-mutate-input",
			discovered:       goDiscovered(),
			agents:           Declared{RoleFormat: "other {file}"},
			instructionsFile: "AGENTS.md",
			want: map[Role]Command{
				RoleFormat: {Cmd: "other {file}", PerFile: true, Source: "AGENTS.md"},
			},
			wantRoots: []string{"go.mod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.discovered
			got := Resolve(in, tt.config, tt.agents, tt.instructionsFile)

			for role, want := range tt.want {
				cmd, ok := got.Get(role)
				if !ok {
					t.Errorf("role %s: not present, want %q", role, want.Cmd)
					continue
				}
				if cmd.Cmd != want.Cmd || cmd.Source != want.Source || cmd.PerFile != want.PerFile {
					t.Errorf("role %s = %+v, want %+v", role, cmd, want)
				}
			}
			for _, role := range tt.wantAbsent {
				if _, ok := got.Get(role); ok {
					t.Errorf("role %s: present, want absent", role)
				}
			}
			if len(tt.wantRoots) == 0 && len(got.Roots) != 0 {
				t.Errorf("roots = %v, want none", got.Roots)
			}
			if len(tt.wantRoots) > 0 {
				if len(got.Roots) != len(tt.wantRoots) {
					t.Errorf("roots = %v, want %v", got.Roots, tt.wantRoots)
				}
				for i := range tt.wantRoots {
					if got.Roots[i] != tt.wantRoots[i] {
						t.Errorf("roots = %v, want %v", got.Roots, tt.wantRoots)
					}
				}
			}
			// The input value must survive untouched: Resolve shares its
			// backing structures with the caller (the session caches the
			// discovery), so mutation would leak across turns.
			if in.Format.Cmd != tt.discovered.Format.Cmd ||
				len(in.Roots) != len(tt.discovered.Roots) {
				t.Errorf("Resolve mutated its input: %+v (original %+v)", in, tt.discovered)
			}
		})
	}
}

func TestCommandsSet(t *testing.T) {
	var c Commands
	c.Set(RoleTest, Command{Cmd: "cargo test"})
	if cmd, ok := c.Get(RoleTest); !ok || cmd.Cmd != "cargo test" {
		t.Errorf("Set/Get round trip = %+v (ok=%v)", cmd, ok)
	}
	c.Set(Role("bogus"), Command{Cmd: "x"})
	if got := c.Render(); !strings.Contains(got, "(none discovered)") {
		t.Errorf("Set with an unknown role must be a no-op; render = %q", got)
	}
}
