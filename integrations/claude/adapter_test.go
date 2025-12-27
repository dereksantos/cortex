package claude

import (
	"encoding/json"
	"testing"

	"github.com/dereksantos/cortex/pkg/events"
)

func TestConvertToEvent(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		projectPath string
		wantErr     bool
		validate    func(*testing.T, *events.Event)
	}{
		{
			name: "valid JSON with all fields",
			data: []byte(`{
				"tool_name": "Edit",
				"tool_input": {"file_path": "/test/file.go", "content": "hello"},
				"tool_result": "success"
			}`),
			projectPath: "/test/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "Edit" {
					t.Errorf("expected ToolName 'Edit', got %q", e.ToolName)
				}
				if e.ToolResult != "success" {
					t.Errorf("expected ToolResult 'success', got %q", e.ToolResult)
				}
				if e.ToolInput["file_path"] != "/test/file.go" {
					t.Errorf("expected file_path '/test/file.go', got %v", e.ToolInput["file_path"])
				}
				if e.Context.ProjectPath != "/test/project" {
					t.Errorf("expected ProjectPath '/test/project', got %q", e.Context.ProjectPath)
				}
			},
		},
		{
			name: "valid JSON with empty tool_input",
			data: []byte(`{
				"tool_name": "Read",
				"tool_input": {},
				"tool_result": "file contents"
			}`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "Read" {
					t.Errorf("expected ToolName 'Read', got %q", e.ToolName)
				}
				if len(e.ToolInput) != 0 {
					t.Errorf("expected empty ToolInput, got %v", e.ToolInput)
				}
			},
		},
		{
			name: "valid JSON with missing optional fields",
			data: []byte(`{
				"tool_name": "Bash"
			}`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "Bash" {
					t.Errorf("expected ToolName 'Bash', got %q", e.ToolName)
				}
				if e.ToolResult != "" {
					t.Errorf("expected empty ToolResult, got %q", e.ToolResult)
				}
			},
		},
		{
			name:        "invalid JSON",
			data:        []byte(`{invalid json`),
			projectPath: "/project",
			wantErr:     true,
		},
		{
			name:        "empty data",
			data:        []byte(``),
			projectPath: "/project",
			wantErr:     true,
		},
		{
			name:        "null JSON unmarshals to zero values",
			data:        []byte(`null`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				// null JSON unmarshals to zero-value ClaudeHookEvent
				if e.ToolName != "" {
					t.Errorf("expected empty ToolName, got %q", e.ToolName)
				}
			},
		},
		{
			name: "complex tool_input",
			data: []byte(`{
				"tool_name": "Task",
				"tool_input": {
					"command": "test",
					"args": ["--verbose", "-n", "5"],
					"env": {"FOO": "bar"}
				},
				"tool_result": "completed"
			}`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "Task" {
					t.Errorf("expected ToolName 'Task', got %q", e.ToolName)
				}
				if e.ToolInput["command"] != "test" {
					t.Errorf("expected command 'test', got %v", e.ToolInput["command"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ConvertToEvent(tt.data, tt.projectPath)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if event == nil {
				t.Fatal("expected non-nil event")
			}

			// Validate common fields
			if event.Source != events.SourceClaude {
				t.Errorf("expected Source SourceClaude, got %v", event.Source)
			}

			if event.EventType != events.EventToolUse {
				t.Errorf("expected EventType EventToolUse, got %v", event.EventType)
			}

			if event.ID == "" {
				t.Error("expected non-empty ID")
			}

			if event.Timestamp.IsZero() {
				t.Error("expected non-zero Timestamp")
			}

			if event.Context.SessionID == "" {
				t.Error("expected non-empty SessionID")
			}

			// Run custom validation
			if tt.validate != nil {
				tt.validate(t, event)
			}
		})
	}
}

func TestConvertToEvent_IDFormat(t *testing.T) {
	data := []byte(`{"tool_name": "Test", "tool_input": {}, "tool_result": ""}`)

	event, err := ConvertToEvent(data, "/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ID should start with "claude-"
	if len(event.ID) < 7 || event.ID[:7] != "claude-" {
		t.Errorf("expected ID to start with 'claude-', got %q", event.ID)
	}

	// SessionID should start with "claude-"
	if len(event.Context.SessionID) < 7 || event.Context.SessionID[:7] != "claude-" {
		t.Errorf("expected SessionID to start with 'claude-', got %q", event.Context.SessionID)
	}
}

func TestConvertToEvent_MultipleEvents(t *testing.T) {
	data := []byte(`{"tool_name": "Test", "tool_input": {}, "tool_result": ""}`)

	// Verify multiple events can be created
	for i := 0; i < 5; i++ {
		event, err := ConvertToEvent(data, "/project")
		if err != nil {
			t.Fatalf("unexpected error on event %d: %v", i, err)
		}

		if event.ID == "" {
			t.Errorf("event %d has empty ID", i)
		}

		if event.Context.SessionID == "" {
			t.Errorf("event %d has empty SessionID", i)
		}
	}
}

func TestClaudeHookEvent_JSONUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected ClaudeHookEvent
	}{
		{
			name: "full event",
			json: `{"tool_name":"Edit","tool_input":{"file":"test.go"},"tool_result":"ok"}`,
			expected: ClaudeHookEvent{
				ToolName:   "Edit",
				ToolInput:  map[string]interface{}{"file": "test.go"},
				ToolResult: "ok",
			},
		},
		{
			name: "empty fields",
			json: `{"tool_name":"","tool_input":{},"tool_result":""}`,
			expected: ClaudeHookEvent{
				ToolName:   "",
				ToolInput:  map[string]interface{}{},
				ToolResult: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var event ClaudeHookEvent
			if err := json.Unmarshal([]byte(tt.json), &event); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if event.ToolName != tt.expected.ToolName {
				t.Errorf("expected ToolName %q, got %q", tt.expected.ToolName, event.ToolName)
			}

			if event.ToolResult != tt.expected.ToolResult {
				t.Errorf("expected ToolResult %q, got %q", tt.expected.ToolResult, event.ToolResult)
			}
		})
	}
}
