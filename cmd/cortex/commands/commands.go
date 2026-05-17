// Package commands provides a command interface and registry for the cortex CLI.
package commands

import (
	"flag"
	"sort"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cliout"
	"github.com/dereksantos/cortex/pkg/config"
)

// Context holds shared dependencies for command execution.
type Context struct {
	Config  *config.Config
	Storage *storage.Storage
	Args    []string
}

// Command defines the interface for CLI commands.
type Command interface {
	// Name returns the command name used to invoke it.
	Name() string
	// Description returns a brief description of what the command does.
	Description() string
	// Execute runs the command with the given context.
	Execute(ctx *Context) error
}

// FlagDescriber is optionally implemented by commands that want their
// flag surface to land in tools.json. The describer registers flags
// onto the passed FlagSet; the manifest generator reads back the
// flag's name, type, default, and usage. Commands that don't implement
// this are still listed in the manifest, just without a Flags field.
//
// The manifest generator passes a throwaway FlagSet — it never parses
// args through it — so describers must avoid side effects beyond the
// fs.* calls.
type FlagDescriber interface {
	DescribeFlags(fs *flag.FlagSet)
}

// ArgsDescriber is optionally implemented by commands with positional
// arguments. Returned slice is recorded verbatim into the manifest's
// ToolEntry.Args; order matters and matches consumption order.
type ArgsDescriber interface {
	DescribeArgs() []cliout.ArgSpec
}

// Versioner is optionally implemented by commands whose CLI surface
// evolves separately from the binary version. Commands that don't
// implement this fall back to the binary version stamped by the
// manifest generator's caller.
type Versioner interface {
	SurfaceVersion() string
}

// registry holds all registered commands.
var registry = make(map[string]Command)

// Register adds a command to the registry.
func Register(cmd Command) {
	registry[cmd.Name()] = cmd
}

// Get returns a command by name, or nil if not found.
func Get(name string) Command {
	return registry[name]
}

// List returns a sorted list of all registered command names.
func List() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
