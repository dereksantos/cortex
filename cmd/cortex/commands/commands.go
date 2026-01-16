// Package commands provides a command interface and registry for the cortex CLI.
package commands

import (
	"sort"

	"github.com/dereksantos/cortex/internal/storage"
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
