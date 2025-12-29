// Package claude provides Claude Code integration
package claude

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

// ClaudeHookEvent represents the full event format from Claude Code hooks
type ClaudeHookEvent struct {
	// Common fields (all hooks)
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	HookEventName  string `json:"hook_event_name"`

	// Tool fields (PostToolUse, PreToolUse)
	ToolName     string                 `json:"tool_name,omitempty"`
	ToolInput    map[string]interface{} `json:"tool_input,omitempty"`
	ToolResult   string                 `json:"tool_result,omitempty"`   // Legacy field
	ToolResponse interface{}            `json:"tool_response,omitempty"` // New field from hooks
	ToolUseID    string                 `json:"tool_use_id,omitempty"`

	// UserPromptSubmit fields
	Prompt string `json:"prompt,omitempty"`

	// Stop fields
	StopHookActive bool `json:"stop_hook_active,omitempty"`

	// SessionStart fields
	Source string `json:"source,omitempty"` // startup, resume, clear, compact
}

// ConvertToEvent converts a Claude hook event to our generic event format
func ConvertToEvent(data []byte, projectPath string) (*events.Event, error) {
	var hookEvent ClaudeHookEvent
	if err := json.Unmarshal(data, &hookEvent); err != nil {
		return nil, fmt.Errorf("failed to parse Claude hook event: %w", err)
	}

	// Generate event ID
	eventID := fmt.Sprintf("claude-%d", time.Now().UnixNano())

	// Determine session ID (use provided or generate)
	sessionID := hookEvent.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("claude-%d", time.Now().Unix())
	}

	// Create base event
	event := &events.Event{
		ID:             eventID,
		Source:         events.SourceClaude,
		Timestamp:      time.Now(),
		TranscriptPath: hookEvent.TranscriptPath,
		Context: events.EventContext{
			ProjectPath: projectPath,
			SessionID:   sessionID,
			WorkingDir:  hookEvent.Cwd,
		},
	}

	// Set event type and fields based on hook type
	switch hookEvent.HookEventName {
	case "UserPromptSubmit":
		event.EventType = events.EventUserPrompt
		event.Prompt = hookEvent.Prompt

	case "Stop", "SubagentStop":
		event.EventType = events.EventStop
		if hookEvent.StopHookActive {
			event.Metadata = map[string]interface{}{
				"stop_hook_active": true,
			}
		}

	case "PostToolUse", "PreToolUse":
		event.EventType = events.EventToolUse
		event.ToolName = hookEvent.ToolName
		event.ToolInput = hookEvent.ToolInput
		// Prefer tool_response over tool_result if available
		if hookEvent.ToolResponse != nil {
			if respStr, ok := hookEvent.ToolResponse.(string); ok {
				event.ToolResult = respStr
			} else if respBytes, err := json.Marshal(hookEvent.ToolResponse); err == nil {
				event.ToolResult = string(respBytes)
			}
		} else {
			event.ToolResult = hookEvent.ToolResult
		}

	default:
		// Default to tool_use for backwards compatibility
		event.EventType = events.EventToolUse
		event.ToolName = hookEvent.ToolName
		event.ToolInput = hookEvent.ToolInput
		event.ToolResult = hookEvent.ToolResult
	}

	return event, nil
}

// ConvertPromptEvent creates a UserPrompt event from inject-context hook data
func ConvertPromptEvent(data []byte, projectPath string) (*events.Event, error) {
	var hookEvent ClaudeHookEvent
	if err := json.Unmarshal(data, &hookEvent); err != nil {
		return nil, fmt.Errorf("failed to parse Claude hook event: %w", err)
	}

	// Skip if no prompt
	if hookEvent.Prompt == "" {
		return nil, nil
	}

	eventID := fmt.Sprintf("claude-%d", time.Now().UnixNano())
	sessionID := hookEvent.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("claude-%d", time.Now().Unix())
	}

	return &events.Event{
		ID:             eventID,
		Source:         events.SourceClaude,
		EventType:      events.EventUserPrompt,
		Timestamp:      time.Now(),
		Prompt:         hookEvent.Prompt,
		TranscriptPath: hookEvent.TranscriptPath,
		Context: events.EventContext{
			ProjectPath: projectPath,
			SessionID:   sessionID,
			WorkingDir:  hookEvent.Cwd,
		},
	}, nil
}

// ConvertStopEvent creates a Stop event from the Stop hook data
func ConvertStopEvent(data []byte, projectPath string) (*events.Event, error) {
	var hookEvent ClaudeHookEvent
	if err := json.Unmarshal(data, &hookEvent); err != nil {
		return nil, fmt.Errorf("failed to parse Claude hook event: %w", err)
	}

	eventID := fmt.Sprintf("claude-%d", time.Now().UnixNano())
	sessionID := hookEvent.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("claude-%d", time.Now().Unix())
	}

	return &events.Event{
		ID:             eventID,
		Source:         events.SourceClaude,
		EventType:      events.EventStop,
		Timestamp:      time.Now(),
		TranscriptPath: hookEvent.TranscriptPath,
		Context: events.EventContext{
			ProjectPath: projectPath,
			SessionID:   sessionID,
			WorkingDir:  hookEvent.Cwd,
		},
		Metadata: map[string]interface{}{
			"stop_hook_active": hookEvent.StopHookActive,
		},
	}, nil
}
