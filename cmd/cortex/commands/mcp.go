package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dereksantos/cortex/internal/mcp"
)

// MCPCommand starts the MCP server over stdio.
type MCPCommand struct{}

func init() {
	Register(&MCPCommand{})
}

// Name returns the command name.
func (c *MCPCommand) Name() string { return "mcp" }

// Description returns the command description.
func (c *MCPCommand) Description() string { return "Start MCP server (for cross-tool access)" }

// Execute runs the MCP server.
func (c *MCPCommand) Execute(ctx *Context) error {
	for _, arg := range ctx.Args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintln(os.Stderr, "Usage: cortex mcp")
			fmt.Fprintln(os.Stderr, "\nStart the MCP (Model Context Protocol) server over stdio.")
			fmt.Fprintln(os.Stderr, "This allows MCP-compatible tools (Claude Code, Cursor, etc.) to access Cortex context.")
			fmt.Fprintln(os.Stderr, "\nThe server reads JSON-RPC 2.0 from stdin and writes responses to stdout.")
			return nil
		}
	}

	server := mcp.NewServer(ctx.Config, ctx.Storage)

	// Set up signal handling for graceful shutdown
	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "MCP server shutting down...")
		cancel()
	}()

	// Log to stderr only (stdout is for MCP protocol)
	fmt.Fprintln(os.Stderr, "Cortex MCP server started")

	return server.Serve(bgCtx)
}
