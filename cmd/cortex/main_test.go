package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestMain disables the self-contained local embedder for the whole package so
// EnableRetrieval-driven tests never trigger a background model download.
// Tests that want it can re-enable via t.Setenv. It also defaults CORTEX_HOME
// to a process-wide temp dir when unset: EnableMemory (session_runtime.go)
// wires a user-tier memory store at userhome.Path("memory") alongside the
// project store (docs/cross-source-learning.md piece 1), and many tests call
// EnableMemory without their own CORTEX_HOME override — without this default
// those tests would create/touch ~/.cortex on the real machine running them.
// Tests that need their own isolated (or shared-but-distinct) user home still
// set CORTEX_HOME explicitly via t.Setenv, which wins for the duration of
// that test.
func TestMain(m *testing.M) {
	homeDir := ""
	if os.Getenv("CORTEX_HOME") == "" {
		if dir, err := os.MkdirTemp("", "cortex-test-home-"); err == nil {
			_ = os.Setenv("CORTEX_HOME", dir)
			homeDir = dir
		}
	}
	code := m.Run()
	if homeDir != "" {
		_ = os.RemoveAll(homeDir)
	}
	os.Exit(code)
}

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
	got, err := call.StringArg("path")
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
			got, err := tc("x", tt.args).StringArg(tt.key)
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
		got, err := tools.Execute(context.Background(), tc(FunctionReadFile, string(args)), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hi there" {
			t.Errorf("got %q, want %q", got, "hi there")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "nope.txt")})
		if _, err := tools.Execute(context.Background(), tc(FunctionReadFile, string(args)), nil); err == nil {
			t.Fatal("expected error reading missing file")
		}
	})
}

func TestWriteFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "written by cortex"})

	got, err := tools.Execute(context.Background(), tc(FunctionWriteFile, string(args)), nil)
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
		return tools.Execute(context.Background(), tc(FunctionEditFile, string(args)), nil)
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
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("got %q, want output containing 'hello'", got)
		}
	})

	t.Run("risky command is gated when no approver is present", func(t *testing.T) {
		// Nil session → no classifier and no confirmRisky hook. A gray-zone
		// command fails closed to Risky and, with no interactive approver, is
		// blocked and reported back (as a result, not an error) so the model
		// can adapt.
		args, _ := json.Marshal(map[string]string{"command": "curl http://example.com"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
		if err != nil {
			t.Fatalf("gating should not error: %v", err)
		}
		low := strings.ToLower(got)
		if !strings.Contains(low, "block") && !strings.Contains(low, "risk") {
			t.Errorf("expected the command to be gated, got %q", got)
		}
	})

	t.Run("empty command errors", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "   "})
		if _, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil); err == nil {
			t.Fatal("expected error for empty command")
		}
	})

	t.Run("oversized output truncates when study unavailable", func(t *testing.T) {
		t.Chdir(t.TempDir())
		// head -c 20000 /dev/zero → 20KB, over maxToolOutput. With a nil
		// session the study path is unavailable; the old truncation
		// behavior must hold.
		args, _ := json.Marshal(map[string]string{"command": "head -c 20000 /dev/zero"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "[output truncated]") {
			t.Errorf("expected truncation sentinel in fallback path")
		}
		if len(got) > maxToolOutput+100 {
			t.Errorf("fallback output not bounded: %d bytes", len(got))
		}
	})
}

// TestDefaultStudyPasses, TestSpillShellOutput, TestConfinedPath, and
// TestConfinedPathSymlinkEscape now live in cmd/cortex/tools, next to the
// (unexported) helpers they cover.

func TestExecuteUnknownTool(t *testing.T) {
	if _, err := tools.Execute(context.Background(), tc("frobnicate", `{}`), nil); err == nil {
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
	t.Chdir(t.TempDir()) // hermetic: no AGENTS.md anywhere up the tree
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
	if req.Temperature != defaultTemperature {
		t.Errorf("temperature = %v, want default %v", req.Temperature, defaultTemperature)
	}
	if len(req.Tools) == 0 {
		t.Error("expected tools attached to the request")
	}
}

func TestProjectInstructionsInjection(t *testing.T) {
	t.Run("AGENTS.md is appended to the system prompt", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use table-driven tests.\n"), 0644)
		t.Chdir(dir)

		sys := CortexArgs{}.Request().Messages[0].Content
		if !strings.HasPrefix(sys, SystemPrompt) {
			t.Error("system message should start with the base prompt")
		}
		for _, want := range []string{"# Project instructions (AGENTS.md)", "Use table-driven tests."} {
			if !strings.Contains(sys, want) {
				t.Errorf("system message missing %q", want)
			}
		}
	})

	t.Run("found in a parent directory", func(t *testing.T) {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("from the root"), 0644)
		child := filepath.Join(root, "a", "b")
		os.MkdirAll(child, 0755)
		t.Chdir(child)

		if sys := (CortexArgs{}).Request().Messages[0].Content; !strings.Contains(sys, "from the root") {
			t.Error("AGENTS.md in an ancestor directory should be found")
		}
	})

	t.Run("oversized file is truncated", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(strings.Repeat("x", maxInstructionBytes+100)), 0644)
		t.Chdir(dir)

		sys := CortexArgs{}.Request().Messages[0].Content
		if !strings.Contains(sys, "[AGENTS.md truncated]") {
			t.Error("oversized AGENTS.md should be marked truncated")
		}
		if len(sys) > len(SystemPrompt)+maxInstructionBytes+200 {
			t.Errorf("system message is %d bytes; the cap did not hold", len(sys))
		}
	})
}

func TestReadFileSizeGuard(t *testing.T) {
	dir := t.TempDir()
	// The curation budget is fixed (curationBudgetTokens), independent of the
	// session window — so a big-window coder still curates large files.
	cs := &CortexSession{Window: 131072}

	t.Run("oversized non-Go file returns a structural skeleton, not an error", func(t *testing.T) {
		// outline.Render's regex/prose/positional tiers cover any language, not
		// just go/ast — a too-large file of ANY kind gets curated, never a
		// dead-end error redirecting the model to study as the only option.
		big := filepath.Join(dir, "big.txt")
		size := (curationBudgetTokens + 1000) * 4
		os.WriteFile(big, make([]byte, size), 0644) // over the budget
		args, _ := json.Marshal(map[string]string{"path": big})
		out, err := tools.Execute(context.Background(), tc(FunctionReadFile, string(args)), cs)
		if err != nil {
			t.Fatalf("oversized non-Go file should get a skeleton, not an error: %v", err)
		}
		if !strings.Contains(out, "too large") || !strings.Contains(out, "study") {
			t.Errorf("skeleton should explain how to get content; got head: %.160q", out)
		}
		if len(out) >= size {
			t.Error("skeleton path leaked raw file content")
		}
	})

	t.Run("oversized Go file returns its declaration skeleton, not an error", func(t *testing.T) {
		bigGo := filepath.Join(dir, "big.go")
		// Real decls plus a giant comment to push the file over the budget while
		// staying parseable — the content must never be dumped, only the map.
		src := "package p\n\ntype Marker struct{}\n\nfunc Sentinel() {}\n\n// " +
			strings.Repeat("x", (curationBudgetTokens+1000)*4) + "\n"
		os.WriteFile(bigGo, []byte(src), 0644)
		args, _ := json.Marshal(map[string]string{"path": bigGo})
		out, err := tools.Execute(context.Background(), tc(FunctionReadFile, string(args)), cs)
		if err != nil {
			t.Fatalf("Go skeleton path should not error: %v", err)
		}
		if !strings.Contains(out, "Marker") || !strings.Contains(out, "Sentinel") {
			t.Errorf("skeleton missing symbols; got head: %.160q", out)
		}
		if !strings.Contains(out, "too large") || !strings.Contains(out, "study") {
			t.Errorf("skeleton should explain how to get content; got head: %.160q", out)
		}
		if strings.Contains(out, strings.Repeat("x", 500)) {
			t.Error("skeleton path leaked raw file content")
		}
	})

	t.Run("ordinary source file under the budget still reads whole", func(t *testing.T) {
		small := filepath.Join(dir, "small.go")
		os.WriteFile(small, make([]byte, 8000), 0644) // ~2k tokens, well under the budget
		args, _ := json.Marshal(map[string]string{"path": small})
		if _, err := tools.Execute(context.Background(), tc(FunctionReadFile, string(args)), cs); err != nil {
			t.Fatalf("under-budget read should succeed: %v", err)
		}
	})

	t.Run("budget is fixed, not window-scaled", func(t *testing.T) {
		// A file over the budget is curated (skeleton, never a raw dump) even
		// with a huge window (the bug this fixes: a big window used to push the
		// threshold past the file size).
		big := filepath.Join(dir, "big2.txt")
		size := (curationBudgetTokens + 1000) * 4
		os.WriteFile(big, make([]byte, size), 0644)
		args, _ := json.Marshal(map[string]string{"path": big})
		out, err := tools.Execute(context.Background(), tc(FunctionReadFile, string(args)), &CortexSession{Window: 1_000_000})
		if err != nil {
			t.Fatalf("a huge window must not turn curation into an error path: %v", err)
		}
		if len(out) >= size {
			t.Error("a huge window must not exempt a large file from curation (raw content leaked)")
		}
	})
}

func TestParseXMLToolCalls(t *testing.T) {
	t.Run("wrapped single call with a pipe", func(t *testing.T) {
		content := "<tool_call>\n<function=bash>\n<parameter=command>\nls -la | grep cortex\n</parameter>\n</function>\n</tool_call>"
		calls := parseXMLToolCalls(content)
		if len(calls) != 1 {
			t.Fatalf("got %d calls, want 1", len(calls))
		}
		if calls[0].Function.Name != "bash" {
			t.Errorf("name = %q, want bash", calls[0].Function.Name)
		}
		got, err := calls[0].StringArg("command")
		if err != nil {
			t.Fatal(err)
		}
		if got != "ls -la | grep cortex" {
			t.Errorf("command = %q", got)
		}
	})

	t.Run("unwrapped (no tool_call tag)", func(t *testing.T) {
		content := "<function=read_file>\n<parameter=path>\ngo.mod\n</parameter>\n</function>"
		calls := parseXMLToolCalls(content)
		if len(calls) != 1 || calls[0].Function.Name != "read_file" {
			t.Fatalf("got %+v", calls)
		}
		if p, _ := calls[0].StringArg("path"); p != "go.mod" {
			t.Errorf("path = %q", p)
		}
	})

	t.Run("multiple params", func(t *testing.T) {
		content := "<function=write_file>\n<parameter=path>\nout.txt\n</parameter>\n<parameter=content>\nhello world\n</parameter>\n</function>"
		calls := parseXMLToolCalls(content)
		if len(calls) != 1 {
			t.Fatalf("got %d", len(calls))
		}
		path, _ := calls[0].StringArg("path")
		body, _ := calls[0].StringArg("content")
		if path != "out.txt" || body != "hello world" {
			t.Errorf("path=%q content=%q", path, body)
		}
	})

	t.Run("multiple function blocks get unique ids", func(t *testing.T) {
		content := "<function=read_file><parameter=path>a</parameter></function>" +
			"<function=read_file><parameter=path>b</parameter></function>"
		calls := parseXMLToolCalls(content)
		if len(calls) != 2 {
			t.Fatalf("got %d, want 2", len(calls))
		}
		if calls[0].ID == calls[1].ID {
			t.Errorf("synthesized IDs must be unique, both %q", calls[0].ID)
		}
	})

	t.Run("no xml returns nil", func(t *testing.T) {
		if calls := parseXMLToolCalls("a normal answer, nothing to call"); calls != nil {
			t.Errorf("expected nil, got %+v", calls)
		}
	})

	t.Run("parsed call executes through the normal path", func(t *testing.T) {
		content := "<function=bash>\n<parameter=command>\necho hi\n</parameter>\n</function>"
		calls := parseXMLToolCalls(content)
		out, err := tools.Execute(context.Background(), calls[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "hi") {
			t.Errorf("got %q", out)
		}
	})
}

// toolResults filters a turn's messages down to the role:"tool" entries.
func toolResults(msgs []Message) []Message {
	var out []Message
	for _, m := range msgs {
		if m.Role == RoleTool {
			out = append(out, m)
		}
	}
	return out
}

// The coder dispatch path (engine + coderDispatcher) must append one tool result
// per call ID even when the turn was interrupted — a missing result for a
// tool_call id breaks the next send.
func TestCoderDispatchInterruptedAppendsAllResults(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	before := len(cs.Request.Messages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already interrupted

	calls := []ToolCall{
		tc(FunctionBash, `{"command":"echo one"}`),
		{ID: "call_2", Type: "function", Function: FunctionCall{Name: FunctionBash, Arguments: `{"command":"echo two"}`}},
	}
	send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		return fakeResp("", calls, 1, 1), false, nil
	})
	ts := Toolset{Tools: cs.Request.Tools, Dispatch: cs.coderDispatcher()}
	_, _, err := runLoop(ctx, send, cs.Request, ts, Bounds{MaxTokens: 100, MaxIter: 100}, nil, cs.Append, nil)
	if err == nil {
		t.Fatal("expected a canceled-context error")
	}

	got := toolResults(cs.Request.Messages[before:])
	if len(got) != 2 {
		t.Fatalf("appended %d tool results, want 2 (one per call)", len(got))
	}
	for i, m := range got {
		if m.ToolCallID != calls[i].ID {
			t.Errorf("result %d tool_call_id = %q, want %q", i, m.ToolCallID, calls[i].ID)
		}
		if !strings.Contains(m.Content, "interrupted") {
			t.Errorf("result %d should record the interrupt, got %q", i, m.Content)
		}
	}
}

func TestCoderDispatchHappyPath(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	before := len(cs.Request.Messages)

	var round int
	send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		defer func() { round++ }()
		if round == 0 {
			return fakeResp("", []ToolCall{tc(FunctionBash, `{"command":"echo hello"}`)}, 1, 1), false, nil
		}
		return fakeResp("done", nil, 1, 1), false, nil
	})
	ts := Toolset{Tools: cs.Request.Tools, Dispatch: cs.coderDispatcher()}
	if _, _, err := runLoop(context.Background(), send, cs.Request, ts, Bounds{MaxTokens: 100, MaxIter: 100}, nil, cs.Append, nil); err != nil {
		t.Fatalf("runLoop: %v", err)
	}

	got := toolResults(cs.Request.Messages[before:])
	if len(got) != 1 {
		t.Fatalf("appended %d tool results, want 1", len(got))
	}
	if !strings.Contains(got[0].Content, "hello") {
		t.Errorf("tool result = %q, want echo output", got[0].Content)
	}
}
