package llm

import "testing"

func TestParseJSONToolCalls(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantN    int
		wantName string
		wantArgs string
	}{
		{
			// The exact shape observed from Qwen3.6 via LiteLLM that
			// produced the 17/44 zero-read fixtures (eval-journal 2026-06-11).
			name:     "fenced json with object arguments",
			content:  "```json\n{\n    \"name\": \"read_file\",\n    \"arguments\": {\n        \"path\": \"src/storage.rs\"\n    }\n}\n```",
			wantN:    1,
			wantName: "read_file",
			wantArgs: `{
        "path": "src/storage.rs"
    }`,
		},
		{
			name:     "bare object as whole content",
			content:  `{"name": "list_dir", "arguments": {"path": "src"}}`,
			wantN:    1,
			wantName: "list_dir",
			wantArgs: `{"path": "src"}`,
		},
		{
			name:     "arguments as encoded string (wire echo)",
			content:  "```\n{\"name\": \"run_shell\", \"arguments\": \"{\\\"command\\\": \\\"ls\\\"}\"}\n```",
			wantN:    1,
			wantName: "run_shell",
			wantArgs: `{"command": "ls"}`,
		},
		{
			name:     "no arguments key",
			content:  "```json\n{\"name\": \"list_dir\"}\n```",
			wantN:    1,
			wantName: "list_dir",
			wantArgs: "{}",
		},
		{
			name:    "two fenced calls",
			content: "first:\n```json\n{\"name\": \"a\", \"arguments\": {}}\n```\nthen:\n```json\n{\"name\": \"b\", \"arguments\": {}}\n```",
			wantN:   2,
		},
		{
			name:    "extra keys mean not-a-tool-call",
			content: "```json\n{\"name\": \"prod\", \"version\": 2, \"arguments\": {}}\n```",
			wantN:   0,
		},
		{
			name:    "fenced code that is not json",
			content: "```go\nfunc main() {}\n```",
			wantN:   0,
		},
		{
			name:    "plain prose final answer",
			content: "The function you want is evictOldest at line 120.",
			wantN:   0,
		},
		{
			name:    "prose mentioning a name field without fences",
			content: `The config sets {"name": "demo", "port": 8080} at startup.`,
			wantN:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := ParseJSONToolCalls(tt.content)
			if len(calls) != tt.wantN {
				t.Fatalf("got %d calls, want %d: %+v", len(calls), tt.wantN, calls)
			}
			if tt.wantN >= 1 && tt.wantName != "" {
				if calls[0].Function.Name != tt.wantName {
					t.Errorf("name = %q, want %q", calls[0].Function.Name, tt.wantName)
				}
				if calls[0].Function.Arguments != tt.wantArgs {
					t.Errorf("args = %q, want %q", calls[0].Function.Arguments, tt.wantArgs)
				}
			}
		})
	}
}

// RecoverToolCalls prefers the XML dialect (more specific) and falls
// back to fenced JSON — one entry point for the harness loop.
func TestRecoverToolCalls(t *testing.T) {
	xml := "<function=read_file>\n<parameter=path>go.mod</parameter>\n</function>"
	if calls := RecoverToolCalls(xml); len(calls) != 1 || calls[0].Function.Name != "read_file" {
		t.Errorf("xml dialect not recovered: %+v", calls)
	}
	fenced := "```json\n{\"name\": \"list_dir\", \"arguments\": {\"path\": \".\"}}\n```"
	if calls := RecoverToolCalls(fenced); len(calls) != 1 || calls[0].Function.Name != "list_dir" {
		t.Errorf("json dialect not recovered: %+v", calls)
	}
	if calls := RecoverToolCalls("all done, the answer is 42"); len(calls) != 0 {
		t.Errorf("prose misread as tool call: %+v", calls)
	}
}
