// Package cursor provides Cursor IDE integration via LSP
package cursor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

// LSPNotification represents an LSP notification from Cursor
type LSPNotification struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

// CursorEvent represents a Cursor-specific event
type CursorEvent struct {
	Method    string                 `json:"method"`
	Params    map[string]interface{} `json:"params"`
	Timestamp time.Time              `json:"timestamp"`
}

// ConvertLSPToEvent converts an LSP notification to our generic event format
func ConvertLSPToEvent(data []byte, projectPath string) (*events.Event, error) {
	var notification LSPNotification
	if err := json.Unmarshal(data, &notification); err != nil {
		return nil, fmt.Errorf("failed to parse LSP notification: %w", err)
	}

	// Map LSP methods to our tool names
	toolName := mapLSPMethodToToolName(notification.Method)
	if toolName == "" {
		// Skip unknown methods
		return nil, fmt.Errorf("unsupported LSP method: %s", notification.Method)
	}

	// Extract relevant information from params
	toolInput := extractToolInput(notification.Params)
	toolResult := extractToolResult(notification.Params)

	// Generate event ID
	eventID := fmt.Sprintf("cursor-%d", time.Now().UnixNano())

	// Create generic event
	event := &events.Event{
		ID:         eventID,
		Source:     events.SourceCursor,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   toolName,
		ToolInput:  toolInput,
		ToolResult: toolResult,
		Context: events.EventContext{
			ProjectPath: projectPath,
			SessionID:   fmt.Sprintf("cursor-%d", time.Now().Unix()),
		},
	}

	return event, nil
}

// mapLSPMethodToToolName maps LSP notification methods to our tool names
func mapLSPMethodToToolName(method string) string {
	methodMap := map[string]string{
		"textDocument/didChange":   "Edit",
		"textDocument/didSave":     "Write",
		"textDocument/didOpen":     "Read",
		"textDocument/didClose":    "Close",
		"workspace/executeCommand": "Command",
		"$/ai/completion":          "AICompletion",
		"$/ai/chat":                "AIChat",
		"$/cursor/applyEdit":       "CursorEdit",
		"$/cursor/accept":          "CursorAccept",
		"$/cursor/reject":          "CursorReject",
	}

	if toolName, ok := methodMap[method]; ok {
		return toolName
	}

	return "" // Unknown method
}

// extractToolInput extracts relevant input parameters from LSP params
func extractToolInput(params map[string]interface{}) map[string]interface{} {
	input := make(map[string]interface{})

	// Extract text document URI
	if textDocument, ok := params["textDocument"].(map[string]interface{}); ok {
		if uri, ok := textDocument["uri"].(string); ok {
			input["file_path"] = uri
		}
	}

	// Extract command information
	if command, ok := params["command"].(string); ok {
		input["command"] = command
	}

	// Extract arguments
	if arguments, ok := params["arguments"].([]interface{}); ok && len(arguments) > 0 {
		input["arguments"] = arguments
	}

	// Extract content changes
	if contentChanges, ok := params["contentChanges"].([]interface{}); ok && len(contentChanges) > 0 {
		// Get the last change (most recent)
		if lastChange, ok := contentChanges[len(contentChanges)-1].(map[string]interface{}); ok {
			if text, ok := lastChange["text"].(string); ok {
				// Only store first 200 chars to avoid bloat
				if len(text) > 200 {
					input["change_preview"] = text[:200] + "..."
				} else {
					input["change_preview"] = text
				}
			}
		}
	}

	// Extract AI-specific params
	if prompt, ok := params["prompt"].(string); ok {
		input["prompt"] = prompt
	}

	return input
}

// extractToolResult extracts result information from LSP params
func extractToolResult(params map[string]interface{}) string {
	// For LSP notifications, we often don't have explicit results
	// But we can construct a meaningful summary

	if textDocument, ok := params["textDocument"].(map[string]interface{}); ok {
		if uri, ok := textDocument["uri"].(string); ok {
			return fmt.Sprintf("Modified: %s", uri)
		}
	}

	if command, ok := params["command"].(string); ok {
		return fmt.Sprintf("Executed: %s", command)
	}

	if result, ok := params["result"].(string); ok {
		return result
	}

	return "success"
}

// ConvertToEvent is an alias for ConvertLSPToEvent for consistency
func ConvertToEvent(data []byte, projectPath string) (*events.Event, error) {
	return ConvertLSPToEvent(data, projectPath)
}
