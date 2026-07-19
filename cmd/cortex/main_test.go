package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/shellrisk"

	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestMain disables the self-contained local embedder for the whole package so
// EnableRetrieval-driven tests never trigger a background model download.
// Tests that want it can re-enable via t.Setenv.
func TestMain(m *testing.M) {
	if os.Getenv("CORTEX_LOCAL_EMBED") == "" {
		_ = os.Setenv("CORTEX_LOCAL_EMBED", "0")
	}
	os.Exit(m.Run())
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
		{1000000, "1M"},
		{1048576, "1M"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		if got := humanK(tt.in); got != tt.want {
			t.Errorf("humanK(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCtxColor(t *testing.T) {
	win := 131072
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

// testFleet mirrors the live fleet for resolution tests. qwen3-4b carries a
// "fast" Role tag as fleet-discovery data only — no rolePolicy matches that
// tag since E1's role collapse (docs/completion-roadmap.md), but the model
// itself is still a useful non-thinking fixture for tests below that pin it
// explicitly.
var testFleet = Fleet{
	"coder":        {Role: "coder", MaxInput: 131072, Thinking: true, SwapGroup: "igpu-8080"},
	"reasoner":     {Role: "reasoner", MaxInput: 32768, Thinking: true, SwapGroup: "igpu-8080"},
	"reasoner-npu": {Role: "reasoner", MaxInput: 32768, Thinking: true},
	"qwen3-4b":     {Role: "fast", MaxInput: 131072, Thinking: false, SwapGroup: "igpu-8080"},
	"embedder":     {Role: "embedder", MaxInput: 32768},
}

// selectModel picks a role's model from discovery by capability, with no
// model names baked in source. study prefers swap-free silicon.
func TestSelectModel(t *testing.T) {
	cases := []struct{ role, want string }{
		{roleCode, "coder"},
		{roleStudy, "reasoner-npu"},
		{roleEmbed, "embedder"},
	}
	for _, c := range cases {
		if got := selectModel(testFleet, c.role); got != c.want {
			t.Errorf("selectModel(%s) = %q, want %q", c.role, got, c.want)
		}
	}
	t.Run("study auto-falls-back to reasoner when the NPU model is gone", func(t *testing.T) {
		f := Fleet{"reasoner": {Role: "reasoner", MaxInput: 32768, SwapGroup: "igpu-8080"}}
		if got := selectModel(f, roleStudy); got != "reasoner" {
			t.Errorf("got %q, want reasoner", got)
		}
	})
	t.Run("nil fleet selects nothing", func(t *testing.T) {
		if got := selectModel(nil, roleCode); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestResolveBinding(t *testing.T) {
	t.Run("nil config selects from discovery by capability", func(t *testing.T) {
		var c *Config
		code := c.resolveBinding(roleCode, testFleet)
		if code.Model != "coder" || code.Endpoint == "" || code.Window != 131072 {
			t.Errorf("code = %+v", code)
		}
		if study := c.resolveBinding(roleStudy, testFleet); study.Model != "reasoner-npu" || study.Window != 32768 {
			t.Errorf("study = %+v", study)
		}
	})

	t.Run("config pins the model; window from discovery, endpoint from backend", func(t *testing.T) {
		c := &Config{Models: map[string]ModelSpec{roleStudy: {Model: "coder"}}}
		s := c.resolveBinding(roleStudy, testFleet)
		if s.Model != "coder" {
			t.Errorf("model = %q, want pinned coder", s.Model)
		}
		if s.Window != 131072 || s.Endpoint != c.backendEndpoint() {
			t.Errorf("window from discovery + endpoint from backend, got %+v", s)
		}
	})

	t.Run("config-pinned window wins over discovery", func(t *testing.T) {
		c := &Config{Models: map[string]ModelSpec{roleCode: {Window: 8000}}}
		if s := c.resolveBinding(roleCode, testFleet); s.Window != 8000 {
			t.Errorf("window = %d, want pinned 8000", s.Window)
		}
	})

	t.Run("temperature defaults globally and per-role can override", func(t *testing.T) {
		global := 0.7
		codeTemp := 0.2
		c := &Config{
			Temperature: &global,
			Models:      map[string]ModelSpec{roleCode: {Temperature: &codeTemp}},
		}
		if got := c.resolveBinding(roleCode, testFleet).temperature(defaultTemperature); got != codeTemp {
			t.Errorf("code temperature = %v, want per-role %v", got, codeTemp)
		}
		if got := c.resolveBinding(roleStudy, testFleet).temperature(defaultTemperature); got != global {
			t.Errorf("study temperature = %v, want global %v", got, global)
		}
		var nilCfg *Config
		if got := nilCfg.resolveBinding(roleCode, testFleet).temperature(defaultTemperature); got != defaultTemperature {
			t.Errorf("nil config temperature = %v, want default %v", got, defaultTemperature)
		}
	})

	t.Run("thinking on for code by default; fleet degrades non-thinkers; config can disable", func(t *testing.T) {
		var nilCfg *Config
		// Pure role default (nil fleet): code deliberates by default —
		// Derek's 2026-07-17 call; effort-off is opt-in via config.
		if code := nilCfg.resolveBinding(roleCode, nil); code.Thinking.Level != llm.EffortOn {
			t.Errorf("code Thinking = %+v, want on by default", code.Thinking)
		}
		// testFleet's coder model IS thinking-capable (hybrid), so the "on"
		// default survives fleet resolution. (The degrade-to-unset path for a
		// non-thinking fleet model is covered by the webui models golden.)
		if code := nilCfg.resolveBinding(roleCode, testFleet); code.Thinking.Level != llm.EffortOn {
			t.Errorf("code Thinking = %+v, want on (hybrid fleet model keeps it)", code.Thinking)
		}
		// study draws from the reasoner tag and deliberates: an explicit "on"
		// default now, rather than the old implicit nil.
		if study := nilCfg.resolveBinding(roleStudy, testFleet); study.Thinking.Level != llm.EffortOn {
			t.Errorf("study Thinking = %+v, want on (reasoner thinks by default)", study.Thinking)
		}
		c := &Config{Models: map[string]ModelSpec{roleCode: {Thinking: llm.Effort{Level: llm.EffortOff}}}}
		if got := c.resolveBinding(roleCode, testFleet); got.Thinking.Level != llm.EffortOff {
			t.Errorf("config thinking=off should win, got %+v", got.Thinking)
		}
	})

	// Role-default "off" degrading to unset for a "none" (non-thinking)
	// fleet model is covered generically by TestApplyFleet's "drops
	// enable_thinking for a non-thinking model" — no live role defaults to
	// "off" since E1's role collapse removed the fast role.

	t.Run("explicit config thinking survives a backend-non-thinking model", func(t *testing.T) {
		// qwen3-4b is reported thinking:false by the backend, but it thinks by
		// default and needs enable_thinking=false. A config override must NOT be
		// stripped by applyFleet (the regression that made study run it slow).
		c := &Config{Models: map[string]ModelSpec{roleStudy: {Model: "qwen3-4b", Thinking: llm.Effort{Level: llm.EffortOff}}}}
		got := c.resolveBinding(roleStudy, testFleet)
		if got.Model != "qwen3-4b" {
			t.Fatalf("model = %q, want qwen3-4b", got.Model)
		}
		if got.Thinking.Level != llm.EffortOff {
			t.Errorf("config thinking=off must survive applyFleet, got %+v", got.Thinking)
		}
		if kw := got.TemplateKwargs(); kw["enable_thinking"] != false {
			t.Errorf("TemplateKwargs should send enable_thinking=false, got %v", kw)
		}
	})

	t.Run("key_service: per-role override, else backend default", func(t *testing.T) {
		c := &Config{
			Backend: Backend{KeyService: "backend-key"},
			Models:  map[string]ModelSpec{roleCode: {KeyService: "cortex-openrouter"}},
		}
		if got := c.resolveBinding(roleCode, testFleet); got.KeyService != "cortex-openrouter" {
			t.Errorf("per-role key = %q, want cortex-openrouter", got.KeyService)
		}
		if got := c.resolveBinding(roleStudy, testFleet); got.KeyService != "backend-key" {
			t.Errorf("study should inherit backend key, got %q", got.KeyService)
		}
	})

	t.Run("key_env: per-role override, else backend default", func(t *testing.T) {
		c := &Config{
			Backend: Backend{KeyEnv: "BACKEND_KEY"},
			Models:  map[string]ModelSpec{roleCode: {KeyEnv: "OPENROUTER_API_KEY"}},
		}
		if got := c.resolveBinding(roleCode, testFleet); got.KeyEnv != "OPENROUTER_API_KEY" {
			t.Errorf("per-role key_env = %q, want OPENROUTER_API_KEY", got.KeyEnv)
		}
		if got := c.resolveBinding(roleStudy, testFleet); got.KeyEnv != "BACKEND_KEY" {
			t.Errorf("study should inherit backend key_env, got %q", got.KeyEnv)
		}
	})
}

func TestResolveKey(t *testing.T) {
	t.Run("key_env wins when the var is set", func(t *testing.T) {
		t.Setenv("CORTEX_TEST_KEY", "sk-from-env")
		if got := resolveKey(ModelSpec{KeyEnv: "CORTEX_TEST_KEY", KeyService: "ignored"}); got != "sk-from-env" {
			t.Errorf("resolveKey = %q, want sk-from-env", got)
		}
	})

	t.Run("empty when neither source is set", func(t *testing.T) {
		if got := resolveKey(ModelSpec{}); got != "" {
			t.Errorf("resolveKey = %q, want empty", got)
		}
	})

	t.Run("blank env value falls through to keychain", func(t *testing.T) {
		t.Setenv("CORTEX_TEST_KEY", "   ")
		// KeyService is empty, so keychainKey returns "" without shelling out —
		// proves the env path doesn't return a blank value as if it were a key.
		if got := resolveKey(ModelSpec{KeyEnv: "CORTEX_TEST_KEY"}); got != "" {
			t.Errorf("resolveKey = %q, want empty (blank env is not a key)", got)
		}
	})
}

func TestLoadMergedConfig(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("project overrides user, inherits the rest", func(t *testing.T) {
		dir := t.TempDir()
		userPath := write(t, dir, "user.json", `{
			"temperature": 0.6,
			"backend": {"type": "openrouter", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY"},
			"models": {
				"code":  {"model": "qwen/qwen3-coder:free"},
				"study": {"model": "openai/gpt-oss-20b:free"}
			}
		}`)
		projPath := write(t, dir, "proj.json", `{
			"models": {"code": {"model": "anthropic/claude-sonnet"}}
		}`)

		cfg := loadMergedConfig(userPath, projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		// Project overrode only code's model.
		if cfg.Models["code"].Model != "anthropic/claude-sonnet" {
			t.Errorf("code model = %q, want the project override", cfg.Models["code"].Model)
		}
		// Backend and the study role inherited from the user layer.
		if cfg.Backend.Type != "openrouter" || cfg.Backend.KeyEnv != "OPENROUTER_API_KEY" {
			t.Errorf("backend not inherited: %+v", cfg.Backend)
		}
		if cfg.Models["study"].Model != "openai/gpt-oss-20b:free" {
			t.Errorf("study model = %q, want inherited free model", cfg.Models["study"].Model)
		}
		if cfg.Temperature == nil || *cfg.Temperature != 0.6 {
			t.Errorf("temperature = %v, want inherited 0.6", cfg.Temperature)
		}
	})

	t.Run("field-level merge within a shared role", func(t *testing.T) {
		dir := t.TempDir()
		userTemp := 0.8
		projectTemp := 0.3
		userPath := write(t, dir, "user.json", `{
			"temperature": 0.8,
			"models": {"code": {"model": "qwen/qwen3-coder:free", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY", "temperature": 0.8}}
		}`)
		projPath := write(t, dir, "proj.json", `{
			"temperature": 0.3,
			"models": {"code": {"model": "openai/gpt-oss-120b:free"}}
		}`)
		cfg := loadMergedConfig(userPath, projPath)
		code := cfg.Models["code"]
		if code.Model != "openai/gpt-oss-120b:free" {
			t.Errorf("model = %q, want project override", code.Model)
		}
		if code.Endpoint != "https://openrouter.ai/api/v1" || code.KeyEnv != "OPENROUTER_API_KEY" {
			t.Errorf("endpoint/key_env should inherit from user: %+v", code)
		}
		if cfg.Temperature == nil || *cfg.Temperature != projectTemp {
			t.Errorf("top-level temperature = %v, want project override %v", cfg.Temperature, projectTemp)
		}
		if code.Temperature == nil || *code.Temperature != userTemp {
			t.Errorf("role temperature = %v, want inherited role value %v", code.Temperature, userTemp)
		}
	})

	t.Run("only one layer present", func(t *testing.T) {
		dir := t.TempDir()
		userPath := write(t, dir, "user.json", `{"backend": {"type": "openrouter"}}`)
		if cfg := loadMergedConfig(userPath, filepath.Join(dir, "missing.json")); cfg == nil || cfg.Backend.Type != "openrouter" {
			t.Errorf("user-only load failed: %+v", cfg)
		}
		projPath := write(t, dir, "proj.json", `{"backend": {"type": "litellm"}}`)
		if cfg := loadMergedConfig(filepath.Join(dir, "missing.json"), projPath); cfg == nil || cfg.Backend.Type != "litellm" {
			t.Errorf("project-only load failed: %+v", cfg)
		}
	})

	t.Run("neither present returns nil", func(t *testing.T) {
		if cfg := loadMergedConfig("", ""); cfg != nil {
			t.Errorf("want nil when no layer exists, got %+v", cfg)
		}
	})

	t.Run("malformed layer degrades to absent", func(t *testing.T) {
		dir := t.TempDir()
		bad := write(t, dir, "bad.json", `{not json`)
		good := write(t, dir, "good.json", `{"backend": {"type": "openrouter"}}`)
		// Bad user layer, good project layer → project alone survives.
		if cfg := loadMergedConfig(bad, good); cfg == nil || cfg.Backend.Type != "openrouter" {
			t.Errorf("malformed user layer should be ignored: %+v", cfg)
		}
	})
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it, so a test can assert on warnUnknownRoles' (or
// any other) stderr message without letting it leak into `go test`'s own
// output.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	return buf.String()
}

// E1 back-compat (docs/completion-roadmap.md): an old config.json with a
// role key removed from the configurable surface (hard-code/reason/fast/
// rerank/tools were audited dead and dropped) must still load without
// error — the key sits inert in cfg.Models, unreachable from
// resolveBinding/selectModel (both keyed off rolePolicies) — and produces
// exactly one stderr warning naming it.
func TestLoadMergedConfigUnknownRoleIgnoredWithWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"backend": {"type": "openrouter"},
		"models": {
			"code": {"model": "qwen/qwen3-coder:free"},
			"hard-code": {"model": "old-hard-code-model"},
			"rerank": {"model": "old-rerank-model"}
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var cfg *Config
	stderr := captureStderr(t, func() {
		cfg = loadMergedConfig(path, "")
	})

	if cfg == nil {
		t.Fatal("expected a non-nil config — unknown role keys must not error")
	}
	if cfg.Models["code"].Model != "qwen/qwen3-coder:free" {
		t.Errorf("code model = %q, want the known role to still resolve", cfg.Models["code"].Model)
	}
	if !strings.Contains(stderr, "hard-code") || !strings.Contains(stderr, "rerank") {
		t.Errorf("stderr warning = %q, want it to name both unknown roles", stderr)
	}
	if n := strings.Count(strings.TrimRight(stderr, "\n"), "\n"); n != 0 {
		t.Errorf("stderr = %q, want exactly one warning line", stderr)
	}
	// The unknown role is never visited by resolveBinding/selectModel:
	// nothing in rolePolicies matches it, so fleet auto-selection can't
	// accidentally pick it.
	if got := selectModel(testFleet, "hard-code"); got != "" {
		t.Errorf("selectModel(hard-code) = %q, want empty — the role is unknown", got)
	}
}

// A config with only known roles produces no warning at all — the warning
// is strictly for the back-compat path, not a default nag.
func TestLoadMergedConfigKnownRolesOnlyProducesNoWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"models": {"code": {"model": "x"}, "study": {"model": "y"}, "embed": {"model": "z"}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		loadMergedConfig(path, "")
	})
	if stderr != "" {
		t.Errorf("stderr = %q, want no warning for an all-known-role config", stderr)
	}
}

// A realistic /model/info payload (trimmed to the fields we read, plus extra
// keys to prove we ignore them) for discovery tests.
const fleetInfoJSON = `{"data":[
  {"model_name":"coder","litellm_params":{"model":"openai/coder"},"model_info":{"max_input_tokens":131072,"role":"coder","silicon":"igpu","thinking":true,"swap_group":"igpu-8080","always_warm":false,"experimental":false,"input_cost_per_token":0}},
  {"model_name":"reasoner-npu","model_info":{"max_input_tokens":32768,"role":"reasoner","silicon":"npu","thinking":true,"swap_group":null,"always_warm":true}},
  {"model_name":"reranker","model_info":{"max_input_tokens":8192,"role":"reranker","silicon":"cpu","thinking":null}},
  {"model_name":"or-levels","model_info":{"max_input_tokens":8192,"role":"reasoner","thinking_mode":"levels"}}
]}`

// TestModelInfoThinkingMode covers the fleet's optional thinking_mode
// descriptor (docs/thinking-models.md §2): parsed verbatim when the fleet
// sends one, derived from the legacy bool (true→hybrid, false→none) when it
// doesn't.
func TestModelInfoThinkingMode(t *testing.T) {
	tests := []struct {
		name string
		info ModelInfo
		want string
	}{
		{"explicit levels", ModelInfo{ThinkingMode: "levels"}, "levels"},
		{"legacy true derives hybrid", ModelInfo{Thinking: true}, "hybrid"},
		{"legacy false derives none", ModelInfo{Thinking: false}, "none"},
		{"explicit wins over legacy bool", ModelInfo{Thinking: true, ThinkingMode: "always"}, "always"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.thinkingMode(); got != tt.want {
				t.Errorf("thinkingMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func fleetServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" {
			t.Errorf("discovery hit %q, want /model/info", r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverFleet(t *testing.T) {
	t.Run("parses model_info, ignores extra keys", func(t *testing.T) {
		srv := fleetServer(t, 200, fleetInfoJSON)
		f := discoverFleet(context.Background(), srv.URL)
		if f == nil {
			t.Fatal("expected a fleet, got nil")
		}
		coder, ok := f["coder"]
		if !ok {
			t.Fatal("coder missing from fleet")
		}
		if coder.MaxInput != 131072 || coder.Role != "coder" || coder.Silicon != "igpu" || !coder.Thinking || coder.SwapGroup != "igpu-8080" {
			t.Errorf("coder = %+v", coder)
		}
		if npu := f["reasoner-npu"]; npu.MaxInput != 32768 || npu.SwapGroup != "" || !npu.AlwaysWarm {
			t.Errorf("reasoner-npu = %+v", npu)
		}
		if rr := f["reranker"]; rr.MaxInput != 8192 || rr.Thinking {
			t.Errorf("reranker = %+v", rr)
		}
		if lv := f["or-levels"]; lv.ThinkingMode != "levels" {
			t.Errorf("or-levels ThinkingMode = %q, want levels", lv.ThinkingMode)
		}
	})

	t.Run("best-effort: nil on non-200, bad JSON, empty", func(t *testing.T) {
		for _, c := range []struct {
			name, body string
			status     int
		}{
			{"500", "{}", 500},
			{"bad json", "not json", 200},
			{"empty data", `{"data":[]}`, 200},
		} {
			t.Run(c.name, func(t *testing.T) {
				srv := fleetServer(t, c.status, c.body)
				if f := discoverFleet(context.Background(), srv.URL); f != nil {
					t.Errorf("want nil fleet, got %+v", f)
				}
			})
		}
	})

	t.Run("nil on unreachable backend", func(t *testing.T) {
		if f := discoverFleet(context.Background(), "http://127.0.0.1:1"); f != nil {
			t.Errorf("want nil for unreachable, got %+v", f)
		}
	})
}

// No backend address lives in source: the endpoint resolves config > env >
// neutral localhost, and every role inherits it unless pinned.
func TestBackendEndpoint(t *testing.T) {
	t.Run("neutral localhost fallback, no env", func(t *testing.T) {
		t.Setenv("CORTEX_BACKEND", "")
		var c *Config
		if got := c.backendEndpoint(); got != defaultEndpoint {
			t.Errorf("nil config = %q, want %q", got, defaultEndpoint)
		}
		// Source carries no address: a binding resolved with no config/env/fleet
		// falls back to the neutral localhost only.
		if b := (&Config{}).resolveBinding(roleCode, nil); b.Endpoint != defaultEndpoint {
			t.Errorf("resolved endpoint = %q, want neutral %q", b.Endpoint, defaultEndpoint)
		}
	})
	t.Run("env overrides the fallback", func(t *testing.T) {
		t.Setenv("CORTEX_BACKEND", "http://env-host:4000")
		var c *Config
		if got := c.backendEndpoint(); got != "http://env-host:4000" {
			t.Errorf("env = %q, want http://env-host:4000", got)
		}
	})
	t.Run("config wins over env, and every role inherits it", func(t *testing.T) {
		t.Setenv("CORTEX_BACKEND", "http://env-host:4000")
		c := &Config{Backend: Backend{Endpoint: "http://cfg-host:4000", KeyService: "cortex-openrouter"}}
		if got := c.backendEndpoint(); got != "http://cfg-host:4000" {
			t.Errorf("config = %q, want http://cfg-host:4000", got)
		}
		for _, role := range []string{roleCode, roleStudy, roleEmbed} {
			s := c.resolveBinding(role, testFleet)
			if s.Endpoint != "http://cfg-host:4000" {
				t.Errorf("%s endpoint = %q, want backend address", role, s.Endpoint)
			}
			if s.KeyService != "cortex-openrouter" {
				t.Errorf("%s should inherit backend key_service, got %q", role, s.KeyService)
			}
		}
	})
	t.Run("a role may pin its own endpoint", func(t *testing.T) {
		c := &Config{
			Backend: Backend{Endpoint: "http://cfg-host:4000"},
			Models:  map[string]ModelSpec{roleEmbed: {Endpoint: "http://embed-host:8081"}},
		}
		if s := c.resolveBinding(roleEmbed, testFleet); s.Endpoint != "http://embed-host:8081" {
			t.Errorf("pinned endpoint = %q, want http://embed-host:8081", s.Endpoint)
		}
	})
}

func TestApplyFleet(t *testing.T) {
	fleet := Fleet{
		"coder":     {MaxInput: 131072, Thinking: true},       // hybrid
		"qwen3-4b":  {MaxInput: 131072, Thinking: false},      // none
		"or-model":  {MaxInput: 8192, ThinkingMode: "levels"}, // explicit thinking_mode
		"always-on": {MaxInput: 8192, ThinkingMode: "always"}, // can't stop reasoning
	}
	t.Run("fills an unset window from discovery", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder"}, fleet)
		if got.Window != 131072 {
			t.Errorf("window = %d, want 131072", got.Window)
		}
	})
	t.Run("leaves a config-pinned window intact", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder", Window: 8000}, fleet)
		if got.Window != 8000 {
			t.Errorf("window = %d, want pinned 8000", got.Window)
		}
	})
	t.Run("keeps enable_thinking for a hybrid model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder", Thinking: llm.Effort{Level: llm.EffortOff}}, fleet)
		if got.Thinking.Level != llm.EffortOff {
			t.Errorf("thinking spec should survive for a hybrid model, got %+v", got.Thinking)
		}
	})
	t.Run("drops enable_thinking for a non-thinking model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "qwen3-4b", Thinking: llm.Effort{Level: llm.EffortOff}}, fleet)
		if !got.Thinking.IsZero() {
			t.Errorf("non-thinking model should not carry the kwarg, got %+v", got.Thinking)
		}
	})
	t.Run("a level degrades to on for a hybrid model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder", Thinking: llm.Effort{Level: llm.EffortHigh}}, fleet)
		if got.Thinking.Level != llm.EffortOn {
			t.Errorf("thinking = %+v, want degraded to on (hybrid has no real levels)", got.Thinking)
		}
	})
	t.Run("a level survives for a levels-capable model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "or-model", Thinking: llm.Effort{Level: llm.EffortHigh}}, fleet)
		if got.Thinking.Level != llm.EffortHigh {
			t.Errorf("thinking = %+v, want high (unchanged)", got.Thinking)
		}
	})
	t.Run("off degrades to on for an always-reasoning model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "always-on", Thinking: llm.Effort{Level: llm.EffortOff}}, fleet)
		if got.Thinking.Level != llm.EffortOn {
			t.Errorf("thinking = %+v, want degraded to on (can't stop reasoning)", got.Thinking)
		}
	})
	t.Run("unknown model and nil fleet pass through untouched", func(t *testing.T) {
		in := ModelSpec{Model: "mystery", Window: 4096, Thinking: llm.Effort{Level: llm.EffortOff}}
		if got := applyFleet(in, fleet); got != in {
			t.Errorf("unknown model mutated: %+v", got)
		}
		if got := applyFleet(in, nil); got != in {
			t.Errorf("nil fleet mutated: %+v", got)
		}
	})
}

func TestSharedSwapGroup(t *testing.T) {
	fleet := Fleet{
		"coder":        {SwapGroup: "igpu-8080"},
		"reasoner":     {SwapGroup: "igpu-8080"},
		"reasoner-npu": {SwapGroup: ""},
	}
	spec := func(m string) ModelSpec { return ModelSpec{Model: m} }
	t.Run("flags two different models in the same group", func(t *testing.T) {
		if g := sharedSwapGroup(fleet, spec("coder"), spec("reasoner")); g != "igpu-8080" {
			t.Errorf("want igpu-8080, got %q", g)
		}
	})
	t.Run("no conflict across silicon (swap-free study)", func(t *testing.T) {
		if g := sharedSwapGroup(fleet, spec("coder"), spec("reasoner-npu")); g != "" {
			t.Errorf("want no conflict, got %q", g)
		}
	})
	t.Run("same model is not a conflict, nil fleet is safe", func(t *testing.T) {
		if g := sharedSwapGroup(fleet, spec("coder"), spec("coder")); g != "" {
			t.Errorf("same model should not conflict, got %q", g)
		}
		if g := sharedSwapGroup(nil, spec("coder"), spec("reasoner")); g != "" {
			t.Errorf("nil fleet should be safe, got %q", g)
		}
	})
}

// TemplateKwargs: off and on are both said affirmatively; levels degrade to
// the affirmative on (this dialect can't represent them); only unset defers
// to the model's template default (docs/thinking-models.md §2).
func TestTemplateKwargs(t *testing.T) {
	enabled := func(b bool) *bool { return &b }
	tests := []struct {
		name     string
		thinking llm.Effort
		want     *bool // nil: no kwargs; else the expected enable_thinking
	}{
		{"unset defers to template default", llm.Effort{}, nil},
		{"on emits enable_thinking=true", llm.Effort{Level: llm.EffortOn}, enabled(true)},
		{"off emits enable_thinking=false", llm.Effort{Level: llm.EffortOff}, enabled(false)},
		{"high degrades to affirmative on", llm.Effort{Level: llm.EffortHigh}, enabled(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := ModelSpec{Thinking: tt.thinking}.TemplateKwargs()
			if tt.want == nil {
				if kw != nil {
					t.Errorf("TemplateKwargs() = %v, want nil", kw)
				}
				return
			}
			if v, ok := kw["enable_thinking"].(bool); !ok || v != *tt.want {
				t.Errorf("TemplateKwargs() = %v, want enable_thinking=%v", kw, *tt.want)
			}
		})
	}
}

// TestModelSpecReasoning covers the OpenRouter dialect counterpart to
// TemplateKwargs.
func TestModelSpecReasoning(t *testing.T) {
	tests := []struct {
		name     string
		thinking llm.Effort
		want     *llm.Reasoning
	}{
		{"unset: nil", llm.Effort{}, nil},
		{"on: enabled true (affirmative)", llm.Effort{Level: llm.EffortOn}, &llm.Reasoning{}},
		{"off: enabled false", llm.Effort{Level: llm.EffortOff}, &llm.Reasoning{}},
		{"high: effort high", llm.Effort{Level: llm.EffortHigh}, &llm.Reasoning{Effort: "high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ModelSpec{Thinking: tt.thinking}.Reasoning()
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("Reasoning() = %+v, want %+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if got.Effort != tt.want.Effort {
				t.Errorf("Reasoning().Effort = %q, want %q", got.Effort, tt.want.Effort)
			}
		})
	}
}

// thinkingLabel is the eval-telemetry attribution derived from a built
// kwargs map (ModelSpec.TemplateKwargs' output) — "off" only when
// enable_thinking is explicitly false, "on" for every other shape (nil, an
// unrelated kwargs map, or enable_thinking=true).
func TestThinkingLabel(t *testing.T) {
	tests := []struct {
		name   string
		kwargs map[string]any
		want   string
	}{
		{"nil kwargs: on", nil, "on"},
		{"enable_thinking=false: off", map[string]any{"enable_thinking": false}, "off"},
		{"enable_thinking=true: on", map[string]any{"enable_thinking": true}, "on"},
		{"unrelated kwargs: on", map[string]any{"some_other_key": "x"}, "on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := thinkingLabel(tt.kwargs); got != tt.want {
				t.Errorf("thinkingLabel(%v) = %q, want %q", tt.kwargs, got, tt.want)
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
	if got := def.windowSize(); got != fallbackWindow {
		t.Errorf("windowSize() = %d, want fallback %d", got, fallbackWindow)
	}
	sized := &CortexSession{Window: 8192}
	if got := sized.windowSize(); got != 8192 {
		t.Errorf("windowSize() = %d, want 8192", got)
	}
}

func TestSessionPrompt(t *testing.T) {
	sess := &CortexSession{Request: CortexArgs{}.Request(), LastPromptTokens: 8200}
	got := sess.Prompt()

	for _, want := range []string{"cortex " + Version, ModelCoder, "8.2k/32.8k", promptGlyph} {
		if !strings.Contains(got, want) {
			t.Errorf("Prompt() = %q, missing %q", got, want)
		}
	}

	// The prompt is redrawn on every keystroke with only \r\033[K, which cannot
	// erase an embedded newline — a \n here walks the line down one row per byte
	// typed. The inter-turn blank line is the REPL loop's job, not Prompt()'s.
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Prompt() must be a single line, got %q", got)
	}
}

func TestSetModel(t *testing.T) {
	s := &CortexSession{Request: &AgentRequest{Model: "coder", BaseURL: "http://backend.example:4000"}}
	s.SetModel("reasoner")
	if s.Request.Model != "reasoner" {
		t.Errorf("model = %q, want reasoner", s.Request.Model)
	}
	if s.Request.BaseURL != "http://backend.example:4000" {
		t.Errorf("endpoint should be unchanged on a model swap, got %q", s.Request.BaseURL)
	}
}

// TestSetModelReResolvesEffortAndWindow covers P3's seam fix
// (docs/thinking-models.md known seam bug #1): SetModel used to swap only
// the model name, leaving effort wire fields and the window stale from the
// OLD binding. It must now re-derive both for the NEW model via the
// discovered Fleet, and clear to neutral when the fleet doesn't know it.
func TestSetModelReResolvesEffortAndWindow(t *testing.T) {
	fleet := Fleet{
		"hybrid-model": {MaxInput: 65536, Thinking: true},  // hybrid
		"plain-model":  {MaxInput: 16384, Thinking: false}, // none
	}
	t.Run("switching to a fleet-known hybrid model re-derives the window", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		s.SetModel("hybrid-model")
		if s.Window != 65536 {
			t.Errorf("Window = %d, want 65536 (from the fleet)", s.Window)
		}
	})
	t.Run("prior explicit off degrades to unset for a non-thinking model", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		s.SetModel("plain-model")
		if !s.Request.Effort.IsZero() {
			t.Errorf("Effort = %+v, want unset (plain-model can't honor any ask)", s.Request.Effort)
		}
		if s.Request.ChatTemplateKwargs != nil {
			t.Errorf("ChatTemplateKwargs = %v, want nil", s.Request.ChatTemplateKwargs)
		}
	})
	t.Run("prior effort survives and stays on for a hybrid model", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		s.SetModel("hybrid-model")
		if s.Request.Effort.Level != llm.EffortOn {
			t.Errorf("Effort = %+v, want on", s.Request.Effort)
		}
	})
	t.Run("fleet nil clears effort to neutral and window to fallback", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		s.SetModel("anything")
		if !s.Request.Effort.IsZero() {
			t.Errorf("Effort = %+v, want unset (fleet unknown)", s.Request.Effort)
		}
		if s.Request.ChatTemplateKwargs != nil {
			t.Errorf("ChatTemplateKwargs = %v, want nil", s.Request.ChatTemplateKwargs)
		}
		if s.Window != 0 {
			t.Errorf("Window = %d, want 0 (falls back via windowSize())", s.Window)
		}
	})
	t.Run("fleet known but model absent clears effort to neutral", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		s.SetModel("mystery-model")
		if !s.Request.Effort.IsZero() {
			t.Errorf("Effort = %+v, want unset (model not in fleet)", s.Request.Effort)
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

func TestParseCtxSize(t *testing.T) {
	msg := "litellm.BadRequestError: request (41193 tokens) exceeds the available context size (32768 tokens)"
	if got := parseCtxSize(msg); got != 32768 {
		t.Errorf("parseCtxSize = %d, want 32768", got)
	}
	if got := parseCtxSize("no numbers here"); got != 0 {
		t.Errorf("parseCtxSize(no match) = %d, want 0", got)
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

// TestSendPerturbsTemperatureOnRetry locks the peg-500 escape: the first attempt
// goes at temperature 0 (deterministic); a retry after a 5xx bumps the temperature
// so the model can escape a deterministic generation the proxy can't parse.
func TestSendPerturbsTemperatureOnRetry(t *testing.T) {
	quickRetries(t)
	var temps []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Temperature float64 `json:"temperature"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		temps = append(temps, body.Temperature)
		if len(temps) < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(okResponse))
	}))
	defer srv.Close()

	if _, err := (&AgentRequest{Model: "m", BaseURL: srv.URL, Temperature: defaultTemperature}).Send(context.Background()); err != nil {
		t.Fatalf("expected success on the perturbed retry, got %v", err)
	}
	if len(temps) < 2 {
		t.Fatalf("want at least 2 attempts, got %d", len(temps))
	}
	if temps[0] != defaultTemperature {
		t.Errorf("first attempt temp = %v, want default %v", temps[0], defaultTemperature)
	}
	if temps[1] <= temps[0] {
		t.Errorf("retry temp = %v, want > first attempt %v (perturbed to escape the 500)", temps[1], temps[0])
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

// TestSendStreamHonorsContextCancel proves that SendStream respects context
// cancellation during an in-flight SSE stream. This is the streaming path
// that was missing cancellation support.
func TestSendStreamHonorsContextCancel(t *testing.T) {
	quickRetries(t)
	// Track when client disconnects
	clientDisconnected := make(chan struct{}, 1)
	// Server that sends one SSE chunk then waits to see if client disconnects
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		// Wait to see if client disconnects
		select {
		case <-clientDisconnected:
			t.Logf("Server: client disconnected")
		case <-time.After(2 * time.Second):
			t.Logf("Server: no disconnect within 2s")
		}
	}))
	defer func() {
		select {
		case clientDisconnected <- struct{}{}:
		default:
		}
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	t.Logf("Starting SendStream with %v timeout", 50*time.Millisecond)
	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).SendStream(ctx, nil, nil)
	elapsed := time.Since(start)
	t.Logf("SendStream returned after %v with err=%v", elapsed, err)

	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("SendStream took %v after cancel; should return promptly", elapsed)
	}
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

// TestCodeWindowLearnedFromOverflow covers C2: the code role self-calibrates
// on a context-overflow error the same way study already did via
// studyWindow(). The first fixture is pkg/llm/context_overflow_test.go's
// "lemonade wrapped llama-server" case verbatim (the wire shape pkg/llm
// parses into a typed ContextOverflowError; cmd/cortex's parseCtxSize
// regex-matches the same text out of err.Error() for the REPL/discord
// recovery paths, per TestParseCtxSize above).
func TestCodeWindowLearnedFromOverflow(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		configured int
		overflow   string // server message carrying the real limit
		wantReal   int
	}{
		{
			name:       "lemonade wrapped llama-server",
			model:      "coder-a",
			configured: 131072,
			overflow:   "litellm.BadRequestError: request (41193 tokens) exceeds the available context size (16384 tokens)",
			wantReal:   16384,
		},
		{
			name:       "openrouter-shaped message",
			model:      "coder-b",
			configured: 8192,
			overflow:   "local-gw (400): server error: llama-server request failed: request (5012 tokens) exceeds the available context size (4096 tokens)",
			wantReal:   4096,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer delete(learnedWindows, tc.model)
			cs := &CortexSession{Request: &AgentRequest{Model: tc.model}, Window: tc.configured}

			// (b, baseline) configured wins before anything overflowed.
			if got := cs.windowSize(); got != tc.configured {
				t.Fatalf("windowSize() before overflow = %d, want configured %d", got, tc.configured)
			}

			real := parseCtxSize(tc.overflow)
			if real != tc.wantReal {
				t.Fatalf("parseCtxSize(%q) = %d, want %d", tc.overflow, real, tc.wantReal)
			}
			cs.learnWindow(real)

			// (a) learnedWindows records it, keyed by the code model.
			if got, ok := learnedWindows[tc.model]; !ok || got != tc.wantReal {
				t.Errorf("learnedWindows[%q] = %d,%v, want %d,true", tc.model, got, ok, tc.wantReal)
			}

			// (b) subsequent window resolution for sizing prefers the learned
			// value over the originally configured one.
			if got := cs.windowSize(); got != tc.wantReal {
				t.Errorf("windowSize() after overflow = %d, want learned %d", got, tc.wantReal)
			}
		})
	}
}

// TestStudyWindowUnaffectedByCodeOverflow covers (c): learning the code
// model's window from an overflow must not perturb study's own resolution,
// and vice versa — learnedWindows is a single map shared by both roles, but
// keyed per-model, so the two precedence chains (windowSize() for code,
// studyWindow() for study) only interact when the roles are bound to the
// literal same model name.
func TestStudyWindowUnaffectedByCodeOverflow(t *testing.T) {
	defer func() {
		delete(learnedWindows, "coder-model")
		delete(learnedWindows, "study-model")
	}()
	cs := &CortexSession{
		Request: &AgentRequest{Model: "coder-model"},
		Window:  131072,
		Study:   ModelSpec{Model: "study-model", Window: 32768},
	}
	if got := cs.studyWindow(); got != 32768 {
		t.Fatalf("studyWindow() before = %d, want configured 32768", got)
	}
	cs.learnWindow(16384) // simulate a coder-path overflow learn
	if got := cs.studyWindow(); got != 32768 {
		t.Errorf("studyWindow() after code-model learn = %d, want unaffected 32768", got)
	}
	// And the reverse: learning study's own window doesn't perturb the code
	// model's already-learned resolution.
	learnedWindows["study-model"] = 4096
	if got := cs.windowSize(); got != 16384 {
		t.Errorf("windowSize() after study-model learn = %d, want unaffected 16384 (code's own learned value)", got)
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
	cs.Close() // release the lock so a second session can resume the file
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
	cs.Close() // hand the lock to the resuming session
	if err := resumed.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.transcript.Close()
	resumed.Append(Message{Role: RoleUser, Content: "second life"})

	cs2 := &CortexSession{Request: CortexArgs{}.Request()}
	resumed.Close() // and to the third
	if err := cs2.ResumeTranscript(""); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	defer cs2.transcript.Close()
	last := cs2.Request.Messages[len(cs2.Request.Messages)-1]
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

// stubCompactSummarize replaces the compaction summarizer call (no model, no
// network) for the duration of a test, recording the content and window it was
// given. compressed=true marks a real compaction; false → nothing to compact.
func stubCompactSummarize(t *testing.T, digest string, compressed bool, err error) (gotContent *string, gotWindow *int) {
	t.Helper()
	saved := compactSummarize
	t.Cleanup(func() { compactSummarize = saved })
	gotContent, gotWindow = new(string), new(int)
	compactSummarize = func(_ context.Context, _ *CortexSession, content string, window int) (string, bool, error) {
		*gotContent, *gotWindow = content, window
		return digest, compressed, err
	}
	return gotContent, gotWindow
}

func appendTestTurn(cs *CortexSession, ordinal int, user, assistant string) {
	cs.turnNo = ordinal
	start := len(cs.Request.Messages)
	cs.Append(Message{Role: RoleUser, Content: user})
	cs.Append(Message{Role: "assistant", Content: assistant})
	cs.ws.AddTurn(cache.TurnSpan{Start: start, End: len(cs.Request.Messages), Tokens: estTurnTokens(cs.Request.Messages[start:])})
	cs.turns = ordinal
	cs.turnNo = 0
}

func TestCompactRebuildsHistory(t *testing.T) {
	gotContent, gotWindow := stubCompactSummarize(t,
		"user is hardening the loop; edited cmd/cortex/main.go; tests pass", true, nil)

	cs := newTestSession(t)
	cs.Window = 64000
	cs.Study.Window = 32768
	cs.LastPromptTokens = 60000
	cs.ws = cs.newWorkingSet(1)
	appendTestTurn(cs, 1, "long conversation", "lots of work")
	appendTestTurn(cs, 2, "newest task context", "current answer")
	oldID := cs.SessionID
	sys := cs.Request.Messages[0]

	if err := cs.Compact(context.Background()); err != nil {
		t.Fatalf("compact: %v", err)
	}
	defer cs.transcript.Close()

	// Only the eligible old prefix was summarized; the newest complete turn
	// remains verbatim.
	if !strings.Contains(*gotContent, "long conversation") || strings.Contains(*gotContent, "newest task context") {
		t.Errorf("summarized content = %q, want only the old completed prefix", *gotContent)
	}
	if *gotWindow != 16000 {
		t.Errorf("study window = %d, want 16000 (codeWindow/4)", *gotWindow)
	}

	// History = original system seed + one state digest + newest raw turn.
	msgs := cs.Request.Messages
	if len(msgs) != 4 {
		t.Fatalf("compacted history has %d messages, want 4", len(msgs))
	}
	if msgs[0].Content != sys.Content || msgs[0].Role != RoleSystem {
		t.Error("system seed should survive compaction unchanged")
	}
	if msgs[1].Role != RoleUser || !strings.Contains(msgs[1].Content, "hardening the loop") {
		t.Errorf("digest message = %+v", msgs[1])
	}

	if msgs[2].Content != "newest task context" || msgs[3].Content != "current answer" {
		t.Errorf("newest turn was not retained verbatim: %+v", msgs[2:])
	}

	// Gauge now reflects the retained state instead of pretending context is empty.
	if cs.LastPromptTokens == 0 {
		t.Error("LastPromptTokens should reflect compacted state")
	}
	if cs.SessionID == oldID {
		t.Error("compaction should start a NEW session id")
	}
	if _, err := os.Stat(filepath.Join(sessionsDir(), oldID+".jsonl")); err != nil {
		t.Errorf("raw transcript should stay on disk: %v", err)
	}

	// The new transcript must resume to exactly the compacted state.
	cs.Close() // release the lock before reopening the same file
	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(cs.SessionID); err != nil {
		t.Fatalf("resume after compact: %v", err)
	}
	defer resumed.transcript.Close()
	if len(resumed.Request.Messages) != 4 || !strings.Contains(resumed.Request.Messages[1].Content, "hardening the loop") || resumed.Request.Messages[2].Content != "newest task context" {
		t.Errorf("resume should restore digest plus newest raw turn, got %d messages", len(resumed.Request.Messages))
	}
}

func TestCompactFoldsExistingStateLayer(t *testing.T) {
	gotContent, _ := stubCompactSummarize(t, "updated state", true, nil)
	cs := newTestSession(t)
	cs.Request.Messages = append(cs.Request.Messages, Message{Role: RoleUser, Content: "[Session state]\nprior-decision-marker"})
	cs.writeTranscript(cs.Request.Messages[1])
	cs.ws = cs.newWorkingSet(2)
	appendTestTurn(cs, 1, "older raw turn", "older answer")
	appendTestTurn(cs, 2, "newest raw turn", "newest answer")

	if err := cs.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*gotContent, "prior-decision-marker") || !strings.Contains(*gotContent, "older raw turn") {
		t.Fatalf("compaction input lost layered state: %q", *gotContent)
	}
	if strings.Contains(*gotContent, "newest raw turn") {
		t.Fatalf("compaction input included newest turn: %q", *gotContent)
	}
}

func TestCompactErrors(t *testing.T) {
	t.Run("unpersisted session", func(t *testing.T) {
		cs := &CortexSession{Request: CortexArgs{}.Request()}
		if err := cs.Compact(context.Background()); err == nil {
			t.Fatal("expected error for unpersisted session")
		}
	})

	t.Run("uncompressed is refused — nothing to compress", func(t *testing.T) {
		stubCompactSummarize(t, "", false, nil) // fit a single chunk → nothing to compact
		cs := newTestSession(t)
		cs.ws = cs.newWorkingSet(1)
		appendTestTurn(cs, 1, "old "+strings.Repeat("x", 3000), "old answer")
		appendTestTurn(cs, 2, "current", "current answer")
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
		stubCompactSummarize(t, "  ", true, nil) // compressed but empty
		cs := newTestSession(t)
		cs.ws = cs.newWorkingSet(1)
		appendTestTurn(cs, 1, "old "+strings.Repeat("x", 3000), "old answer")
		appendTestTurn(cs, 2, "current", "current answer")
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
// Shell syntax (pipes, redirects, chaining) now runs via `bash -c` when the
// risk gate permits it — the old "not supported" rejection is gone. The gate,
// not the tokenizer, is what governs whether a command runs.
func TestBashShellSyntax(t *testing.T) {
	stubSafe := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test: safe", nil
	}
	stubRisky := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Risky, "test: risky", nil
	}

	t.Run("pipe runs when the gate allows", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubSafe}
		args, _ := json.Marshal(map[string]string{"command": "echo hello | tr a-z A-Z"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "HELLO") {
			t.Errorf("pipe did not run through bash -c: %q", got)
		}
	})

	t.Run("chaining runs when the gate allows", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubSafe}
		args, _ := json.Marshal(map[string]string{"command": "echo a && echo b"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
			t.Errorf("chained command did not run: %q", got)
		}
	})

	t.Run("deny-floor blocks even when the classifier says safe", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cs := &CortexSession{classifyShell: stubSafe}
		args, _ := json.Marshal(map[string]string{"command": "echo x > /etc/cortex-should-never-write"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(strings.ToLower(got), "refused") {
			t.Errorf("deny-floor should refuse the redirect, got %q", got)
		}
	})

	t.Run("risky command runs after interactive yes", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool { return true }}
		args, _ := json.Marshal(map[string]string{"command": "echo confirmed | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "confirmed") {
			t.Errorf("approved risky command did not run: %q", got)
		}
	})

	t.Run("risky command refused after interactive no", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool { return false }}
		args, _ := json.Marshal(map[string]string{"command": "echo nope | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "nope") {
			t.Errorf("declined command should not have run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "declined") {
			t.Errorf("expected a declined message, got %q", got)
		}
	})

	t.Run("risky command blocked when headless (no approver)", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			confirmRisky: func(string) bool { return true }} // present but ignored when quiet
		args, _ := json.Marshal(map[string]string{"command": "echo headless | cat"})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "headless\n") {
			t.Errorf("headless risky command should not run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "block") {
			t.Errorf("expected a blocked message when headless, got %q", got)
		}
	})

	// M4.2: a subagent (e.g. the `agent` profile) has no human operator
	// mid-loop — Risky must fall straight to the headless-blocked shape, never
	// the interactive confirm prompt, regardless of confirmRisky/quiet.
	t.Run("risky command blocked inside a subagent regardless of confirmRisky", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool {
			t.Fatal("confirmRisky must not be invoked for a subagent-depth call")
			return true
		}}
		ctx := withSubagentDepth(context.Background(), 1)
		args, _ := json.Marshal(map[string]string{"command": "echo nested | cat"})
		got, err := tools.Execute(ctx, tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "nested\n") {
			t.Errorf("subagent-depth risky command should not run: %q", got)
		}
		if !strings.Contains(strings.ToLower(got), "no interactive approval") {
			t.Errorf("expected the headless-blocked message, got %q", got)
		}
	})

	// Control: an explicit depth-0 context (the coder's own top-level call)
	// keeps the interactive confirm path unchanged.
	t.Run("risky command at depth 0 still uses interactive confirm", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, confirmRisky: func(string) bool { return true }}
		ctx := withSubagentDepth(context.Background(), 0)
		args, _ := json.Marshal(map[string]string{"command": "echo depth-zero | cat"})
		got, err := tools.Execute(ctx, tc(FunctionBash, string(args)), cs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "depth-zero") {
			t.Errorf("approved depth-0 risky command should have run: %q", got)
		}
	})
}

// Regression: a quoted grep pattern must actually match. Before the tokenizer
// fix, `grep -n "X" f` searched for the literal `"X"` (quotes included), found
// nothing, and the model looped on the identical command (2026-06-14).
func TestBashHonorsQuotedArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("func TestScroller(t *testing.T) {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{`grep -n Scroller f.txt`, `grep -n "Scroller" f.txt`} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", cmd, err)
		}
		if !strings.Contains(got, "Scroller") {
			t.Errorf("%q: got %q, want a line containing Scroller", cmd, got)
		}
	}
}

// grep's exit 1 means "no matches" — a content-free result, not a failure.
// It must read as such, not as a bare "[exit error: exit status 1]" the model
// can't distinguish from a broken command.
func TestBashGrepNoMatch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"command": `grep -n Absent f.txt`})
	got, err := tools.Execute(context.Background(), tc(FunctionBash, string(args)), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "(no matches)" {
		t.Errorf("got %q, want %q", got, "(no matches)")
	}
	if strings.Contains(got, "exit error") {
		t.Errorf("grep no-match should not surface as an exit error: %q", got)
	}
}

func TestAssembleStreamResponsePreservesCachedTokens(t *testing.T) {
	res := assembleStreamResponse(llm.StreamResult{Stats: llm.GenerationStats{
		InputTokens: 100, OutputTokens: 20, CachedInputTokens: 75,
	}})
	if got := res.Usage.CachedPromptTokens(); got != 75 {
		t.Fatalf("cached prompt tokens = %d, want 75", got)
	}
}

func TestWireMessagesComposesEphemerally(t *testing.T) {
	req := CortexArgs{}.Request() // system message only
	sys := req.Messages[0].Content
	req.Messages = append(req.Messages, Message{Role: RoleUser, Content: "add a field"})
	userOrig := req.Messages[1].Content

	t.Run("no ephemeral → everything unchanged", func(t *testing.T) {
		wire := req.wireMessages()
		if wire[0].Content != sys || wire[1].Content != userOrig {
			t.Error("without ephemeral, no message should change")
		}
	})

	t.Run("ephemeral occupies a separate slot after the stable prefix", func(t *testing.T) {
		req.EphemeralSystem = "# memory\n- [decision] use pgx"
		wire := req.wireMessages()

		if wire[0].Content != sys {
			t.Errorf("system message changed: got %q, want %q", wire[0].Content, sys)
		}
		if len(wire) != len(req.Messages)+1 {
			t.Fatalf("wire length = %d, want %d", len(wire), len(req.Messages)+1)
		}
		if wire[1].Role != RoleUser || wire[1].Content != req.EphemeralSystem {
			t.Errorf("wire[1] = %+v, want ephemeral user slot", wire[1])
		}
		if wire[2].Content != userOrig {
			t.Errorf("original user message moved incorrectly: %q", wire[2].Content)
		}
		if req.Messages[0].Content != sys || req.Messages[1].Content != userOrig {
			t.Error("stored messages must not be mutated by composition")
		}
	})

	t.Run("ephemeral slot remains fixed while the tool loop appends", func(t *testing.T) {
		req.Messages = append(req.Messages, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "1"}}})
		req.Messages = append(req.Messages, Message{Role: RoleTool, ToolCallID: "1", Content: "tool output"})
		req.EphemeralSystem = "ctx"
		wire := req.wireMessages()
		if wire[0].Content != sys || wire[1].Content != "ctx" || wire[2].Content != userOrig {
			t.Fatalf("wire prefix = %+v, want stable system, ephemeral slot, original user", wire[:3])
		}
		if wire[len(wire)-1].Content != "tool output" {
			t.Error("tool-loop append missing from wire tail")
		}
	})
}

func TestWireMessagesTwoZones(t *testing.T) {
	req := &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: RoleUser, Content: "u2"}, {Role: "assistant", Content: "a2"}}}

	t.Run("demoted turns are replaced by the outline", func(t *testing.T) {
		req.OutlineBlock = "OUTLINE"
		req.EphemeralSystem = "INDEX"
		req.TailFrom = 3

		wire := req.wireMessages()
		// Wire: system, outline, index slot, u2, a2.
		if len(wire) != 5 {
			t.Errorf("wire length = %d, want 5", len(wire))
		}
		if wire[0].Content != "sys" {
			t.Errorf("wire[0] = %q, want stable system", wire[0].Content)
		}
		if wire[1].Role != RoleUser || wire[1].Content != "OUTLINE" {
			t.Errorf("wire[1] = {%q,%q}, want {%q,%q}", wire[1].Role, wire[1].Content, RoleUser, "OUTLINE")
		}
		if wire[2].Content != "INDEX" || wire[3].Content != "u2" || wire[4].Content != "a2" {
			t.Errorf("wire suffix = %+v, want index, u2, a2", wire[2:])
		}
		// Stored Messages must not be mutated
		if len(req.Messages) != 5 || req.Messages[1].Content != "u1" {
			t.Errorf("stored Messages[1] = %q, want %q (not mutated)", req.Messages[1].Content, "u1")
		}
	})

	t.Run("tail-only demotion without outline still drops demoted messages", func(t *testing.T) {
		req.OutlineBlock = ""
		req.EphemeralSystem = ""
		req.TailFrom = 3

		wire := req.wireMessages()
		if len(wire) != 3 {
			t.Errorf("wire length = %d, want 3", len(wire))
		}
		if wire[0].Content != "sys" {
			t.Errorf("wire[0] = %q, want %q", wire[0].Content, "sys")
		}
		if wire[1].Content != "u2" {
			t.Errorf("wire[1] = %q, want %q", wire[1].Content, "u2")
		}
		if wire[2].Content != "a2" {
			t.Errorf("wire[2] = %q, want %q", wire[2].Content, "a2")
		}
	})

	t.Run("zero values are a no-op", func(t *testing.T) {
		req.OutlineBlock = ""
		req.EphemeralSystem = ""
		req.TailFrom = 0

		wire := req.wireMessages()
		if len(wire) != len(req.Messages) {
			t.Errorf("wire length = %d, want %d", len(wire), len(req.Messages))
		}
		if wire[1].Content != "u1" {
			t.Errorf("wire[1] = %q, want %q", wire[1].Content, "u1")
		}
	})
}

// applyPromptCache marks Anthropic cache breakpoints on the system message and
// the end of prior history, and only for anthropic/* models. The default
// (no-cache) message must marshal byte-identically so transcripts are untouched.
func TestPromptCache(t *testing.T) {
	mk := func() []Message {
		return []Message{
			{Role: RoleSystem, Content: "SYS"},
			{Role: RoleUser, Content: "first task"},
			{Role: "assistant", Content: "doing it"},
			{Role: RoleUser, Content: "follow up"}, // current turn (last user)
		}
	}
	cached := func(m Message) bool {
		b, _ := json.Marshal(&m) // pointer, as addressable wire-slice elements are
		return strings.Contains(string(b), "cache_control")
	}

	t.Run("default message marshals byte-identically (no cache_control)", func(t *testing.T) {
		b, _ := json.Marshal(Message{Role: RoleUser, Content: "hi"})
		if string(b) != `{"role":"user","content":"hi"}` {
			t.Errorf("default marshal changed: %s", b)
		}
	})

	t.Run("non-anthropic model is a no-op", func(t *testing.T) {
		msgs := mk()
		applyPromptCache(msgs, "z-ai/glm-4.6")
		for i, m := range msgs {
			if cached(m) {
				t.Errorf("message %d should not be cached for a non-anthropic model", i)
			}
		}
	})

	t.Run("anthropic marks system + end-of-prior-history, not the current turn", func(t *testing.T) {
		msgs := mk()
		applyPromptCache(msgs, "anthropic/claude-haiku-4.5")
		want := map[int]bool{0: true, 1: false, 2: true, 3: false} // sys + pre-current-user
		for i, m := range msgs {
			if cached(m) != want[i] {
				t.Errorf("message %d (role %s) cached=%v, want %v", i, m.Role, cached(m), want[i])
			}
		}
		// The cached system message must carry the structured content form.
		b, _ := json.Marshal(&msgs[0])
		if !strings.Contains(string(b), `"type":"ephemeral"`) || !strings.Contains(string(b), `"text":"SYS"`) {
			t.Errorf("cached message not in content-parts form: %s", b)
		}
		// The real wire path marshals the message SLICE inside the payload —
		// addressable elements must invoke the pointer marshaler there too.
		wire, _ := json.Marshal(struct {
			Messages []Message `json:"messages"`
		}{msgs})
		if got := strings.Count(string(wire), "cache_control"); got != 2 {
			t.Errorf("wire payload should carry 2 cache breakpoints, got %d: %s", got, wire)
		}
	})

	t.Run("first turn (no prior history) marks only the system message", func(t *testing.T) {
		msgs := []Message{{Role: RoleSystem, Content: "SYS"}, {Role: RoleUser, Content: "hi"}}
		applyPromptCache(msgs, "anthropic/claude-opus-4.8")
		if !cached(msgs[0]) || cached(msgs[1]) {
			t.Error("first turn should cache only the system message")
		}
	})
}

// core messages.
func TestLoadTranscriptBackCompat(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := sessionsDir()
	os.MkdirAll(dir, 0755)
	// Legacy line: {ts, role, content} with no "kind".
	legacy := `{"ts":"2026-01-01T00:00:00Z","role":"user","content":"legacy turn"}` + "\n"
	path := filepath.Join(dir, "20260101-000000.jsonl")
	os.WriteFile(path, []byte(legacy), 0644)

	msgs, turns, err := loadTranscript(path)
	if err != nil {
		t.Fatalf("loadTranscript: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "legacy turn" {
		t.Errorf("legacy (kind-less) entry should replay as a core message, got %+v", msgs)
	}
	if len(turns) != 1 || turns[0] != 0 {
		t.Errorf("legacy entry should have turn=0 stamp, got %v", turns)
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

// --- Session metrics (6a) --------------------------------------------------

func TestSessionSummary(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now().Add(-90 * time.Second)}
	cs.turns, cs.tokensIn, cs.tokensOut, cs.captures, cs.injections = 5, 52000, 8000, 9, 6
	s := cs.sessionSummary()
	for _, want := range []string{"5 turns", "52k in", "8k out", "9 captured", "6 memory injections"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
}

func TestTurnAccumulatesTokens(t *testing.T) {
	quickRetries(t)
	srv := httptest.NewServer(sseHandler(sseBody(
		`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
	)))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	if _, err := cs.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if cs.tokensIn != 12 || cs.tokensOut != 3 {
		t.Errorf("accumulated tokens = %d in / %d out, want 12/3", cs.tokensIn, cs.tokensOut)
	}
}

func TestTurnDemotesOldTurnsToOutline(t *testing.T) {
	quickRetries(t)
	var got [][]Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req.Messages)
		w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
		)))
	}))
	defer srv.Close()

	// Window 60 → demotion watermarks high=30/low=20 tokens (newWorkingSet).
	cs := &CortexSession{Window: 60, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}

	// Turn 1 is ~40 tokens (162 chars / 4): over the high watermark, but the
	// most-recent-turn invariant blocks demoting the only turn. Turn 2 doubles
	// the tail; at turn 3 start, turn 1 demotes (turn 2 stays: same invariant).
	for _, input := range []string{strings.Repeat("alpha ", 27), strings.Repeat("bravo ", 27), "charlie"} {
		if _, err := cs.Turn(context.Background(), input); err != nil {
			t.Fatalf("turn %q: %v", input[:5], err)
		}
	}

	if len(got) != 3 {
		t.Fatalf("server saw %d requests, want 3", len(got))
	}
	wire := got[2]
	if wire[0].Content != "s" {
		t.Errorf("wire[0] = %q, want the system message", wire[0].Content)
	}
	if wire[1].Role != RoleUser || !strings.HasPrefix(wire[1].Content, outlineHeader) {
		t.Errorf("wire[1] should be the outline zone, got role=%q content=%q", wire[1].Role, wire[1].Content)
	}
	if !strings.Contains(wire[1].Content, "alpha") || !strings.Contains(wire[1].Content, "t1 · user:") {
		t.Errorf("outline should carry the demoted turn 1 entry, got %q", wire[1].Content)
	}
	for i, m := range wire[2:] {
		if strings.Contains(m.Content, "alpha") {
			t.Errorf("wire[%d] still carries raw turn-1 content after demotion", i+2)
		}
	}
	hydrated := false
	for _, m := range wire[2:] {
		if strings.Contains(m.Content, "bravo") {
			hydrated = true
		}
	}
	if !hydrated {
		t.Error("turn 2 should still ride the wire verbatim")
	}
	kept := false
	for _, m := range cs.Request.Messages {
		if strings.Contains(m.Content, "alpha") {
			kept = true
		}
	}
	if !kept {
		t.Error("demotion must be wire-only: the stored log keeps turn 1 verbatim")
	}
	if cs.Request.TailFrom <= 1 {
		t.Errorf("TailFrom = %d, want > 1 after demotion", cs.Request.TailFrom)
	}
}

// The inner loop must break when the model re-issues the byte-identical
// tool-call batch, rather than spinning to maxToolIterations. The model in the
// 2026-06-14 transcript made the same grep 68 times before the cap.
func TestTurnStopsRepeatedToolCalls(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	var calls int
	body := sseBody(
		// Always ask for the same harmless allowlisted command.
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"x","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	if _, err := cs.Turn(context.Background(), "go"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	// Guard fires at maxRepeatedToolCalls identical batches, then one forced
	// finalize (tools withheld) — far below the maxToolIterations cap.
	if calls < maxRepeatedToolCalls || calls > maxRepeatedToolCalls+1 {
		t.Errorf("model called %d times, want ~%d (guard should break the loop)", calls, maxRepeatedToolCalls)
	}
	if calls >= maxToolIterations {
		t.Errorf("guard failed: ran to the iteration cap (%d)", calls)
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
	cs.turns, cs.tokensIn, cs.tokensOut, cs.captures, cs.injections, cs.injectedChars = 3, 1200, 340, 2, 1, 400

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
	if p.ContextStrategy != "none" { // memory store nil in this test
		t.Errorf("context strategy = %q, want none", p.ContextStrategy)
	}
	if !strings.Contains(p.Notes, "injections=1") || !strings.Contains(p.Notes, "captures=2") {
		t.Errorf("notes = %q", p.Notes)
	}
}

// TestEmitSessionMetricsThinkingAttribution covers item 3: the resolved
// thinking config and accumulated reasoning-token count land in the emitted
// eval.cell_result row.
func TestEmitSessionMetricsThinkingAttribution(t *testing.T) {
	tests := []struct {
		name            string
		kwargs          map[string]any
		reasoningTokens int
		wantThinking    string
	}{
		{
			name:            "thinking explicitly suppressed",
			kwargs:          map[string]any{"enable_thinking": false},
			reasoningTokens: 512,
			wantThinking:    "off",
		},
		{
			name:            "no suppression: default on",
			kwargs:          nil,
			reasoningTokens: 0,
			wantThinking:    "on",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now()}
			cs.Request.ChatTemplateKwargs = tt.kwargs
			cs.StartTranscript()
			t.Cleanup(func() {
				if cs.transcript != nil {
					cs.transcript.Close()
				}
			})
			cs.reasoningTokens = tt.reasoningTokens

			cs.emitSessionMetrics()

			r, err := journal.NewReader(filepath.Join(contextDir(), "journal", "eval"))
			if err != nil {
				t.Fatalf("reader: %v", err)
			}
			defer r.Close()
			e, err := r.Next()
			if err != nil || e == nil {
				t.Fatalf("expected one entry, got err=%v entry=%v", err, e)
			}
			p, perr := journal.ParseEvalCellResult(e)
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			if p.Thinking != tt.wantThinking {
				t.Errorf("Thinking = %q, want %q", p.Thinking, tt.wantThinking)
			}
			if p.ReasoningTokens != tt.reasoningTokens {
				t.Errorf("ReasoningTokens = %d, want %d", p.ReasoningTokens, tt.reasoningTokens)
			}
		})
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

func TestResumeReplaysWorkingSet(t *testing.T) {
	t.Run("stamped transcript replays spans", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)

		// Build transcript with turn stamps
		var lines []string
		lines = append(lines, `{"kind":"message","role":"system","content":"s"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"user","content":"first question"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"assistant","content":"first answer"}`)
		lines = append(lines, `{"kind":"message","turn":2,"role":"user","content":"second question"}`)
		lines = append(lines, `{"kind":"message","turn":2,"role":"assistant","content":"second answer"}`)

		path := filepath.Join(dir, "test-session.jsonl")
		content := strings.Join(lines, "\n") + "\n"
		os.WriteFile(path, []byte(content), 0644)

		cs := &CortexSession{Window: 1000, Request: &AgentRequest{Messages: []Message{}}}
		if err := cs.ResumeTranscript("test-session"); err != nil {
			t.Fatalf("ResumeTranscript: %v", err)
		}
		defer cs.transcript.Close()

		// Verify messages loaded
		if len(cs.Request.Messages) != 5 {
			t.Errorf("len(cs.Request.Messages) = %d, want 5", len(cs.Request.Messages))
		}

		// Verify turns count
		if cs.turns != 2 {
			t.Errorf("cs.turns = %d, want 2", cs.turns)
		}

		// Verify working set: frontier should be at 1 (after seed/system message)
		frontier := cs.ws.FrontierMsg()
		if frontier != 1 {
			t.Errorf("cs.ws.FrontierMsg() = %d, want 1", frontier)
		}

		// Verify TailTokens > 0 (spans were recorded)
		if cs.ws.TailTokens() == 0 {
			t.Error("cs.ws.TailTokens() = 0, want > 0")
		}
	})

	t.Run("legacy unstamped transcript stays hydrated", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)

		// Build transcript without turn stamps
		var lines []string
		lines = append(lines, `{"kind":"message","role":"system","content":"s"}`)
		lines = append(lines, `{"kind":"message","role":"user","content":"first question"}`)
		lines = append(lines, `{"kind":"message","role":"assistant","content":"first answer"}`)
		lines = append(lines, `{"kind":"message","role":"user","content":"second question"}`)
		lines = append(lines, `{"kind":"message","role":"assistant","content":"second answer"}`)

		path := filepath.Join(dir, "legacy-session.jsonl")
		content := strings.Join(lines, "\n") + "\n"
		os.WriteFile(path, []byte(content), 0644)

		cs := &CortexSession{Window: 1000, Request: &AgentRequest{Messages: []Message{}}}
		if err := cs.ResumeTranscript("legacy-session"); err != nil {
			t.Fatalf("ResumeTranscript: %v", err)
		}
		defer cs.transcript.Close()

		// Verify messages loaded
		if len(cs.Request.Messages) != 5 {
			t.Errorf("len(cs.Request.Messages) = %d, want 5", len(cs.Request.Messages))
		}

		// Verify turns count is 0 for unstamped
		if cs.turns != 0 {
			t.Errorf("cs.turns = %d, want 0", cs.turns)
		}

		// Verify no spans recorded
		if cs.ws.TailTokens() != 0 {
			t.Errorf("cs.ws.TailTokens() = %d, want 0", cs.ws.TailTokens())
		}

		// Verify frontier equals message count
		if cs.ws.FrontierMsg() != len(cs.Request.Messages) {
			t.Errorf("cs.ws.FrontierMsg() = %d, want %d", cs.ws.FrontierMsg(), len(cs.Request.Messages))
		}
	})

	t.Run("gapped stamps fall back safely", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		dir := sessionsDir()
		os.MkdirAll(dir, 0755)

		// Build transcript with gapped stamps (turn 1, then turn 3)
		var lines []string
		lines = append(lines, `{"kind":"message","role":"system","content":"s"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"user","content":"first question"}`)
		lines = append(lines, `{"kind":"message","turn":1,"role":"assistant","content":"first answer"}`)
		lines = append(lines, `{"kind":"message","turn":3,"role":"user","content":"third question"}`)
		lines = append(lines, `{"kind":"message","turn":3,"role":"assistant","content":"third answer"}`)

		path := filepath.Join(dir, "gapped-session.jsonl")
		content := strings.Join(lines, "\n") + "\n"
		os.WriteFile(path, []byte(content), 0644)

		cs := &CortexSession{Window: 1000, Request: &AgentRequest{Messages: []Message{}}}
		if err := cs.ResumeTranscript("gapped-session"); err != nil {
			t.Fatalf("ResumeTranscript: %v", err)
		}
		defer cs.transcript.Close()

		// Verify no spans recorded due to invalid sequence
		if cs.ws.TailTokens() != 0 {
			t.Errorf("cs.ws.TailTokens() = %d, want 0", cs.ws.TailTokens())
		}

		// Verify frontier equals message count
		if cs.ws.FrontierMsg() != len(cs.Request.Messages) {
			t.Errorf("cs.ws.FrontierMsg() = %d, want %d", cs.ws.FrontierMsg(), len(cs.Request.Messages))
		}

		// Verify turns is the max stamp (3)
		if cs.turns != 3 {
			t.Errorf("cs.turns = %d, want 3", cs.turns)
		}
	})
}

func TestFoldOutline(t *testing.T) {
	newFoldSession := func() *CortexSession {
		cs := &CortexSession{Window: 800, Request: &AgentRequest{}} // budget = 800/8 = 100 tokens
		for i := 1; i <= 4; i++ {
			cs.outline = append(cs.outline, cache.OutlineEntry{Turn: i, User: strings.Repeat(fmt.Sprintf("entry%d ", i), 40), Citation: fmt.Sprintf("@session/s#m%d-%d", i, i+1)})
		}
		return cs
	}

	t.Run("over budget folds the oldest half", func(t *testing.T) {
		var recordedContent string
		orig := foldSummarize
		foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
			recordedContent = content
			return "FOLDED [@session/s#m1-2]", true, nil
		}
		defer func() { foldSummarize = orig }()

		cs := newFoldSession()
		ctx := context.Background()
		cs.foldOutlineIfNeeded(ctx)

		// The stub digest kept entry 1's citation but dropped entry 2's; the
		// citation guard must restore the missing one.
		if !strings.HasPrefix(cs.outlineFolded, "FOLDED [@session/s#m1-2]") {
			t.Errorf("cs.outlineFolded = %q, want prefix %q", cs.outlineFolded, "FOLDED [@session/s#m1-2]")
		}
		if !strings.Contains(cs.outlineFolded, "[@session/s#m2-3]") {
			t.Errorf("cs.outlineFolded = %q, want the dropped citation [@session/s#m2-3] restored", cs.outlineFolded)
		}
		if len(cs.outline) != 2 {
			t.Errorf("len(cs.outline) = %d, want 2", len(cs.outline))
		}
		if len(cs.outline) >= 2 {
			if cs.outline[0].Turn != 3 || cs.outline[1].Turn != 4 {
				t.Errorf("remaining turns = %d, %d, want 3, 4", cs.outline[0].Turn, cs.outline[1].Turn)
			}
		}
		if !strings.Contains(recordedContent, "entry1") || !strings.Contains(recordedContent, "entry2") {
			t.Errorf("recordedContent missing entry1 or entry2")
		}
		if strings.Contains(recordedContent, "entry4") {
			t.Errorf("recordedContent should not contain entry4")
		}

		rendered := cs.renderOutlineBlock()
		if !strings.HasPrefix(rendered, outlineHeader) {
			t.Errorf("renderOutlineBlock() prefix does not match outlineHeader")
		}
		if !strings.Contains(rendered, "FOLDED") || !strings.Contains(rendered, "entry4") {
			t.Errorf("renderOutlineBlock() should contain both 'FOLDED' and 'entry4'")
		}
	})

	t.Run("under budget never calls the summarizer", func(t *testing.T) {
		called := false
		orig := foldSummarize
		foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
			called = true
			return "", true, nil
		}
		defer func() { foldSummarize = orig }()

		cs := &CortexSession{Window: 800, Request: &AgentRequest{}}
		cs.outline = append(cs.outline, cache.OutlineEntry{Turn: 1, User: "tiny", Citation: "@session/s#m1-2"})

		ctx := context.Background()
		cs.foldOutlineIfNeeded(ctx)

		if called {
			t.Errorf("foldSummarize should not have been called")
		}
		if cs.outlineFolded != "" {
			t.Errorf("cs.outlineFolded = %q, want empty", cs.outlineFolded)
		}
	})

	t.Run("summarizer failure leaves the outline intact", func(t *testing.T) {
		orig := foldSummarize
		foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
			return "", false, fmt.Errorf("boom")
		}
		defer func() { foldSummarize = orig }()

		cs := newFoldSession()
		initialLen := len(cs.outline)

		ctx := context.Background()
		cs.foldOutlineIfNeeded(ctx)

		if cs.outlineFolded != "" {
			t.Errorf("cs.outlineFolded = %q, want empty", cs.outlineFolded)
		}
		if len(cs.outline) != initialLen {
			t.Errorf("len(cs.outline) = %d, want %d (unchanged)", len(cs.outline), initialLen)
		}
	})
}

// TestTurnContextGaugeUpdatesMidTurn verifies that the context gauge updates
// during tool execution, not just after model responses.
func TestTurnContextGaugeUpdatesMidTurn(t *testing.T) {
	quickRetries(t)
	// Server that returns multiple tool calls in sequence
	var calls int
	body := sseBody(
		// First response: first tool call
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo one\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":50}}`,
		// Second response: second tool call
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo two\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":600,"completion_tokens":50}}`,
		// Final response: no more tool calls
		`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":700,"completion_tokens":10}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cs := &CortexSession{Window: 128000, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "system"}}}}

	// Before turn starts, LastPromptTokens should be 0
	if cs.LastPromptTokens != 0 {
		t.Errorf("before turn: LastPromptTokens = %d, want 0", cs.LastPromptTokens)
	}

	// Execute turn with tool calls
	if _, err := cs.Turn(context.Background(), "test"); err != nil {
		t.Fatalf("turn: %v", err)
	}

	// After turn, LastPromptTokens should reflect the final model response
	if cs.LastPromptTokens != 700 {
		t.Errorf("after turn: LastPromptTokens = %d, want 700 (final model response)", cs.LastPromptTokens)
	}

	// The context gauge should have updated during tool execution
	// Check that currentContextSize is being computed (it should be > 0)
	current := cs.currentContextSize()
	if current <= 0 {
		t.Errorf("currentContextSize = %d, want > 0", current)
	}

	// Verify the prompt shows current context size
	prompt := cs.Prompt()
	// The gauge should show the current context size in the format X/Y where Y is window size
	// After the final model response, LastPromptTokens = 700, window = 128000 = 128k
	if !strings.Contains(prompt, "700/128k") {
		t.Errorf("Prompt() = %q, expected to contain '700/128k' (context size)", prompt)
	}
}
