package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tc builds a ToolCall with the given name and raw JSON-string arguments,
// matching the wire shape the model sends.
func tc(name, args string) ToolCall {
	return ToolCall{ID: "call_1", Type: "function", Function: FunctionCall{Name: name, Arguments: args}}
}

// TestToolCallArgumentsAreJSONString guards the headline bug: on the wire,
// function.arguments is a STRING whose contents are JSON, not a nested object.
// Regressing FunctionCall.Arguments back to a map breaks every tool call.
func TestToolCallArgumentsAreJSONString(t *testing.T) {
	raw := `{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}`
	var call ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		t.Fatalf("unmarshal tool call: %v", err)
	}
	got, err := call.stringArg("path")
	if err != nil {
		t.Fatalf("stringArg: %v", err)
	}
	if got != "go.mod" {
		t.Errorf("got %q, want %q", got, "go.mod")
	}
}

func TestStringArg(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		key     string
		want    string
		wantErr bool
	}{
		{"valid", `{"path":"go.mod"}`, "path", "go.mod", false},
		{"second key", `{"path":"a","content":"b"}`, "content", "b", false},
		{"missing key", `{"path":"a"}`, "content", "", true},
		{"non-string value", `{"path":123}`, "path", "", true},
		{"malformed json", `{"path":`, "path", "", true},
		{"empty args", ``, "path", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tc("x", tt.args).stringArg(tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hi there"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("reads existing file", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": path})
		got, err := tc(FunctionReadFile, string(args)).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hi there" {
			t.Errorf("got %q, want %q", got, "hi there")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "nope.txt")})
		if _, err := tc(FunctionReadFile, string(args)).Execute(); err == nil {
			t.Fatal("expected error reading missing file")
		}
	})
}

func TestWriteFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "written by cortex"})

	got, err := tc(FunctionWriteFile, string(args)).Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "wrote") {
		t.Errorf("expected a confirmation message, got %q", got)
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(onDisk) != "written by cortex" {
		t.Errorf("on disk = %q, want %q", onDisk, "written by cortex")
	}
}

func TestEditFileTool(t *testing.T) {
	edit := func(path, oldS, newS string) (string, error) {
		args, _ := json.Marshal(map[string]string{"path": path, "old_string": oldS, "new_string": newS})
		return tc(FunctionEditFile, string(args)).Execute()
	}

	t.Run("unique match is replaced", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.go")
		os.WriteFile(path, []byte("package main\n\nvar x = 1\n"), 0644)

		if _, err := edit(path, "var x = 1", "var x = 2"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _ := os.ReadFile(path)
		if want := "package main\n\nvar x = 2\n"; string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("not found errors and leaves file untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		os.WriteFile(path, []byte("hello"), 0644)

		if _, err := edit(path, "goodbye", "hi"); err == nil {
			t.Fatal("expected not-found error")
		}
		if got, _ := os.ReadFile(path); string(got) != "hello" {
			t.Errorf("file should be untouched, got %q", got)
		}
	})

	t.Run("ambiguous match is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		os.WriteFile(path, []byte("a a a"), 0644)

		_, err := edit(path, "a", "b")
		if err == nil {
			t.Fatal("expected ambiguity error")
		}
		if !strings.Contains(err.Error(), "3 times") {
			t.Errorf("error should report the match count, got %q", err)
		}
	})

	t.Run("empty old_string is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		os.WriteFile(path, []byte("x"), 0644)
		if _, err := edit(path, "", "y"); err == nil {
			t.Fatal("expected error for empty old_string")
		}
	})

	t.Run("preserves file mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "script.sh")
		os.WriteFile(path, []byte("echo old\n"), 0755)

		if _, err := edit(path, "old", "new"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0755 {
			t.Errorf("mode = %v, want 0755", info.Mode().Perm())
		}
	})
}

func TestBashTool(t *testing.T) {
	t.Run("allowlisted command runs", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "echo hello"})
		got, err := tc(FunctionBash, string(args)).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("got %q, want output containing 'hello'", got)
		}
	})

	t.Run("non-allowlisted command rejected", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "curl http://example.com"})
		_, err := tc(FunctionBash, string(args)).Execute()
		if err == nil {
			t.Fatal("expected allowlist rejection")
		}
		if !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("error %q should mention the allowlist", err)
		}
	})

	t.Run("empty command errors", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "   "})
		if _, err := tc(FunctionBash, string(args)).Execute(); err == nil {
			t.Fatal("expected error for empty command")
		}
	})
}

func TestExecuteUnknownTool(t *testing.T) {
	if _, err := tc("frobnicate", `{}`).Execute(); err == nil {
		t.Fatal("expected error for unknown tool name")
	}
}

// TestToolResultWireFormat locks the shape of a role:"tool" result message:
// it must carry tool_call_id and must NOT emit an empty tool_calls array.
func TestToolResultWireFormat(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleTool, ToolCallID: "call_42", Content: "result"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"tool_call_id":"call_42"`) {
		t.Errorf("missing tool_call_id: %s", s)
	}
	if strings.Contains(s, "tool_calls") {
		t.Errorf("tool result must omit tool_calls (omitempty): %s", s)
	}
}

func TestRequestSeedsSystemPromptAndTools(t *testing.T) {
	req := CortexArgs{"build something"}.Request()

	if len(req.Messages) == 0 {
		t.Fatal("expected at least the seeded system message")
	}
	if req.Messages[0].Role != RoleSystem {
		t.Errorf("messages[0] role = %q, want %q", req.Messages[0].Role, RoleSystem)
	}
	if req.Messages[0].Content != SystemPrompt {
		t.Error("messages[0] should be the system prompt")
	}
	if req.Temperature != 0 {
		t.Errorf("temperature = %v, want 0 for deterministic agent behavior", req.Temperature)
	}
	if len(req.Tools) == 0 {
		t.Error("expected tools attached to the request")
	}
}
