// Declaration is the other half of project commands: instead of being
// discovered from manifest files, the project names its own commands in
// a `project.commands` section of .cortex/config.json or in a
// `## Commands` section of AGENTS.md (the cross-harness convention
// Cortex already reads into its seed).
//
// Declaration beats discovery field-by-field, and a config declaration
// beats an AGENTS.md declaration (issue #129: discovery is an inference
// from manifests; a declaration is the project's own word).
package projectcmd

import (
	"strings"
)

// Source labels for declared (vs discovered) commands, so a rendered
// report can show where each command came from. Discovered commands
// carry their manifest name ("go.mod", ...) as Source.
const (
	SourceConfig = "config.json"
	SourceAgents = "AGENTS.md"
)

// Declared is a set of explicitly declared commands, keyed by role.
// Absent (or blank) roles fall through to discovery in Resolve.
type Declared map[Role]string

// Get returns the declared command for role and whether a non-blank
// one was declared.
func (d Declared) Get(role Role) (string, bool) {
	cmd, ok := d[role]
	return cmd, ok && strings.TrimSpace(cmd) != ""
}

// Roles lists the declared roles in canonical order (format, lint,
// test, build) — a stable order for rendering and tests, unlike map
// iteration.
func (d Declared) Roles() []Role {
	var roles []Role
	for _, r := range []Role{RoleFormat, RoleLint, RoleTest, RoleBuild} {
		if _, ok := d.Get(r); ok {
			roles = append(roles, r)
		}
	}
	return roles
}

// ParseAgentsCommands extracts the declared commands from an AGENTS.md
// body: the "- role: command" list entries of the last "## Commands"
// section, roles lowercased, restricted to the known roles. Anything
// else in the file — a "commands" word in prose, a differently-named
// section, list items with no "role: command" shape — is ignored. An
// empty body or one without the section yields an empty Declared.
func ParseAgentsCommands(body string) Declared {
	out := Declared{}
	for _, line := range agentsCommandsSection(body) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "*") {
			continue
		}
		item := strings.TrimSpace(strings.TrimLeft(trimmed, "-*"))
		idx := strings.Index(item, ":")
		if idx <= 0 {
			continue
		}
		role := Role(strings.ToLower(strings.TrimSpace(item[:idx])))
		if !RoleKnown(role) {
			continue
		}
		cmd := strings.TrimSpace(item[idx+1:])
		if cmd == "" {
			continue
		}
		out[role] = cmd
	}
	return out
}

// agentsCommandsSection returns the lines of the last "## Commands"
// section — from just under its header up to the next ATX header of any
// level, or the end of the body. The header name is case- and
// whitespace-insensitive ("## commands", "##  COMMANDS" both match);
// a header other than "Commands" ends the section.
func agentsCommandsSection(body string) []string {
	var section []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		name, isHeader := atxHeader(line)
		if isHeader {
			in = strings.EqualFold(name, "Commands")
			continue
		}
		if in {
			section = append(section, line)
		}
	}
	return section
}

// atxHeader reports whether line is an ATX markdown header and, if so,
// its trimmed name ("## Commands" -> "Commands").
func atxHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	return name, name != ""
}

// RoleKnown reports whether role is one of the four command roles
// (format, lint, test, build). Config loaders use it to filter declared
// role keys to the live set.
func RoleKnown(role Role) bool {
	switch role {
	case RoleFormat, RoleLint, RoleTest, RoleBuild:
		return true
	}
	return false
}

// Resolve applies declarations over discovery, field by field: a
// command the project declared wins over one the manifests implied, a
// config declaration beats an AGENTS.md declaration for the same role,
// and a role no source declared stays discovered. Roots is pruned to the
// manifests that still supply a final command — a fully overridden
// manifest is no longer a source of the set.
func Resolve(discovered Commands, configDeclared, agentsDeclared Declared) Commands {
	out := discovered
	for _, role := range []Role{RoleFormat, RoleLint, RoleTest, RoleBuild} {
		cmd, source, ok := resolveRole(role, configDeclared, agentsDeclared)
		if !ok {
			continue
		}
		out.Set(role, Command{Cmd: cmd, PerFile: strings.Contains(cmd, FilePlaceholder), Source: source})
	}

	supplied := map[string]bool{}
	for _, cmd := range []Command{out.Format, out.Lint, out.Test, out.Build} {
		if cmd.Cmd != "" {
			supplied[cmd.Source] = true
		}
	}
	// A fresh slice: in-place compaction would write through the shared
	// backing array and corrupt the caller's discovered Roots.
	roots := make([]string, 0, len(out.Roots))
	for _, root := range out.Roots {
		if supplied[root] {
			roots = append(roots, root)
		}
	}
	out.Roots = roots
	return out
}

// resolveRole picks the winning command for one role: config beats
// AGENTS.md beats nothing. ok=false leaves the discovered command.
func resolveRole(role Role, configDeclared, agentsDeclared Declared) (cmd, source string, ok bool) {
	if cmd, declared := configDeclared.Get(role); declared {
		return cmd, SourceConfig, true
	}
	if cmd, declared := agentsDeclared.Get(role); declared {
		return cmd, SourceAgents, true
	}
	return "", "", false
}

// Set stores a command under role (nil-safe: no role matched means the
// Commands value is left untouched).
func (c *Commands) Set(role Role, cmd Command) {
	switch role {
	case RoleFormat:
		c.Format = cmd
	case RoleLint:
		c.Lint = cmd
	case RoleTest:
		c.Test = cmd
	case RoleBuild:
		c.Build = cmd
	}
}
