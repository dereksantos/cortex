// Package events defines the generic event format for all AI tool integrations
package events

import (
	"encoding/json"
	"time"
)

// Source represents the AI tool that generated the event
type Source string

const (
	SourceClaude Source = "claude"
	SourceCursor Source = "cursor"
	// Reserved for future integrations:
	SourceCopilot  Source = "copilot"
	SourceWindsurf Source = "windsurf"
	SourceGeneric  Source = "generic"
)

// EventType categorizes the type of development event
type EventType string

const (
	EventToolUse EventType = "tool_use"
	// Reserved for future event types:
	EventEdit   EventType = "edit"
	EventSearch EventType = "search"
	EventAgent  EventType = "agent"
	EventBuild  EventType = "build"
	EventTest   EventType = "test"
)

// Event is the generic event structure for all AI tools
type Event struct {
	// Core identification
	ID        string    `json:"id"`
	Source    Source    `json:"source"`
	EventType EventType `json:"event_type"`
	Timestamp time.Time `json:"timestamp"`

	// Tool information
	ToolName   string                 `json:"tool_name"`
	ToolInput  map[string]interface{} `json:"tool_input"`
	ToolResult string                 `json:"tool_result,omitempty"`

	// Context
	Context EventContext `json:"context"`

	// Optional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// EventContext provides environmental context for the event
type EventContext struct {
	ProjectPath string `json:"project_path"`
	SessionID   string `json:"session_id"`
	UserID      string `json:"user_id,omitempty"`
	Branch      string `json:"branch,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
}

// ToJSON serializes the event to JSON
func (e *Event) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

// FromJSON deserializes JSON to an Event
func FromJSON(data []byte) (*Event, error) {
	var event Event
	err := json.Unmarshal(data, &event)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// ShouldCapture determines if an event should be captured based on quick filters
func (e *Event) ShouldCapture(skipPatterns []string) bool {
	// Skip routine commands
	if e.ToolName == "Bash" {
		if cmd, ok := e.ToolInput["command"].(string); ok {
			routineCommands := []string{"ls", "pwd", "echo", "cd", "which", "date"}
			for _, routine := range routineCommands {
				if cmd == routine {
					return false
				}
			}
		}
	}

	// Skip based on patterns
	for _, pattern := range skipPatterns {
		// Check tool result
		if contains(e.ToolResult, pattern) {
			return false
		}

		// Check file paths in tool input
		if filePath, ok := e.ToolInput["file_path"].(string); ok {
			if contains(filePath, pattern) {
				return false
			}
		}
	}

	return true
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		(s == substr || (len(s) >= len(substr) && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
