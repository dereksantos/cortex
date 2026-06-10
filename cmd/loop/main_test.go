package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/study"
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
		got, err := tc(FunctionReadFile, string(args)).Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hi there" {
			t.Errorf("got %q, want %q", got, "hi there")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "nope.txt")})
		if _, err := tc(FunctionReadFile, string(args)).Execute(context.Background(), nil); err == nil {
			t.Fatal("expected error reading missing file")
		}
	})
}

func TestWriteFileTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "written by cortex"})

	got, err := tc(FunctionWriteFile, string(args)).Execute(context.Background(), nil)
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
		return tc(FunctionEditFile, string(args)).Execute(context.Background(), nil)
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
		got, err := tc(FunctionBash, string(args)).Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("got %q, want output containing 'hello'", got)
		}
	})

	t.Run("non-allowlisted command rejected", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "curl http://example.com"})
		_, err := tc(FunctionBash, string(args)).Execute(context.Background(), nil)
		if err == nil {
			t.Fatal("expected allowlist rejection")
		}
		if !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("error %q should mention the allowlist", err)
		}
	})

	t.Run("empty command errors", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"command": "   "})
		if _, err := tc(FunctionBash, string(args)).Execute(context.Background(), nil); err == nil {
			t.Fatal("expected error for empty command")
		}
	})
}

func TestExecuteUnknownTool(t *testing.T) {
	if _, err := tc("frobnicate", `{}`).Execute(context.Background(), nil); err == nil {
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
	if req.Temperature != 0 {
		t.Errorf("temperature = %v, want 0 for deterministic agent behavior", req.Temperature)
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

func TestHumanK(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{8200, "8.2k"},
		{65536, "65.5k"},
	}
	for _, tt := range tests {
		if got := humanK(tt.in); got != tt.want {
			t.Errorf("humanK(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCtxColor(t *testing.T) {
	win := defaultModels[roleCode].Window
	tests := []struct {
		used int
		want string
	}{
		{0, green},
		{win / 4, green},       // 25%
		{win * 6 / 10, yellow}, // 60%
		{win * 9 / 10, red},    // 90%
		{win, red},             // full
	}
	for _, tt := range tests {
		if got := ctxColor(tt.used, win); got != tt.want {
			t.Errorf("ctxColor(%d/%d) = %q, want %q", tt.used, win, got, tt.want)
		}
	}
}

func TestConfigSpec(t *testing.T) {
	t.Run("nil config returns built-in defaults", func(t *testing.T) {
		var c *Config
		if code := c.Spec(roleCode); code.Model != "coder" || code.Endpoint == "" || code.Window == 0 {
			t.Errorf("code default = %+v", code)
		}
		if study := c.Spec(roleStudy); study.Model != "reasoner" {
			t.Errorf("study default model = %q, want reasoner", study.Model)
		}
	})

	t.Run("config overrides layer per-field on the default", func(t *testing.T) {
		c := &Config{Models: map[string]ModelSpec{
			roleStudy: {Model: "custom-study"}, // only the model; endpoint/window inherit
		}}
		s := c.Spec(roleStudy)
		if s.Model != "custom-study" {
			t.Errorf("model = %q, want custom-study", s.Model)
		}
		if s.Endpoint != defaultModels[roleStudy].Endpoint || s.Window != defaultModels[roleStudy].Window {
			t.Errorf("endpoint/window should inherit the default, got %+v", s)
		}
	})
}

// windowSize falls back to the default when Window is unset, so the gauge never
// divides by zero or shows /0.
func TestWindowSizeFallback(t *testing.T) {
	if got := (CortexSession{}).windowSize(); got != defaultModels[roleCode].Window {
		t.Errorf("windowSize() = %d, want default %d", got, defaultModels[roleCode].Window)
	}
	if got := (CortexSession{Window: 8192}).windowSize(); got != 8192 {
		t.Errorf("windowSize() = %d, want 8192", got)
	}
}

func TestSessionPrompt(t *testing.T) {
	sess := &CortexSession{Request: CortexArgs{}.Request(), LastPromptTokens: 8200}
	got := sess.Prompt()

	for _, want := range []string{"cortex " + Version, ModelCoder, "8.2k/65.5k", promptGlyph} {
		if !strings.Contains(got, want) {
			t.Errorf("Prompt() = %q, missing %q", got, want)
		}
	}
}

func TestSetModel(t *testing.T) {
	s := &CortexSession{Request: &AgentRequest{Model: "coder", BaseURL: "http://chatterbox:4000"}}
	s.SetModel("reasoner")
	if s.Request.Model != "reasoner" {
		t.Errorf("model = %q, want reasoner", s.Request.Model)
	}
	if s.Request.BaseURL != "http://chatterbox:4000" {
		t.Errorf("endpoint should be unchanged on a model swap, got %q", s.Request.BaseURL)
	}
}

func TestReadFileSizeGuard(t *testing.T) {
	dir := t.TempDir()
	cs := &CortexSession{Window: 1000} // threshold = 500 tokens ≈ 2000 bytes

	t.Run("oversized read is refused and redirects to study", func(t *testing.T) {
		big := filepath.Join(dir, "big.txt")
		os.WriteFile(big, make([]byte, 4000), 0644) // ~1000 tokens > 500
		args, _ := json.Marshal(map[string]string{"path": big})
		_, err := tc(FunctionReadFile, string(args)).Execute(context.Background(), cs)
		if err == nil {
			t.Fatal("expected size-guard error")
		}
		if !strings.Contains(err.Error(), "study") {
			t.Errorf("guard should redirect to study, got %q", err)
		}
	})

	t.Run("small read under the threshold succeeds", func(t *testing.T) {
		small := filepath.Join(dir, "small.txt")
		os.WriteFile(small, []byte("hi there"), 0644)
		args, _ := json.Marshal(map[string]string{"path": small})
		if _, err := tc(FunctionReadFile, string(args)).Execute(context.Background(), cs); err != nil {
			t.Fatalf("small read should succeed: %v", err)
		}
	})
}

func TestScroller(t *testing.T) {
	valid := map[rune]bool{}
	for _, r := range heights {
		valid[r] = true
	}
	for _, r := range flecks {
		valid[r] = true
	}

	t.Run("each frame is spinnerWidth runes from the palette", func(t *testing.T) {
		s := newScroller(1)
		for n := 0; n < 500; n++ {
			f := []rune(s.frame())
			if len(f) != spinnerWidth {
				t.Fatalf("frame %d = %q has %d runes, want %d", n, string(f), len(f), spinnerWidth)
			}
			for _, r := range f {
				if !valid[r] {
					t.Errorf("frame %d has off-palette glyph %q", n, string(r))
				}
			}
		}
	})

	t.Run("scrolls left: each frame shifts in exactly one new column", func(t *testing.T) {
		s := newScroller(7)
		prev := []rune(s.frame())
		for n := 0; n < 100; n++ {
			cur := []rune(s.frame())
			for k := 0; k < spinnerWidth-1; k++ {
				if cur[k] != prev[k+1] {
					t.Fatalf("frame %d not a left-shift: prev=%q cur=%q", n, string(prev), string(cur))
				}
			}
			prev = cur
		}
	})

	t.Run("same seed is deterministic", func(t *testing.T) {
		a, b := newScroller(42), newScroller(42)
		for n := 0; n < 50; n++ {
			if a.frame() != b.frame() {
				t.Fatalf("seeded scrollers diverged at frame %d", n)
			}
		}
	})

	t.Run("different seeds diverge", func(t *testing.T) {
		a, b := newScroller(1), newScroller(2)
		same := true
		for n := 0; n < 50; n++ {
			if a.frame() != b.frame() {
				same = false
				break
			}
		}
		if same {
			t.Error("different seeds produced identical sequences")
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
		got, err := calls[0].stringArg("command")
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
		if p, _ := calls[0].stringArg("path"); p != "go.mod" {
			t.Errorf("path = %q", p)
		}
	})

	t.Run("multiple params", func(t *testing.T) {
		content := "<function=write_file>\n<parameter=path>\nout.txt\n</parameter>\n<parameter=content>\nhello world\n</parameter>\n</function>"
		calls := parseXMLToolCalls(content)
		if len(calls) != 1 {
			t.Fatalf("got %d", len(calls))
		}
		path, _ := calls[0].stringArg("path")
		body, _ := calls[0].stringArg("content")
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
		out, err := calls[0].Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "hi") {
			t.Errorf("got %q", out)
		}
	})
}

func TestStripToolMarkup(t *testing.T) {
	content := "Let me check.\n<tool_call>\n<function=bash>\n<parameter=command>\nls\n</parameter>\n</function>\n</tool_call>"
	got := stripToolMarkup(content)
	if got != "Let me check." {
		t.Errorf("stripToolMarkup = %q, want %q", got, "Let me check.")
	}
}

func TestMessageRender(t *testing.T) {
	ts := time.Date(2026, 6, 8, 14, 23, 1, 0, time.UTC)
	tests := []struct {
		role string
		icon string
	}{
		{"assistant", iconCortex},
		{RoleSystem, iconCortex}, // default branch
		{RoleTool, iconTool},
		{RoleUser, iconUser},
	}
	for _, tt := range tests {
		m := Message{Role: tt.role, Content: "hello"}
		got := m.render(ts)
		for _, want := range []string{tt.icon, "14:23:01", "hello"} {
			if !strings.Contains(got, want) {
				t.Errorf("render(role=%s) = %q, missing %q", tt.role, got, want)
			}
		}
	}
}

func TestRenderStudyResult(t *testing.T) {
	t.Run("read mode returns the whole file verbatim", func(t *testing.T) {
		res := study.StudyLoopResult{
			Stopped: "read",
			Passes:  []study.StudyPass{{Response: study.StudyResponse{Mode: "read", ReadContent: "package main\n\nfunc main() {}\n"}}},
		}
		if got := renderStudyResult(res); got != "package main\n\nfunc main() {}\n" {
			t.Errorf("read mode = %q, want the whole content", got)
		}
	})

	t.Run("study mode renders digests and cited line ranges", func(t *testing.T) {
		res := study.StudyLoopResult{
			Stopped:     "done",
			CoveragePct: 0.42,
			Digests:     []string{"the study command registers subcommands", ""},
			Citations:   []study.Citation{{RelPath: "study.go", LineStart: 10, LineEnd: 20, Claim: "registers the study command"}},
		}
		got := renderStudyResult(res)
		for _, want := range []string{"42%", "done", "the study command registers", "study.go:10-20", "registers the study command"} {
			if !strings.Contains(got, want) {
				t.Errorf("render missing %q in:\n%s", want, got)
			}
		}
	})
}

func TestParseCtxSize(t *testing.T) {
	msg := "litellm.BadRequestError: request (41193 tokens) exceeds the available context size (32768 tokens)"
	if got := parseCtxSize(msg); got != 32768 {
		t.Errorf("parseCtxSize = %d, want 32768", got)
	}
	if got := parseCtxSize("no numbers here"); got != 0 {
		t.Errorf("parseCtxSize(no match) = %d, want 0", got)
	}
}

func TestSampleBudget(t *testing.T) {
	// headroom = window/4 (min 2048); budget = window - headroom
	for _, tt := range []struct{ window, want int }{
		{32768, 24576},   // 32768 - 8192
		{262144, 196608}, // 262144 - 65536
		{4096, 2048},     // headroom floored at 2048
	} {
		if got := sampleBudget(tt.window); got != tt.want {
			t.Errorf("sampleBudget(%d) = %d, want %d", tt.window, got, tt.want)
		}
	}
}

// quickRetries shrinks the retry backoff for the duration of a test.
func quickRetries(t *testing.T) {
	t.Helper()
	saved := retryBackoff
	retryBackoff = time.Millisecond
	t.Cleanup(func() { retryBackoff = saved })
}

const okResponse = `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1}}`

func TestSendRetriesTransientErrors(t *testing.T) {
	quickRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(okResponse))
	}))
	defer srv.Close()

	req := &AgentRequest{Model: "m", BaseURL: srv.URL}
	res, err := req.Send(context.Background())
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("server saw %d calls, want 3 (two 503s then success)", calls)
	}
	if res.Choices[0].Message.Content != "ok" {
		t.Errorf("unexpected response content %q", res.Choices[0].Message.Content)
	}
}

func TestSendGivesUpAfterMaxAttempts(t *testing.T) {
	quickRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != maxSendAttempts {
		t.Errorf("server saw %d calls, want %d", calls, maxSendAttempts)
	}
}

// A 4xx means the request itself is wrong (e.g. context overflow) — retrying
// can't fix it and would just burn time, so exactly one attempt is made.
func TestSendDoesNotRetryClientErrors(t *testing.T) {
	quickRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("context size (32768 tokens)"))
	}))
	defer srv.Close()

	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if calls != 1 {
		t.Errorf("server saw %d calls, want 1 (no retry on 4xx)", calls)
	}
	// The error must preserve the provider's message — study's window
	// self-calibration parses it.
	if !strings.Contains(err.Error(), "context size (32768 tokens)") {
		t.Errorf("error should carry the response body, got %q", err)
	}
}

func TestSendHonorsContextCancel(t *testing.T) {
	quickRetries(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block // hold the request open until the test ends
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(ctx)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Send took %v after cancel; should return promptly", elapsed)
	}
}

// runToolCalls must append one tool result per call ID even when the turn was
// interrupted — a missing result for a tool_call id breaks the next send.
func TestRunToolCallsInterruptedAppendsAllResults(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	before := len(cs.Request.Messages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already interrupted

	calls := []ToolCall{
		tc(FunctionBash, `{"command":"echo one"}`),
		{ID: "call_2", Type: "function", Function: FunctionCall{Name: FunctionBash, Arguments: `{"command":"echo two"}`}},
	}
	cs.runToolCalls(ctx, calls)

	got := cs.Request.Messages[before:]
	if len(got) != 2 {
		t.Fatalf("appended %d messages, want 2 (one per call)", len(got))
	}
	for i, m := range got {
		if m.Role != RoleTool {
			t.Errorf("message %d role = %q, want %q", i, m.Role, RoleTool)
		}
		if m.ToolCallID != calls[i].ID {
			t.Errorf("message %d tool_call_id = %q, want %q", i, m.ToolCallID, calls[i].ID)
		}
		if !strings.Contains(m.Content, "interrupted") {
			t.Errorf("message %d should record the interrupt, got %q", i, m.Content)
		}
	}
}

func TestRunToolCallsHappyPath(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	before := len(cs.Request.Messages)

	cs.runToolCalls(context.Background(), []ToolCall{tc(FunctionBash, `{"command":"echo hello"}`)})

	got := cs.Request.Messages[before:]
	if len(got) != 1 {
		t.Fatalf("appended %d messages, want 1", len(got))
	}
	if !strings.Contains(got[0].Content, "hello") {
		t.Errorf("tool result = %q, want echo output", got[0].Content)
	}
}

func TestStudyWindowResolution(t *testing.T) {
	defer func() { delete(learnedWindows, "m") }()
	cs := &CortexSession{Study: ModelSpec{Model: "m", Window: 32768}}
	if got := cs.studyWindow(); got != 32768 {
		t.Errorf("configured window = %d, want 32768", got)
	}
	learnedWindows["m"] = 16000 // learned beats configured
	if got := cs.studyWindow(); got != 16000 {
		t.Errorf("learned window = %d, want 16000", got)
	}
	empty := &CortexSession{Study: ModelSpec{Model: "x"}}
	if got := empty.studyWindow(); got != studyFallbackWindow {
		t.Errorf("fallback window = %d, want %d", got, studyFallbackWindow)
	}
}
