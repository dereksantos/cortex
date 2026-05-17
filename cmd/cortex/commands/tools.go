// tools.go — `cortex tools` writes the generated tools.json manifest to
// disk (or stdout). The committed file at repo root is the source of
// truth callers (Aider, Claude Code, MCP server) read; `go generate`
// and `go test ./cmd/cortex/commands/...` both verify it matches what
// the registry currently produces.
//
//go:generate go run ../ tools --out ../../../tools.json

package commands

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
)

func init() {
	Register(&ToolsCommand{})
}

// ToolsCommand emits the tool-surface manifest.
type ToolsCommand struct{}

// Name returns the command name.
func (c *ToolsCommand) Name() string { return "tools" }

// Description returns a brief description.
func (c *ToolsCommand) Description() string {
	return "Generate the tools.json manifest from the command registry"
}

// SurfaceVersion lets the manifest stamp the tools subcommand at its
// own version independent of the binary. Bump when the generator's
// output schema changes in a way callers should notice.
func (c *ToolsCommand) SurfaceVersion() string { return "1.0.0" }

// DescribeFlags surfaces the tools subcommand's own flags into the
// manifest. Recursive in spirit — `cortex tools` describes itself the
// same way it describes every other command.
func (c *ToolsCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.String("out", "tools.json", "Path to write manifest to (use '-' for stdout)")
	fs.String("version", "", "Version string stamped on tools without their own SurfaceVersion (defaults to binary version)")
	fs.Bool("check", false, "Verify the committed manifest matches the generator output; exit 1 on drift")
}

// Execute parses flags and writes (or checks) the manifest.
func (c *ToolsCommand) Execute(ctx *Context) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	out := fs.String("out", "tools.json", "Path to write manifest to (use '-' for stdout)")
	version := fs.String("version", "", "Version string stamped on tools without their own SurfaceVersion (defaults to binary version)")
	check := fs.Bool("check", false, "Verify the committed manifest matches the generator output; exit 1 on drift")
	if err := fs.Parse(ctx.Args); err != nil {
		return err
	}

	fallback := *version
	if fallback == "" {
		fallback = BinaryVersion
	}

	manifest := GenerateManifest(fallback)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n') // trailing newline so git doesn't complain

	if *check {
		return checkManifest(*out, data)
	}

	if *out == "-" {
		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "cortex tools: wrote %s (%d tools)\n", *out, len(manifest.Tools))
	return nil
}

// checkManifest compares the on-disk file at path to the generator's
// expected output. Returns an actionable error on drift so CI / the
// pre-commit hook prints something the user can paste into a shell.
func checkManifest(path string, expected []byte) error {
	if path == "-" {
		return errors.New("--check requires a file path, not stdout")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w (run `cortex tools` to generate)", path, err)
	}
	if string(got) == string(expected) {
		fmt.Fprintf(os.Stderr, "cortex tools: %s matches generator output\n", path)
		return nil
	}
	return fmt.Errorf("%s is out of date — regenerate with `go run ./cmd/cortex tools --out %s`", path, path)
}

// BinaryVersion is the fallback version stamped onto manifest entries
// that don't implement Versioner. main.go overrides this at startup so
// the manifest stays in sync with the binary; the default keeps
// standalone tests / `go run` invocations producing a valid manifest.
var BinaryVersion = "0.1.0"
