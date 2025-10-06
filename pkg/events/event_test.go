package events

import (
	"testing"
	"time"
)

func TestEvent_ToJSON(t *testing.T) {
	event := &Event{
		ID:        "test-123",
		Source:    SourceClaude,
		EventType: EventEdit,
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{
			"file_path": "test.go",
		},
		ToolResult: "success",
		Context: EventContext{
			ProjectPath: "/test/project",
			SessionID:   "session-1",
		},
	}

	data, err := event.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("ToJSON returned empty data")
	}
}

func TestFromJSON(t *testing.T) {
	jsonData := []byte(`{
		"id": "test-123",
		"source": "claude",
		"event_type": "edit",
		"timestamp": "2025-01-01T12:00:00Z",
		"tool_name": "Edit",
		"tool_input": {"file_path": "test.go"},
		"tool_result": "success",
		"context": {
			"project_path": "/test/project",
			"session_id": "session-1"
		}
	}`)

	event, err := FromJSON(jsonData)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}

	if event.ID != "test-123" {
		t.Errorf("Expected ID 'test-123', got '%s'", event.ID)
	}

	if event.Source != SourceClaude {
		t.Errorf("Expected source 'claude', got '%s'", event.Source)
	}

	if event.ToolName != "Edit" {
		t.Errorf("Expected ToolName 'Edit', got '%s'", event.ToolName)
	}
}

func TestEvent_ShouldCapture(t *testing.T) {
	tests := []struct {
		name     string
		event    *Event
		patterns []string
		want     bool
	}{
		{
			name: "should capture normal edit",
			event: &Event{
				ToolName: "Edit",
				ToolInput: map[string]interface{}{
					"file_path": "main.go",
				},
			},
			patterns: []string{".git", "node_modules"},
			want:     true,
		},
		{
			name: "should skip routine ls command",
			event: &Event{
				ToolName: "Bash",
				ToolInput: map[string]interface{}{
					"command": "ls",
				},
			},
			patterns: []string{},
			want:     false,
		},
		{
			name: "should skip files matching skip patterns",
			event: &Event{
				ToolName: "Edit",
				ToolInput: map[string]interface{}{
					"file_path": "node_modules/package.json",
				},
			},
			patterns: []string{"node_modules"},
			want:     false,
		},
		{
			name: "should skip git directory",
			event: &Event{
				ToolName: "Write",
				ToolInput: map[string]interface{}{
					"file_path": ".git/config",
				},
			},
			patterns: []string{".git"},
			want:     false,
		},
		{
			name: "should capture non-routine bash",
			event: &Event{
				ToolName: "Bash",
				ToolInput: map[string]interface{}{
					"command": "go build",
				},
			},
			patterns: []string{},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.event.ShouldCapture(tt.patterns)
			if got != tt.want {
				t.Errorf("ShouldCapture() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvent_RoundTrip(t *testing.T) {
	// Test that we can serialize and deserialize without data loss
	original := &Event{
		ID:        "round-trip-123",
		Source:    SourceCursor,
		EventType: EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Write",
		ToolInput: map[string]interface{}{
			"file_path": "test.go",
			"content":   "package main",
		},
		ToolResult: "File written successfully",
		Context: EventContext{
			ProjectPath: "/home/user/project",
			SessionID:   "session-abc",
			UserID:      "user-123",
			Branch:      "main",
		},
		Metadata: map[string]interface{}{
			"custom_field": "custom_value",
		},
	}

	// Serialize
	jsonData, err := original.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	// Deserialize
	restored, err := FromJSON(jsonData)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}

	// Compare
	if restored.ID != original.ID {
		t.Errorf("ID mismatch: got %s, want %s", restored.ID, original.ID)
	}

	if restored.Source != original.Source {
		t.Errorf("Source mismatch: got %s, want %s", restored.Source, original.Source)
	}

	if restored.ToolName != original.ToolName {
		t.Errorf("ToolName mismatch: got %s, want %s", restored.ToolName, original.ToolName)
	}

	if restored.Context.ProjectPath != original.Context.ProjectPath {
		t.Errorf("ProjectPath mismatch: got %s, want %s",
			restored.Context.ProjectPath, original.Context.ProjectPath)
	}
}
