// Package claude provides Claude Code integration
package claude

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

// ClaudeHookEvent represents the event format from Claude Code hooks
type ClaudeHookEvent struct {
	ToolName   string                 `json:"tool_name"`
	ToolInput  map[string]interface{} `json:"tool_input"`
	ToolResult string                 `json:"tool_result"`
}

// ConvertToEvent converts a Claude hook event to our generic event format
func ConvertToEvent(data []byte, projectPath string) (*events.Event, error) {
	var hookEvent ClaudeHookEvent
	if err := json.Unmarshal(data, &hookEvent); err != nil {
		return nil, fmt.Errorf("failed to parse Claude hook event: %w", err)
	}

	// Generate event ID
	eventID := fmt.Sprintf("claude-%d", time.Now().UnixNano())

	// Create generic event
	event := &events.Event{
		ID:         eventID,
		Source:     events.SourceClaude,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   hookEvent.ToolName,
		ToolInput:  hookEvent.ToolInput,
		ToolResult: hookEvent.ToolResult,
		Context: events.EventContext{
			ProjectPath: projectPath,
			SessionID:   fmt.Sprintf("claude-%d", time.Now().Unix()),
		},
	}

	return event, nil
}
