package cursor

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/events"
)

func TestMapLSPMethodToToolName(t *testing.T) {
	tests := []struct {
		method   string
		expected string
	}{
		{"textDocument/didChange", "Edit"},
		{"textDocument/didSave", "Write"},
		{"textDocument/didOpen", "Read"},
		{"textDocument/didClose", "Close"},
		{"workspace/executeCommand", "Command"},
		{"$/ai/completion", "AICompletion"},
		{"$/ai/chat", "AIChat"},
		{"$/cursor/applyEdit", "CursorEdit"},
		{"$/cursor/accept", "CursorAccept"},
		{"$/cursor/reject", "CursorReject"},
		{"unknown/method", ""},
		{"", ""},
		{"textDocument/hover", ""},
		{"workspace/didChangeConfiguration", ""},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			result := mapLSPMethodToToolName(tt.method)
			if result != tt.expected {
				t.Errorf("mapLSPMethodToToolName(%q) = %q, want %q", tt.method, result, tt.expected)
			}
		})
	}
}

func TestExtractToolInput(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]interface{}
		validate func(*testing.T, map[string]interface{})
	}{
		{
			name: "textDocument with URI",
			params: map[string]interface{}{
				"textDocument": map[string]interface{}{
					"uri": "file:///path/to/file.go",
				},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if input["file_path"] != "file:///path/to/file.go" {
					t.Errorf("expected file_path 'file:///path/to/file.go', got %v", input["file_path"])
				}
			},
		},
		{
			name: "contentChanges array with short text",
			params: map[string]interface{}{
				"contentChanges": []interface{}{
					map[string]interface{}{"text": "first change"},
					map[string]interface{}{"text": "second change"},
				},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				// Should get the last change
				if input["change_preview"] != "second change" {
					t.Errorf("expected change_preview 'second change', got %v", input["change_preview"])
				}
			},
		},
		{
			name: "contentChanges with long text truncated",
			params: map[string]interface{}{
				"contentChanges": []interface{}{
					map[string]interface{}{"text": strings.Repeat("x", 250)},
				},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				preview := input["change_preview"].(string)
				if len(preview) != 203 { // 200 + "..."
					t.Errorf("expected change_preview length 203, got %d", len(preview))
				}
				if !strings.HasSuffix(preview, "...") {
					t.Error("expected change_preview to end with '...'")
				}
			},
		},
		{
			name: "command field",
			params: map[string]interface{}{
				"command": "format.document",
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if input["command"] != "format.document" {
					t.Errorf("expected command 'format.document', got %v", input["command"])
				}
			},
		},
		{
			name: "arguments field",
			params: map[string]interface{}{
				"arguments": []interface{}{"arg1", "arg2"},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				args := input["arguments"].([]interface{})
				if len(args) != 2 {
					t.Errorf("expected 2 arguments, got %d", len(args))
				}
			},
		},
		{
			name: "prompt field",
			params: map[string]interface{}{
				"prompt": "help me fix this",
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if input["prompt"] != "help me fix this" {
					t.Errorf("expected prompt 'help me fix this', got %v", input["prompt"])
				}
			},
		},
		{
			name:   "empty params",
			params: map[string]interface{}{},
			validate: func(t *testing.T, input map[string]interface{}) {
				if len(input) != 0 {
					t.Errorf("expected empty input, got %v", input)
				}
			},
		},
		{
			name: "multiple fields combined",
			params: map[string]interface{}{
				"textDocument": map[string]interface{}{"uri": "file:///test.go"},
				"command":      "test",
				"prompt":       "help",
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if input["file_path"] != "file:///test.go" {
					t.Errorf("expected file_path, got %v", input["file_path"])
				}
				if input["command"] != "test" {
					t.Errorf("expected command 'test', got %v", input["command"])
				}
				if input["prompt"] != "help" {
					t.Errorf("expected prompt 'help', got %v", input["prompt"])
				}
			},
		},
		{
			name: "textDocument not a map",
			params: map[string]interface{}{
				"textDocument": "not a map",
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if _, exists := input["file_path"]; exists {
					t.Error("expected file_path to not be set when textDocument is invalid")
				}
			},
		},
		{
			name: "contentChanges empty array",
			params: map[string]interface{}{
				"contentChanges": []interface{}{},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if _, exists := input["change_preview"]; exists {
					t.Error("expected change_preview to not be set for empty contentChanges")
				}
			},
		},
		{
			name: "arguments empty array not included",
			params: map[string]interface{}{
				"arguments": []interface{}{},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if _, exists := input["arguments"]; exists {
					t.Error("expected arguments to not be set for empty array")
				}
			},
		},
		{
			name: "contentChanges with non-map element",
			params: map[string]interface{}{
				"contentChanges": []interface{}{"not a map"},
			},
			validate: func(t *testing.T, input map[string]interface{}) {
				if _, exists := input["change_preview"]; exists {
					t.Error("expected change_preview to not be set for invalid contentChanges")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolInput(tt.params)
			tt.validate(t, result)
		})
	}
}

func TestExtractToolResult(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]interface{}
		expected string
	}{
		{
			name: "textDocument present",
			params: map[string]interface{}{
				"textDocument": map[string]interface{}{
					"uri": "file:///path/to/file.go",
				},
			},
			expected: "Modified: file:///path/to/file.go",
		},
		{
			name: "command present without textDocument",
			params: map[string]interface{}{
				"command": "format.document",
			},
			expected: "Executed: format.document",
		},
		{
			name: "result field present",
			params: map[string]interface{}{
				"result": "custom result message",
			},
			expected: "custom result message",
		},
		{
			name:     "no special fields",
			params:   map[string]interface{}{},
			expected: "success",
		},
		{
			name: "textDocument takes precedence over command",
			params: map[string]interface{}{
				"textDocument": map[string]interface{}{"uri": "file:///a.go"},
				"command":      "should not appear",
			},
			expected: "Modified: file:///a.go",
		},
		{
			name: "command takes precedence over result",
			params: map[string]interface{}{
				"command": "cmd",
				"result":  "should not appear",
			},
			expected: "Executed: cmd",
		},
		{
			name: "textDocument without uri",
			params: map[string]interface{}{
				"textDocument": map[string]interface{}{"version": 1},
				"command":      "fallback",
			},
			expected: "Executed: fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolResult(tt.params)
			if result != tt.expected {
				t.Errorf("extractToolResult() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestConvertLSPToEvent(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		projectPath string
		wantErr     bool
		errContains string
		validate    func(*testing.T, *events.Event)
	}{
		{
			name: "valid LSP notification",
			data: []byte(`{
				"method": "textDocument/didChange",
				"params": {
					"textDocument": {"uri": "file:///test.go"},
					"contentChanges": [{"text": "new content"}]
				}
			}`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "Edit" {
					t.Errorf("expected ToolName 'Edit', got %q", e.ToolName)
				}
				if e.Source != events.SourceCursor {
					t.Errorf("expected Source SourceCursor, got %v", e.Source)
				}
				if e.ToolInput["file_path"] != "file:///test.go" {
					t.Errorf("expected file_path, got %v", e.ToolInput["file_path"])
				}
			},
		},
		{
			name: "unknown LSP method",
			data: []byte(`{
				"method": "unknown/method",
				"params": {}
			}`),
			projectPath: "/project",
			wantErr:     true,
			errContains: "unsupported LSP method",
		},
		{
			name:        "invalid JSON",
			data:        []byte(`{invalid`),
			projectPath: "/project",
			wantErr:     true,
			errContains: "failed to parse LSP notification",
		},
		{
			name: "AI completion event",
			data: []byte(`{
				"method": "$/ai/completion",
				"params": {"prompt": "help me"}
			}`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "AICompletion" {
					t.Errorf("expected ToolName 'AICompletion', got %q", e.ToolName)
				}
				if e.ToolInput["prompt"] != "help me" {
					t.Errorf("expected prompt, got %v", e.ToolInput["prompt"])
				}
			},
		},
		{
			name: "workspace command",
			data: []byte(`{
				"method": "workspace/executeCommand",
				"params": {"command": "editor.action.formatDocument"}
			}`),
			projectPath: "/project",
			wantErr:     false,
			validate: func(t *testing.T, e *events.Event) {
				if e.ToolName != "Command" {
					t.Errorf("expected ToolName 'Command', got %q", e.ToolName)
				}
				if !strings.Contains(e.ToolResult, "Executed") {
					t.Errorf("expected ToolResult to contain 'Executed', got %q", e.ToolResult)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ConvertLSPToEvent(tt.data, tt.projectPath)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if event == nil {
				t.Fatal("expected non-nil event")
			}

			// Common validations
			if event.EventType != events.EventToolUse {
				t.Errorf("expected EventType EventToolUse, got %v", event.EventType)
			}

			if event.ID == "" {
				t.Error("expected non-empty ID")
			}

			if !strings.HasPrefix(event.ID, "cursor-") {
				t.Errorf("expected ID to start with 'cursor-', got %q", event.ID)
			}

			if event.Context.ProjectPath != tt.projectPath {
				t.Errorf("expected ProjectPath %q, got %q", tt.projectPath, event.Context.ProjectPath)
			}

			if event.Context.SessionID == "" {
				t.Error("expected non-empty SessionID")
			}

			if tt.validate != nil {
				tt.validate(t, event)
			}
		})
	}
}

func TestConvertToEvent_Alias(t *testing.T) {
	data := []byte(`{
		"method": "textDocument/didSave",
		"params": {"textDocument": {"uri": "file:///test.go"}}
	}`)

	event1, err1 := ConvertLSPToEvent(data, "/project")
	if err1 != nil {
		t.Fatalf("ConvertLSPToEvent failed: %v", err1)
	}

	event2, err2 := ConvertToEvent(data, "/project")
	if err2 != nil {
		t.Fatalf("ConvertToEvent failed: %v", err2)
	}

	// Both should produce similar events (IDs will differ due to timing)
	if event1.ToolName != event2.ToolName {
		t.Errorf("ToolName mismatch: %q vs %q", event1.ToolName, event2.ToolName)
	}

	if event1.Source != event2.Source {
		t.Errorf("Source mismatch: %v vs %v", event1.Source, event2.Source)
	}
}

func TestLSPNotification_JSONStructure(t *testing.T) {
	notification := LSPNotification{
		Method: "textDocument/didChange",
		Params: map[string]interface{}{
			"textDocument": map[string]interface{}{"uri": "file:///test.go"},
		},
	}

	if notification.Method != "textDocument/didChange" {
		t.Errorf("expected Method 'textDocument/didChange', got %q", notification.Method)
	}

	if notification.Params == nil {
		t.Error("expected non-nil Params")
	}
}

func TestCursorEvent_Structure(t *testing.T) {
	event := CursorEvent{
		Method: "$/ai/chat",
		Params: map[string]interface{}{"prompt": "hello"},
	}

	if event.Method != "$/ai/chat" {
		t.Errorf("expected Method '$/ai/chat', got %q", event.Method)
	}
}
