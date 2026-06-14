package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
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

	t.Run("oversized output truncates when study unavailable", func(t *testing.T) {
		t.Chdir(t.TempDir())
		// head -c 20000 /dev/zero → 20KB, over maxToolOutput. With a nil
		// session the study path is unavailable; the old truncation
		// behavior must hold.
		args, _ := json.Marshal(map[string]string{"command": "head -c 20000 /dev/zero"})
		got, err := tc(FunctionBash, string(args)).Execute(context.Background(), nil)
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

func TestDefaultStudyPasses(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want int
	}{
		{"file", file, 1},
		{"dir", dir, dirStudyPasses},
		{"missing", filepath.Join(dir, "nope"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := defaultStudyPasses(c.path); got != c.want {
				t.Errorf("defaultStudyPasses(%s) = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

func TestSpillShellOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	out := []byte(strings.Repeat("log line\n", 100))
	p1, err := spillShellOutput("go test ./...", out)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	data, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(data) != string(out) {
		t.Error("spill content differs from output")
	}
	if !strings.HasPrefix(filepath.ToSlash(p1), ".cortex/shell/go-") {
		t.Errorf("spill path %q, want .cortex/shell/go-<hash>.txt", p1)
	}
	// Content-addressed: same output → same path (no pile-up).
	p2, err := spillShellOutput("go test ./...", out)
	if err != nil {
		t.Fatalf("spill 2: %v", err)
	}
	if p1 != p2 {
		t.Errorf("same output spilled to different paths: %q vs %q", p1, p2)
	}
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

	t.Run("both roles disable thinking by default; config can re-enable", func(t *testing.T) {
		var nilCfg *Config
		code := nilCfg.Spec(roleCode)
		if code.Thinking == nil || *code.Thinking {
			t.Errorf("code default Thinking = %v, want false", code.Thinking)
		}
		if study := nilCfg.Spec(roleStudy); study.Thinking == nil || *study.Thinking {
			t.Errorf("study default Thinking = %v, want false", study.Thinking)
		}
		on := true
		c := &Config{Models: map[string]ModelSpec{roleCode: {Thinking: &on}}}
		if got := c.Spec(roleCode); got.Thinking == nil || !*got.Thinking {
			t.Errorf("config thinking=true should override the default-off, got %v", got.Thinking)
		}
	})

	t.Run("key_service overrides layer on the default", func(t *testing.T) {
		c := &Config{Models: map[string]ModelSpec{roleCode: {KeyService: "cortex-openrouter"}}}
		if got := c.Spec(roleCode); got.KeyService != "cortex-openrouter" {
			t.Errorf("KeyService = %q, want cortex-openrouter", got.KeyService)
		}
	})
}

// TemplateKwargs: thinking=false is the only case that emits kwargs — nil and
// true both defer to the model's template default.
func TestTemplateKwargs(t *testing.T) {
	off, on := false, true
	tests := []struct {
		name     string
		thinking *bool
		want     bool // kwargs expected?
	}{
		{"nil defers to template default", nil, false},
		{"true defers to template default", &on, false},
		{"false emits enable_thinking=false", &off, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := ModelSpec{Thinking: tt.thinking}.TemplateKwargs()
			if !tt.want {
				if kw != nil {
					t.Errorf("TemplateKwargs() = %v, want nil", kw)
				}
				return
			}
			if v, ok := kw["enable_thinking"].(bool); !ok || v {
				t.Errorf("TemplateKwargs() = %v, want enable_thinking=false", kw)
			}
		})
	}
}

// The wire body must omit chat_template_kwargs when unset (universal
// compatibility) and carry it when the code role disables thinking.
func TestRequestMarshalsTemplateKwargs(t *testing.T) {
	bare, err := json.Marshal(&AgentRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "chat_template_kwargs") {
		t.Errorf("unset kwargs should be omitted from the body: %s", bare)
	}

	req := &AgentRequest{Model: "m", ChatTemplateKwargs: map[string]any{"enable_thinking": false}}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"chat_template_kwargs":{"enable_thinking":false}`) {
		t.Errorf("kwargs missing from body: %s", b)
	}
}

// windowSize falls back to the default when Window is unset, so the gauge never
// divides by zero or shows /0.
func TestWindowSizeFallback(t *testing.T) {
	def := &CortexSession{}
	if got := def.windowSize(); got != defaultModels[roleCode].Window {
		t.Errorf("windowSize() = %d, want default %d", got, defaultModels[roleCode].Window)
	}
	sized := &CortexSession{Window: 8192}
	if got := sized.windowSize(); got != 8192 {
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

// newTestSession builds a persisted session in an isolated cwd.
func newTestSession(t *testing.T) *CortexSession {
	t.Helper()
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript did not open a transcript file")
	}
	t.Cleanup(func() { cs.transcript.Close() })
	return cs
}

func TestTranscriptRoundTrip(t *testing.T) {
	cs := newTestSession(t)

	cs.Append(Message{Role: RoleUser, Content: "fix the bug"})
	cs.Append(Message{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "c1", Type: "function", Function: FunctionCall{Name: FunctionBash, Arguments: `{"command":"go test"}`}},
	}})
	cs.Append(Message{Role: RoleTool, ToolCallID: "c1", Content: "ok"})

	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.transcript.Close()

	want := cs.Request.Messages
	got := resumed.Request.Messages
	if len(got) != len(want) {
		t.Fatalf("resumed %d messages, want %d", len(got), len(want))
	}
	if got[0].Role != RoleSystem {
		t.Errorf("messages[0] role = %q, want the persisted system prompt", got[0].Role)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content || got[i].ToolCallID != want[i].ToolCallID {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The assistant message's tool calls must survive the round trip — resume
	// with a dangling tool result would 400 on the next send.
	if calls := got[2].ToolCalls; len(calls) != 1 || calls[0].ID != "c1" || calls[0].Function.Name != FunctionBash {
		t.Errorf("tool calls did not survive round trip: %+v", calls)
	}
	if resumed.SessionID != cs.SessionID {
		t.Errorf("resumed id %q, want %q", resumed.SessionID, cs.SessionID)
	}
}

func TestResumeAppendsToSameFile(t *testing.T) {
	cs := newTestSession(t)
	cs.Append(Message{Role: RoleUser, Content: "first life"})

	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.transcript.Close()
	resumed.Append(Message{Role: RoleUser, Content: "second life"})

	again := &CortexSession{Request: CortexArgs{}.Request()}
	if err := again.ResumeTranscript(""); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	defer again.transcript.Close()
	last := again.Request.Messages[len(again.Request.Messages)-1]
	if last.Content != "second life" {
		t.Errorf("post-resume append did not persist; last message = %q", last.Content)
	}
}

func TestResumeLatestPicksNewest(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := sessionsDir()
	os.MkdirAll(dir, 0755)
	line := func(content string) []byte {
		b, _ := json.Marshal(sessionEntry{Message: Message{Role: RoleUser, Content: content}})
		return append(b, '\n')
	}
	os.WriteFile(filepath.Join(dir, "20260101-000000.jsonl"), line("old"), 0644)
	os.WriteFile(filepath.Join(dir, "20260201-000000.jsonl"), line("new"), 0644)

	cs := &CortexSession{Request: CortexArgs{}.Request()}
	if err := cs.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer cs.transcript.Close()
	if cs.SessionID != "20260201-000000" {
		t.Errorf("resumed %q, want the newest session", cs.SessionID)
	}
	if cs.Request.Messages[0].Content != "new" {
		t.Errorf("loaded %q, want the newest transcript's content", cs.Request.Messages[0].Content)
	}
}

func TestResumeErrors(t *testing.T) {
	t.Run("no sessions dir", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.ResumeTranscript(""); err == nil {
			t.Fatal("expected error with no sessions directory")
		}
	})

	t.Run("malformed line is an error, not a silent skip", func(t *testing.T) {
		t.Chdir(t.TempDir())
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "20260101-000000.jsonl"), []byte("{not json\n"), 0644)

		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.ResumeTranscript(""); err == nil {
			t.Fatal("expected error for malformed transcript")
		}
	})
}

// An unpersisted session (study CLI, tests) must work identically — Append
// without a transcript is not an error.
func TestAppendWithoutTranscript(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Append(Message{Role: RoleUser, Content: "no persistence"})
	if n := len(cs.Request.Messages); n != 2 {
		t.Errorf("got %d messages, want 2", n)
	}
}

// Per-format grounding: code claims verify by symbol (one hit), prose/data
// claims by word overlap (two distinct hits), and claims without enough
// verifiable material fall to unscored rather than failed.
func TestClaimAnchorsPerFormat(t *testing.T) {
	tests := []struct {
		name     string
		claim    string
		lang     string
		wantMin  int // at least this many anchors
		wantNeed int
	}{
		{"code symbol", "The resolveAPIURL function checks for a slash", "code", 1, 1},
		{"code no symbol", "it checks for a slash here", "code", 0, 1},
		{"prose words", "The billing section describes timeout handling", "md", 2, 2},
		{"json words", "billing service reports timeout errors", "json", 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchors, need := claimAnchors(tt.claim, tt.lang)
			if len(anchors) < tt.wantMin {
				t.Errorf("anchors = %v, want >= %d", anchors, tt.wantMin)
			}
			if need != tt.wantNeed {
				t.Errorf("need = %d, want %d", need, tt.wantNeed)
			}
		})
	}
}

func TestScoreGroundednessPerFormat(t *testing.T) {
	content := "{\"service\":\"billing\",\"error\":\"timeout\"}\n{\"service\":\"search\",\"status\":200}\n"
	mk := func(claim string, lo, hi int) study.StudyLoopResult {
		return study.StudyLoopResult{Citations: []study.Citation{{Claim: claim, LineStart: lo, LineEnd: hi}}}
	}
	t.Run("json grounded at cited lines", func(t *testing.T) {
		g, f, u := scoreGroundedness(content, "json", mk("the billing service reports timeout errors", 1, 1))
		if g != 1 || f != 0 || u != 0 {
			t.Errorf("got g=%d f=%d u=%d, want 1/0/0", g, f, u)
		}
	})
	t.Run("json wrong location fails", func(t *testing.T) {
		g, f, u := scoreGroundedness(content, "json", mk("the billing service reports timeout errors", 2, 2))
		if g != 0 || f != 1 || u != 0 {
			t.Errorf("got g=%d f=%d u=%d, want 0/1/0", g, f, u)
		}
	})
	t.Run("thin claim is unscored", func(t *testing.T) {
		g, f, u := scoreGroundedness(content, "json", mk("it has data", 1, 1))
		if g != 0 || f != 0 || u != 1 {
			t.Errorf("got g=%d f=%d u=%d, want 0/0/1", g, f, u)
		}
	})
}

// CORTEX_LOOP_STUDY_WINDOW overrides every other window source — the
// recursion-experiment knob (force study mode on small digest corpora).
func TestStudyWindowEnvOverride(t *testing.T) {
	t.Setenv("CORTEX_LOOP_STUDY_WINDOW", "8192")
	cs := &CortexSession{Study: ModelSpec{Model: "reasoner", Window: 32768}}
	if got := cs.studyWindow(); got != 8192 {
		t.Errorf("studyWindow() = %d, want 8192 (env override)", got)
	}
	t.Setenv("CORTEX_LOOP_STUDY_WINDOW", "")
	if got := cs.studyWindow(); got != 32768 {
		t.Errorf("studyWindow() = %d, want 32768 (configured)", got)
	}
}

// studyCompletionCap: half the headroom, floored where that collapses to
// the JSON-truncation point (citations die silently below ~2K tokens).
func TestStudyCompletionCap(t *testing.T) {
	tests := []struct {
		window, want int
	}{
		{32768, 4096}, // headroom 8192 → half
		{8192, 2048},  // headroom 2048 → half=1024 → floored
		{6144, 2048},  // floor dominates small windows
	}
	for _, tt := range tests {
		if got := studyCompletionCap(tt.window); got != tt.want {
			t.Errorf("studyCompletionCap(%d) = %d, want %d", tt.window, got, tt.want)
		}
	}
}

// stubCompactStudy replaces the compaction study call (no model, no network)
// for the duration of a test, recording the path and window it was given.
func stubCompactStudy(t *testing.T, res study.StudyLoopResult, err error) (gotPath *string, gotWindow *int) {
	t.Helper()
	saved := compactStudy
	t.Cleanup(func() { compactStudy = saved })
	gotPath, gotWindow = new(string), new(int)
	compactStudy = func(_ context.Context, _ *CortexSession, path string, window int) (study.StudyLoopResult, error) {
		*gotPath, *gotWindow = path, window
		return res, err
	}
	return gotPath, gotWindow
}

func TestCompactRebuildsHistory(t *testing.T) {
	digest := study.StudyLoopResult{
		Stopped:     "budget",
		CoveragePct: 0.5,
		Digests:     []string{"user is hardening the loop; edited cmd/loop/main.go; tests pass"},
	}
	gotPath, gotWindow := stubCompactStudy(t, digest, nil)

	cs := newTestSession(t)
	cs.Window = 64000
	cs.Study.Window = 32768
	cs.LastPromptTokens = 60000
	cs.Append(Message{Role: RoleUser, Content: "long conversation"})
	cs.Append(Message{Role: "assistant", Content: "lots of work"})
	oldID := cs.SessionID
	sys := cs.Request.Messages[0]

	if err := cs.Compact(context.Background()); err != nil {
		t.Fatalf("compact: %v", err)
	}
	defer cs.transcript.Close()

	// Studied the right transcript with the consumer-derived budget:
	// min(codeWindow/4=16000, studyWindow=32768) = 16000.
	if !strings.HasSuffix(*gotPath, oldID+".jsonl") {
		t.Errorf("studied %q, want the old transcript %s.jsonl", *gotPath, oldID)
	}
	if *gotWindow != 16000 {
		t.Errorf("study window = %d, want 16000 (codeWindow/4)", *gotWindow)
	}

	// History = original system seed + one digest message.
	msgs := cs.Request.Messages
	if len(msgs) != 2 {
		t.Fatalf("compacted history has %d messages, want 2", len(msgs))
	}
	if msgs[0].Content != sys.Content || msgs[0].Role != RoleSystem {
		t.Error("system seed should survive compaction unchanged")
	}
	if msgs[1].Role != RoleUser || !strings.Contains(msgs[1].Content, "hardening the loop") {
		t.Errorf("digest message = %+v", msgs[1])
	}

	// Gauge reset; new transcript with a new id; old transcript intact.
	if cs.LastPromptTokens != 0 {
		t.Errorf("LastPromptTokens = %d, want 0 after compaction", cs.LastPromptTokens)
	}
	if cs.SessionID == oldID {
		t.Error("compaction should start a NEW session id")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir(), oldID+".jsonl")); err != nil {
		t.Errorf("raw transcript should stay on disk: %v", err)
	}

	// The new transcript must resume to exactly the compacted state.
	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(cs.SessionID); err != nil {
		t.Fatalf("resume after compact: %v", err)
	}
	defer resumed.transcript.Close()
	if len(resumed.Request.Messages) != 2 || !strings.Contains(resumed.Request.Messages[1].Content, "hardening the loop") {
		t.Errorf("resume should restore the compacted state, got %d messages", len(resumed.Request.Messages))
	}
}

func TestCompactErrors(t *testing.T) {
	t.Run("unpersisted session", func(t *testing.T) {
		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.Compact(context.Background()); err == nil {
			t.Fatal("expected error for unpersisted session")
		}
	})

	t.Run("read mode is refused — nothing to compress", func(t *testing.T) {
		stubCompactStudy(t, study.StudyLoopResult{Stopped: "read"}, nil)
		cs := newTestSession(t)
		cs.Append(Message{Role: RoleUser, Content: "short"})
		before := len(cs.Request.Messages)

		err := cs.Compact(context.Background())
		if err == nil || !strings.Contains(err.Error(), "nothing to compact") {
			t.Fatalf("expected nothing-to-compact error, got %v", err)
		}
		if len(cs.Request.Messages) != before {
			t.Error("a refused compact must leave history unchanged")
		}
	})

	t.Run("empty digest leaves history unchanged", func(t *testing.T) {
		stubCompactStudy(t, study.StudyLoopResult{Stopped: "budget", Digests: []string{"  "}}, nil)
		cs := newTestSession(t)
		cs.Append(Message{Role: RoleUser, Content: "work"})
		before := len(cs.Request.Messages)

		if err := cs.Compact(context.Background()); err == nil {
			t.Fatal("expected error for empty digest")
		}
		if len(cs.Request.Messages) != before {
			t.Error("a failed compact must leave history unchanged")
		}
	})
}

func TestClearResetsSession(t *testing.T) {
	cs := newTestSession(t)
	cs.Request.Model = "switched-model"
	cs.Request.BaseURL = "http://somewhere:1234"
	cs.LastPromptTokens = 9000
	cs.Append(Message{Role: RoleUser, Content: "old work"})
	oldID := cs.SessionID

	cs.Clear()
	defer cs.transcript.Close()

	if n := len(cs.Request.Messages); n != 1 || cs.Request.Messages[0].Role != RoleSystem {
		t.Errorf("cleared history = %d messages, want just the system seed", n)
	}
	if cs.Request.Model != "switched-model" || cs.Request.BaseURL != "http://somewhere:1234" {
		t.Error("clear must preserve the model binding")
	}
	if cs.LastPromptTokens != 0 {
		t.Error("clear must reset the gauge")
	}
	if cs.SessionID == oldID {
		t.Error("clear should start a new session id")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir(), oldID+".jsonl")); err != nil {
		t.Errorf("old transcript should stay on disk: %v", err)
	}
}

// Same-second sessions (compact and clear do this routinely) must get
// distinct transcript files, not interleave into one.
func TestStartTranscriptCollisionSafe(t *testing.T) {
	t.Chdir(t.TempDir())
	a := &CortexSession{Request: CortexArgs{}.Request()}
	b := &CortexSession{Request: CortexArgs{}.Request()}
	a.StartTranscript()
	b.StartTranscript()
	defer a.transcript.Close()
	defer b.transcript.Close()

	if a.SessionID == "" || b.SessionID == "" {
		t.Fatal("both sessions should persist")
	}
	if a.SessionID == b.SessionID {
		t.Errorf("same-second sessions share id %q", a.SessionID)
	}
}

func TestContextRatio(t *testing.T) {
	cs := CortexSession{Window: 1000, LastPromptTokens: 800}
	if got := cs.contextRatio(); got != 0.8 {
		t.Errorf("contextRatio = %v, want 0.8", got)
	}
	// The gauge color and the compact trigger share the same threshold.
	if ctxColor(800, 1000) != red {
		t.Error("gauge should be red exactly at compactThreshold")
	}
	if ctxColor(799, 1000) != yellow {
		t.Error("gauge should be yellow just under compactThreshold")
	}
}

// Shell metacharacters get an explicit, instructive rejection — the tool
// execs without a shell, so a passed-through `|` previously reached the
// binary as a literal arg and produced confusing downstream errors the
// model retried verbatim ("find: |: unknown primary").
func TestBashRejectsShellSyntax(t *testing.T) {
	cases := []string{
		`find . -name "*.go" | head -30`,
		"go vet ./... 2>&1",
		"echo hi > out.txt",
		"ls; pwd",
		"echo $(pwd)",
		"go test ./... && echo ok",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"command": cmd})
			_, err := tc(FunctionBash, string(args)).Execute(context.Background(), nil)
			if err == nil {
				t.Fatal("expected shell-syntax rejection")
			}
			if !strings.Contains(err.Error(), "not supported") {
				t.Errorf("error should explain the limitation, got %q", err)
			}
		})
	}
	// Bare commands still run.
	args, _ := json.Marshal(map[string]string{"command": "echo plain"})
	if out, err := tc(FunctionBash, string(args)).Execute(context.Background(), nil); err != nil || !strings.Contains(out, "plain") {
		t.Errorf("bare command should still run: out=%q err=%v", out, err)
	}
}

// --- Retrieval -------------------------------------------------------------

func TestFormatRetrieved(t *testing.T) {
	t.Run("empty input yields empty string", func(t *testing.T) {
		if got := formatRetrieved(nil); got != "" {
			t.Errorf("formatRetrieved(nil) = %q, want empty", got)
		}
	})

	t.Run("all-blank content yields empty string", func(t *testing.T) {
		got := formatRetrieved([]cognition.Result{{Content: "   "}, {Content: ""}})
		if got != "" {
			t.Errorf("blank content should produce no note, got %q", got)
		}
	})

	t.Run("labels category, collapses whitespace, marks provenance", func(t *testing.T) {
		got := formatRetrieved([]cognition.Result{
			{Category: "decision", Content: "Use pgx\n  not database/sql"},
			{Content: "no category here"}, // → "note"
		})
		for _, want := range []string{
			"retrieved, not user-authored",
			"- [decision] Use pgx not database/sql",
			"- [note] no category here",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("formatRetrieved missing %q in:\n%s", want, got)
			}
		}
		if strings.Contains(got, "\n  not") {
			t.Error("content newlines should be collapsed")
		}
	})

	t.Run("oversized content is truncated", func(t *testing.T) {
		long := strings.Repeat("x", retrievedContentCap+50)
		got := formatRetrieved([]cognition.Result{{Content: long}})
		if !strings.Contains(got, "…") {
			t.Error("oversized snippet should be truncated with an ellipsis")
		}
		if len(got) > len("# Relevant context from memory (retrieved, not user-authored)\n- [note] ")+retrievedContentCap+10 {
			t.Errorf("truncation cap did not hold: %d bytes", len(got))
		}
	})
}

// wireMessages folds the ephemeral note into the system message for the wire
// only — the stored Messages must be untouched, so nothing accumulates.
func TestWireMessagesComposesEphemerally(t *testing.T) {
	req := CortexArgs{}.Request()
	orig := req.Messages[0].Content

	t.Run("no ephemeral → system content unchanged", func(t *testing.T) {
		if req.wireMessages()[0].Content != orig {
			t.Error("without ephemeral, system content should be unchanged")
		}
	})

	t.Run("ephemeral folds into system on the wire, not in storage", func(t *testing.T) {
		req.EphemeralSystem = "# memory\n- [decision] use pgx"
		wire := req.wireMessages()
		if !strings.Contains(wire[0].Content, "use pgx") {
			t.Error("wire system message should carry the ephemeral note")
		}
		if !strings.HasPrefix(wire[0].Content, orig) {
			t.Error("wire system message should keep the original prompt as prefix")
		}
		if req.Messages[0].Content != orig {
			t.Error("stored system message must NOT be mutated by composition")
		}
	})
}

func TestRetrieveDisabledReturnsNil(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()} // retriever == nil
	if got := cs.retrieve("anything"); got != nil {
		t.Errorf("retrieve with no retriever = %v, want nil", got)
	}
}

// Round-trip: a captured insight under the project's .cortex/ store must
// surface through the loop's Fast retrieval, AND be recorded to the transcript
// as a kindRetrieval entry while staying OUT of the replayed window on resume
// (record-only policy). No model: Reflex is text-only here.
func TestRetrieveRecordsButDoesNotReplay(t *testing.T) {
	t.Chdir(t.TempDir())

	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.StartTranscript()
	cs.EnableRetrieval()
	if cs.retriever == nil || cs.transcript == nil {
		t.Fatal("EnableRetrieval/StartTranscript should both succeed in a writable dir")
	}
	t.Cleanup(cs.Close)
	id := cs.SessionID

	cap := capture.NewWithStorage(
		&config.Config{ContextDir: contextDir(), ProjectRoot: filepath.Dir(contextDir())},
		cs.store,
	)
	if err := cap.CaptureEvent(&events.Event{
		ID:        "evt-loop-rt",
		Source:    events.SourceGeneric,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "loop",
		ToolInput: map[string]interface{}{
			"type":    "decision",
			"content": "we use JWT for authentication, not server-side sessions",
		},
		ToolResult: "captured the JWT authentication decision",
		Context:    events.EventContext{SessionID: "s1", ProjectPath: contextDir()},
	}); err != nil {
		t.Fatalf("CaptureEvent: %v", err)
	}

	// A turn: retrieve, record, and (would) inject.
	hits := cs.retrieve("authentication")
	if len(hits) == 0 {
		t.Fatal("retrieve found nothing — capture is not visible to the loop's retrieval")
	}
	cs.recordRetrieval("authentication", hits)
	cs.Append(Message{Role: RoleUser, Content: "how does auth work?"})

	// The note that WOULD be injected carries provenance + content.
	note := formatRetrieved(hits)
	if !strings.Contains(note, "retrieved, not user-authored") {
		t.Errorf("note missing provenance header:\n%s", note)
	}
	if !strings.Contains(strings.ToLower(note), "jwt") && !strings.Contains(strings.ToLower(note), "authentication") {
		t.Errorf("note should carry the captured content:\n%s", note)
	}

	// The raw transcript records the retrieval (debuggability)...
	raw, err := os.ReadFile(filepath.Join(sessionsDir(), id+".jsonl"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(raw), `"kind":"retrieval"`) {
		t.Error("transcript should record a kindRetrieval entry")
	}
	if !strings.Contains(string(raw), "JWT") {
		t.Error("recorded retrieval should carry the retrieved content")
	}

	// ...but resume rebuilds the window from core messages ONLY: the system
	// seed and the user turn, never the retrieval entry.
	msgs, err := loadTranscript(filepath.Join(sessionsDir(), id+".jsonl"))
	if err != nil {
		t.Fatalf("loadTranscript: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "JWT") || strings.Contains(m.Content, "retrieved, not user-authored") {
			t.Errorf("retrieval must not be replayed into the window, but found: %q", m.Content)
		}
	}
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	if len(msgs) != 2 || roles[0] != RoleSystem || roles[1] != RoleUser {
		t.Errorf("replayed window = roles %v, want [system user] (core conversation only)", roles)
	}
}

// Older transcripts have no kind field; those entries must still replay as
// core messages.
func TestLoadTranscriptBackCompat(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := sessionsDir()
	os.MkdirAll(dir, 0755)
	// Legacy line: {ts, role, content} with no "kind".
	legacy := `{"ts":"2026-01-01T00:00:00Z","role":"user","content":"legacy turn"}` + "\n"
	path := filepath.Join(dir, "20260101-000000.jsonl")
	os.WriteFile(path, []byte(legacy), 0644)

	msgs, err := loadTranscript(path)
	if err != nil {
		t.Fatalf("loadTranscript: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "legacy turn" {
		t.Errorf("legacy (kind-less) entry should replay as a core message, got %+v", msgs)
	}
}

// --- Capture (Tier 1) ------------------------------------------------------

func TestTurnArtifacts(t *testing.T) {
	t.Run("extracts edited files, commands, and the final answer", func(t *testing.T) {
		msgs := []Message{
			{Role: RoleUser, Content: "fix the bug and test it"},
			{Role: "assistant", ToolCalls: []ToolCall{
				{Function: FunctionCall{Name: FunctionEditFile, Arguments: `{"path":"main.go"}`}},
				{Function: FunctionCall{Name: FunctionBash, Arguments: `{"command":"go test ./..."}`}},
			}},
			{Role: RoleTool, Content: "ok"},
			{Role: "assistant", Content: "Done — fixed and tested."},
		}
		outcome, answer := turnArtifacts(msgs)
		for _, want := range []string{"edited: main.go", "ran: go test ./..."} {
			if !strings.Contains(outcome, want) {
				t.Errorf("outcome %q missing %q", outcome, want)
			}
		}
		if answer != "Done — fixed and tested." {
			t.Errorf("answer = %q, want the final assistant message", answer)
		}
	})

	t.Run("read-only turn has empty outcome but keeps the answer", func(t *testing.T) {
		msgs := []Message{
			{Role: RoleUser, Content: "how does auth work?"},
			{Role: "assistant", Content: "It uses JWT."},
		}
		outcome, answer := turnArtifacts(msgs)
		if outcome != "" {
			t.Errorf("read-only outcome should be empty, got %q", outcome)
		}
		if answer != "It uses JWT." {
			t.Errorf("answer = %q", answer)
		}
	})

	t.Run("repeated edits to one file are de-duplicated", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", ToolCalls: []ToolCall{
				{Function: FunctionCall{Name: FunctionEditFile, Arguments: `{"path":"a.go"}`}},
			}},
			{Role: "assistant", ToolCalls: []ToolCall{
				{Function: FunctionCall{Name: FunctionEditFile, Arguments: `{"path":"a.go"}`}},
			}},
		}
		outcome, _ := turnArtifacts(msgs)
		if strings.Count(outcome, "a.go") != 1 {
			t.Errorf("file should appear once, got %q", outcome)
		}
	})
}

func TestCaptureDisabledIsNoOp(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}                        // capturer == nil
	cs.captureTurn("anything", []Message{{Role: RoleUser, Content: "anything"}}) // must not panic
	if err := cs.remember("note"); err == nil {
		t.Error("remember without a store should return an error")
	}
}

// Every completed turn is captured — read-only included — and is retrievable.
func TestCaptureTurnIsRetrievable(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.StartTranscript()
	cs.EnableRetrieval()
	if cs.capturer == nil {
		t.Fatal("EnableRetrieval should wire a capturer")
	}
	t.Cleanup(cs.Close)

	// A read-only turn where the USER states a durable fact (no file edits).
	cs.captureTurn("we use JWT for authentication, not server-side sessions", []Message{
		{Role: RoleUser, Content: "we use JWT for authentication, not server-side sessions"},
		{Role: "assistant", Content: "Understood — JWT it is."},
	})

	hits := cs.retrieve("authentication")
	if len(hits) == 0 {
		t.Fatal("a captured read-only turn must be retrievable — read-only lessons matter")
	}
	found := false
	for _, h := range hits {
		if strings.Contains(strings.ToLower(h.Content), "jwt") || strings.Contains(strings.ToLower(h.Content), "authentication") {
			found = true
		}
	}
	if !found {
		t.Errorf("retrieved hits should carry the captured content: %+v", hits)
	}
}

func TestRememberIsRetrievable(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.StartTranscript()
	cs.EnableRetrieval()
	t.Cleanup(cs.Close)

	if err := cs.remember("the staging database is reset every night at 2am UTC"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	hits := cs.retrieve("staging database reset")
	if len(hits) == 0 {
		t.Fatal("an explicit /remember memory must be retrievable")
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Content, "staging") {
			found = true
		}
	}
	if !found {
		t.Errorf("hits should carry the remembered text: %+v", hits)
	}
}

// --- Capture (Tier 2: distillation) ----------------------------------------

// stubDistill replaces the reasoner call for the test's duration, returning a
// canned analysis response. No model, no network.
func stubDistill(t *testing.T, response string, err error) *int {
	t.Helper()
	saved := distillExtract
	t.Cleanup(func() { distillExtract = saved })
	calls := new(int)
	distillExtract = func(_ context.Context, _ llm.Provider, _ string) (string, error) {
		*calls++
		return response, err
	}
	return calls
}

func newDistillSession(t *testing.T) *CortexSession {
	t.Helper()
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.StartTranscript()
	cs.EnableRetrieval()
	if cs.store == nil {
		t.Fatal("EnableRetrieval should open a store")
	}
	t.Cleanup(cs.Close)
	return cs
}

func TestDistillPendingStoresInsight(t *testing.T) {
	stubDistill(t, `{"content":"use pgx, not database/sql","category":"decision","importance":0.8,"tags":["db"]}`, nil)
	cs := newDistillSession(t)
	cs.pendingTurns = []pendingTurn{{user: "what db driver?", msgs: []Message{
		{Role: RoleUser, Content: "what db driver?"},
		{Role: "assistant", Content: "We use pgx."},
	}}}

	cs.distillPending(context.Background())

	if len(cs.pendingTurns) != 0 {
		t.Errorf("distilled turn should be consumed, %d left", len(cs.pendingTurns))
	}
	// The insight is in the insights layer and retrievable.
	hits := cs.retrieve("database driver")
	found := false
	for _, h := range hits {
		if strings.Contains(strings.ToLower(h.Content), "pgx") {
			found = true
		}
	}
	if !found {
		t.Errorf("distilled insight should be retrievable, got %+v", hits)
	}
}

func TestDistillPendingDedups(t *testing.T) {
	cs := newDistillSession(t)
	// Pre-store the insight the reasoner is about to "discover" again.
	if err := cs.store.StoreInsightWithSession("", "decision", "use pgx, not database/sql", 8, nil, "", cs.SessionID, "loop"); err != nil {
		t.Fatal(err)
	}
	before, _ := cs.store.GetRecentInsights(100)

	stubDistill(t, `{"content":"Use pgx, not database/sql.","category":"decision","importance":0.8}`, nil)
	cs.pendingTurns = []pendingTurn{{user: "db?", msgs: []Message{{Role: RoleUser, Content: "db?"}}}}
	cs.distillPending(context.Background())

	after, _ := cs.store.GetRecentInsights(100)
	if len(after) != len(before) {
		t.Errorf("duplicate insight should not be stored: before=%d after=%d", len(before), len(after))
	}
	if len(cs.pendingTurns) != 0 {
		t.Error("turn should still be consumed even when deduped")
	}
}

func TestDistillPendingNoInsightConsumesTurn(t *testing.T) {
	calls := stubDistill(t, "NO_INSIGHT", nil)
	cs := newDistillSession(t)
	cs.pendingTurns = []pendingTurn{{user: "hi", msgs: []Message{{Role: RoleUser, Content: "hi"}}}}

	cs.distillPending(context.Background())

	if *calls != 1 {
		t.Errorf("reasoner should be called once, got %d", *calls)
	}
	if len(cs.pendingTurns) != 0 {
		t.Error("a NO_INSIGHT turn must still be consumed (not retried forever)")
	}
	if got, _ := cs.store.GetRecentInsights(10); len(got) != 0 {
		t.Errorf("NO_INSIGHT should store nothing, got %d insights", len(got))
	}
}

func TestDistillPendingPreemptedLeavesTurn(t *testing.T) {
	calls := stubDistill(t, `{"content":"x","category":"pattern","importance":0.5}`, nil)
	cs := newDistillSession(t)
	cs.pendingTurns = []pendingTurn{{user: "q", msgs: []Message{{Role: RoleUser, Content: "q"}}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already preempted

	cs.distillPending(ctx)

	if *calls != 0 {
		t.Errorf("preempted distill should not call the reasoner, got %d", *calls)
	}
	if len(cs.pendingTurns) != 1 {
		t.Error("a preempted turn must stay pending for the next idle")
	}
}

// A transient model error leaves the turn pending; a later retry succeeds.
func TestDistillPendingTransientErrorRetries(t *testing.T) {
	saved := distillExtract
	t.Cleanup(func() { distillExtract = saved })
	cs := newDistillSession(t)
	cs.pendingTurns = []pendingTurn{{user: "db?", msgs: []Message{{Role: RoleUser, Content: "db?"}}}}

	distillExtract = func(_ context.Context, _ llm.Provider, _ string) (string, error) {
		return "", errTest
	}
	cs.distillPending(context.Background())
	if len(cs.pendingTurns) != 1 {
		t.Fatal("a model error must leave the turn pending")
	}

	distillExtract = func(_ context.Context, _ llm.Provider, _ string) (string, error) {
		return `{"content":"use pgx","category":"decision","importance":0.7}`, nil
	}
	cs.distillPending(context.Background())
	if len(cs.pendingTurns) != 0 {
		t.Error("retry should consume the turn")
	}
}

var errTest = fmt.Errorf("transient model error")

func TestIsDuplicateInsight(t *testing.T) {
	known := []string{"Use pgx, not database/sql"}
	for _, dup := range []string{"use pgx, not database/sql", "Use  pgx,  not   database/sql", "USE PGX, NOT DATABASE/SQL"} {
		if !isDuplicateInsight(dup, known) {
			t.Errorf("%q should be a duplicate", dup)
		}
	}
	if isDuplicateInsight("use sqlx for queries", known) {
		t.Error("distinct insight should not be a duplicate")
	}
}

// --- Session metrics (6a) --------------------------------------------------

func TestSessionSummary(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now().Add(-90 * time.Second)}
	cs.turns, cs.tokensIn, cs.tokensOut, cs.captures, cs.retrievals = 5, 52000, 8000, 9, 6
	cs.insights.Store(4)
	s := cs.sessionSummary()
	for _, want := range []string{"5 turns", "52k in", "8k out", "9 captured", "4 insights", "6 retrievals"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
}

func TestResolveAccumulatesTokens(t *testing.T) {
	quickRetries(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	cs.Append(Message{Role: RoleUser, Content: "hi"})
	if err := cs.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cs.tokensIn != 12 || cs.tokensOut != 3 {
		t.Errorf("accumulated tokens = %d in / %d out, want 12/3", cs.tokensIn, cs.tokensOut)
	}
}

func TestEmitSessionMetrics(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now()}
	cs.StartTranscript()
	t.Cleanup(func() {
		if cs.transcript != nil {
			cs.transcript.Close()
		}
	})
	cs.turns, cs.tokensIn, cs.tokensOut, cs.captures, cs.retrievals, cs.injectedChars = 3, 1200, 340, 2, 1, 400
	cs.insights.Store(1)

	cs.emitSessionMetrics()

	r, err := journal.NewReader(filepath.Join(contextDir(), "journal", "eval"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var got []*journal.EvalCellResultPayload
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		if p, perr := journal.ParseEvalCellResult(e); perr == nil {
			got = append(got, p)
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %d eval.cell_result entries, want 1", len(got))
	}
	p := got[0]
	if p.Harness != "loop" || p.RunID != cs.SessionID || p.ScenarioID != "repl-session" {
		t.Errorf("identity wrong: harness=%q run=%q scenario=%q", p.Harness, p.RunID, p.ScenarioID)
	}
	if p.TokensIn != 1200 || p.TokensOut != 340 || p.AgentTurnsTotal != 3 {
		t.Errorf("metrics wrong: in=%d out=%d turns=%d", p.TokensIn, p.TokensOut, p.AgentTurnsTotal)
	}
	if p.InjectedContextTokens != 100 { // 400 chars / 4
		t.Errorf("injected tokens = %d, want 100", p.InjectedContextTokens)
	}
	if p.ContextStrategy != "none" { // retriever nil in this test
		t.Errorf("context strategy = %q, want none", p.ContextStrategy)
	}
	if !strings.Contains(p.Notes, "insights=1") || !strings.Contains(p.Notes, "captures=2") {
		t.Errorf("notes = %q", p.Notes)
	}
}

// An unpersisted session (no SessionID) emits nothing rather than erroring.
func TestEmitSessionMetricsUnpersistedNoOp(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now()}
	cs.emitSessionMetrics() // must not panic; SessionID == "" → skip
	if _, err := os.Stat(filepath.Join(contextDir(), "journal", "eval")); err == nil {
		t.Error("unpersisted session should not write an eval entry")
	}
}
