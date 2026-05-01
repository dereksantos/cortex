# Cursor Integration for Cortex

> **Status: Planned, not yet functional.** This document describes the intended integration approach. The Go adapter (`adapter.go`) exists, but no shipping Cursor extension or LSP bridge has been built or tested end-to-end. The MCP-server path (see bottom) is the most likely route once the MCP server is validated. Treat the setup methods below as design notes, not working instructions.

## Setup Methods (planned)

### Method 1: Direct LSP Integration (Recommended)

Cursor can send LSP notifications to external tools. Configure Cursor to send notifications to Cortex:

1. **Create a Cursor extension wrapper** (one-time setup):

```bash
# Create extension directory
mkdir -p ~/.cursor/extensions/cortex-capture

# Create extension manifest
cat > ~/.cursor/extensions/cortex-capture/package.json <<'EOF'
{
  "name": "cortex-capture",
  "version": "0.1.0",
  "description": "Cortex context capture for Cursor",
  "main": "extension.js",
  "contributes": {
    "commands": [
      {
        "command": "cortex.capture",
        "title": "Cortex: Capture Event"
      }
    ]
  }
}
EOF

# Create extension code
cat > ~/.cursor/extensions/cortex-capture/extension.js <<'EOF'
const vscode = require('vscode');
const { exec } = require('child_process');

function activate(context) {
    // Listen to document changes
    vscode.workspace.onDidChangeTextDocument((event) => {
        const doc = event.document;
        const notification = {
            method: 'textDocument/didChange',
            params: {
                textDocument: { uri: doc.uri.toString() },
                contentChanges: event.contentChanges.map(c => ({
                    text: c.text
                }))
            }
        };
        captureEvent(notification);
    });

    // Listen to document saves
    vscode.workspace.onDidSaveTextDocument((doc) => {
        const notification = {
            method: 'textDocument/didSave',
            params: {
                textDocument: { uri: doc.uri.toString() }
            }
        };
        captureEvent(notification);
    });
}

function captureEvent(notification) {
    const json = JSON.stringify(notification);
    exec(`echo '${json}' | cortex capture --source cursor`, (error) => {
        if (error) {
            console.error('Cortex capture failed:', error);
        }
    });
}

module.exports = { activate };
EOF
```

2. **Install the extension in Cursor**:
   - Open Cursor
   - Go to Extensions
   - Click "Install from VSIX" or reload extensions
   - Enable "cortex-capture"

### Method 2: Generic stdin Wrapper

For simpler integration without extension development:

```bash
# Create a wrapper script
cat > ~/.local/bin/cursor-cortex-capture <<'EOF'
#!/bin/bash
# Cursor to Cortex event capture wrapper

# Read LSP notification from stdin
notification=$(cat)

# Send to Cortex
echo "$notification" | cortex capture --source cursor
EOF

chmod +x ~/.local/bin/cursor-cortex-capture
```

Then configure Cursor to pipe notifications to this script.

### Method 3: File Watcher (Fallback)

If direct LSP integration isn't possible, use file watching:

```bash
# Start Cortex in watch mode for Cursor workspace
cortex watch --ide cursor
```

This monitors file changes in your workspace and creates events automatically.

## Supported Events

Cortex captures the following Cursor events:

- **Document Changes** (`textDocument/didChange`) → `Edit` events
- **Document Saves** (`textDocument/didSave`) → `Write` events
- **Document Opens** (`textDocument/didOpen`) → `Read` events
- **AI Completions** (`$/ai/completion`) → `AICompletion` events
- **AI Chat** (`$/ai/chat`) → `AIChat` events
- **Cursor Edits** (`$/cursor/applyEdit`) → `CursorEdit` events

## Event Format

LSP notifications are converted to the generic Cortex event format:

```json
{
  "id": "cursor-1696800000000000000",
  "source": "cursor",
  "event_type": "tool_use",
  "timestamp": "2025-10-04T00:00:00Z",
  "tool_name": "Edit",
  "tool_input": {
    "file_path": "file:///path/to/file.go",
    "change_preview": "func main() {..."
  },
  "tool_result": "Modified: file:///path/to/file.go",
  "context": {
    "project_path": "/path/to/project",
    "session_id": "cursor-1696800000"
  }
}
```

## Testing

Test the integration:

```bash
# Send a sample LSP notification
echo '{
  "method": "textDocument/didSave",
  "params": {
    "textDocument": {
      "uri": "file:///Users/you/project/main.go"
    }
  }
}' | cortex capture --source cursor

# Verify it was captured
cortex recent 1
```

## Auto-Setup

Run auto-setup to detect and configure Cursor:

```bash
cortex init --auto
```

This will:
- Detect Cursor installation
- Configure capture hooks
- Validate the setup

## Troubleshooting

### Extension not loading
- Check Cursor's extension logs
- Verify cortex is in PATH: `which cortex`
- Test capture manually: `echo '{"method":"test"}' | cortex capture --source cursor`

### Events not appearing
- Check Cortex daemon is running: `cortex daemon`
- View queue status: `cortex stats`
- Check logs in `.cortex/logs/`

### Performance issues
- Cortex capture is <10ms, shouldn't impact editor
- Reduce captured events by filtering in extension
- Adjust processing interval in config

## Configuration

Configure Cursor-specific settings in `.cortex/config.json`:

```json
{
  "cursor": {
    "capture_ai_completions": true,
    "capture_file_changes": true,
    "debounce_ms": 500,
    "skip_patterns": ["node_modules", ".git", "vendor"]
  }
}
```

## Advanced: Custom Event Mapping

You can customize which LSP methods map to which tool names by modifying `integrations/cursor/adapter.go`:

```go
methodMap := map[string]string{
    "textDocument/didChange": "Edit",
    "your/custom/method":     "YourToolName",
}
```

## MCP Integration (Recommended for New Setups)

Cursor supports MCP (Model Context Protocol) servers natively. Once the Cortex MCP server is available (see [ROADMAP.md](../../ROADMAP.md)), this will be the preferred integration method:

```json
{
  "mcpServers": {
    "cortex": {
      "command": "cortex",
      "args": ["mcp-server"]
    }
  }
}
```

This provides `cortex_search`, `cortex_recall`, and `cortex_record` tools directly in Cursor's AI context, without requiring a custom extension.

## See Also

- [LSP Specification](https://microsoft.github.io/language-server-protocol/)
- [Cursor Documentation](https://cursor.sh/docs)
- [Cortex Integration Guide](../../README.md)
