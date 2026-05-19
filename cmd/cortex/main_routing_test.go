// main_routing_test.go ensures every command registered via
// commands.Register has a matching `case "<name>":` arm in main.go's
// top-level switch. Catches the "register a new command but forget to
// route it" failure mode the multi-case switch otherwise hides.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/cmd/cortex/commands"
)

// routedCommands walks main.go's AST and returns the set of string
// literals used as `case` values inside its top-level switch. We parse
// the source file directly rather than reflecting on a runtime value
// because the dispatch is a switch on a string variable, not a map we
// can read at runtime.
func routedCommands(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	routed := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, e := range cc.List {
			lit, ok := e.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			s := strings.Trim(lit.Value, `"`)
			if s != "" {
				routed[s] = true
			}
		}
		return true
	})
	return routed
}

// metaCommands are routed in main.go but aren't registered through
// commands.Register (help / version / shell flags). They live in the
// switch but have no Command struct behind them.
var metaCommands = map[string]bool{
	"help":    true,
	"-h":      true,
	"--help":  true,
	"version": true,
}

// TestEveryRegisteredCommandIsRouted is the regression guard: any
// command we Register but forget to wire into main.go's switch will
// fail this test instead of silently returning "unknown command" at
// runtime.
func TestEveryRegisteredCommandIsRouted(t *testing.T) {
	routed := routedCommands(t)

	var missing []string
	for _, name := range commands.List() {
		if metaCommands[name] {
			continue
		}
		if !routed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("commands registered but not routed in main.go: %v\n"+
			"add a matching `case \"<name>\":` arm to main.go", missing)
	}
}

// TestEveryRoutedCommandIsRegistered catches the inverse drift: a
// `case "<name>":` arm in main.go whose name no longer has a registered
// Command struct (e.g. forgotten leftover after deleting a command).
// metaCommands are exempt because they're handled inline.
func TestEveryRoutedCommandIsRegistered(t *testing.T) {
	routed := routedCommands(t)
	registered := map[string]bool{}
	for _, name := range commands.List() {
		registered[name] = true
	}

	var orphaned []string
	for name := range routed {
		if metaCommands[name] {
			continue
		}
		if !registered[name] {
			orphaned = append(orphaned, name)
		}
	}
	if len(orphaned) > 0 {
		t.Fatalf("main.go routes these commands but no Command is registered for them: %v\n"+
			"either Register the command or remove the stale `case`", orphaned)
	}
}
