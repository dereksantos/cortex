package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// stubProvider satisfies llm.Provider for tests that only need to
// assert wiring (not response quality). All methods return zero values.
type stubProvider struct{}

func (stubProvider) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (stubProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return "", nil
}
func (stubProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	return "", llm.GenerationStats{}, nil
}
func (stubProvider) IsAvailable() bool { return true }
func (stubProvider) Name() string      { return "stub" }

// TestABRSessionOptionsValidate covers the input gates — these all
// fail before any subprocess is spawned, so they're cheap.
func TestABRSessionOptionsValidate(t *testing.T) {
	good := ABRSessionOptions{
		ScenarioID:    "x",
		REPLBinary:    "bin/cortex",
		Model:         "anthropic/claude-haiku-4.5",
		Workdir:       "/tmp/x",
		Prompts:       []string{"hi"},
		JudgeCriteria: "good answer",
		Judge:         stubProvider{},
	}

	tests := []struct {
		name    string
		mutate  func(o *ABRSessionOptions)
		wantErr string
	}{
		{"happy", func(o *ABRSessionOptions) {}, ""},
		{"empty ScenarioID", func(o *ABRSessionOptions) { o.ScenarioID = "" }, "ScenarioID"},
		{"empty REPLBinary", func(o *ABRSessionOptions) { o.REPLBinary = "" }, "REPLBinary"},
		{"empty Model", func(o *ABRSessionOptions) { o.Model = "" }, "Model"},
		{"empty Workdir", func(o *ABRSessionOptions) { o.Workdir = "" }, "workdir"},
		{"no prompts", func(o *ABRSessionOptions) { o.Prompts = nil }, "prompts"},
		{"prompt has newline", func(o *ABRSessionOptions) { o.Prompts = []string{"a\nb"} }, "newline"},
		{"empty prompt", func(o *ABRSessionOptions) { o.Prompts = []string{"   "} }, "empty"},
		{"no criteria", func(o *ABRSessionOptions) { o.JudgeCriteria = "" }, "JudgeCriteria"},
		{"no judge", func(o *ABRSessionOptions) { o.Judge = nil }, "judge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := good
			tt.mutate(&o)
			err := o.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestReadSessionJSONL_HappyPath confirms the adapter can parse a
// session.jsonl shaped the way the REPL writes it.
func TestReadSessionJSONL_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"turn":0,"session_id":"abc","user_message":"hi","final_text":"hello","tokens_in":10,"tokens_out":5,"cost_usd":0.001,"latency_ms":200}
{"turn":1,"session_id":"abc","user_message":"again","final_text":"yes","tokens_in":12,"tokens_out":3,"latency_ms":150}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rows, err := readSessionJSONL(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: want 2, got %d", len(rows))
	}
	if rows[0].FinalText != "hello" || rows[1].FinalText != "yes" {
		t.Errorf("final_text mismatch: %+v", rows)
	}
	if rows[0].TokensIn != 10 || rows[1].TokensOut != 3 {
		t.Errorf("token counts wrong: %+v", rows)
	}
}

// TestLatestSessionJSONL_PicksNewest confirms the timestamp-named dir
// resolution picks the right session when several are present (e.g. a
// retry scenario).
func TestLatestSessionJSONL_PicksNewest(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, ".cortex", "sessions")
	if err := os.MkdirAll(filepath.Join(sessions, "20260101T000000Z"), 0o755); err != nil {
		t.Fatalf("mkdir 1: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sessions, "20260517T120000Z"), 0o755); err != nil {
		t.Fatalf("mkdir 2: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sessions, "20260201T000000Z"), 0o755); err != nil {
		t.Fatalf("mkdir 3: %v", err)
	}

	got, err := latestSessionJSONL(dir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(sessions, "20260517T120000Z", "session.jsonl")
	if got != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
