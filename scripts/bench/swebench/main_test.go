package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTurnEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   turnEnvelope
		wantOK bool
	}{
		{"plain", `{"session":"s1","reply":"done"}`, turnEnvelope{Session: "s1", Reply: "done"}, true},
		{"banner before", "note: something\n{\"session\":\"s2\",\"error\":\"boom\"}\n", turnEnvelope{Session: "s2", Error: "boom"}, true},
		{"no session", `{"reply":"x"}`, turnEnvelope{}, false},
		{"empty", "", turnEnvelope{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTurnEnvelope([]byte(tt.stdout))
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("got %+v,%v want %+v,%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// The workspace config is the pinned system: provider pinned without
// fallback, one model for both roles, no web, no model substitution, and a
// key referenced by NAME only.
func TestWorkspaceConfig(t *testing.T) {
	o := &options{model: "qwen/qwen3-coder", studyModel: "qwen/qwen3-coder", providerTags: "novita/fp8",
		requireParms: true, window: 131072, endpoint: "https://openrouter.ai/api/v1"}
	cfg := workspaceConfig(o)
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Backend struct {
			Type     string `json:"type"`
			KeyEnv   string `json:"key_env"`
			Provider struct {
				Order             []string `json:"order"`
				AllowFallbacks    *bool    `json:"allow_fallbacks"`
				RequireParameters *bool    `json:"require_parameters"`
			} `json:"provider"`
		} `json:"backend"`
		Models map[string]struct {
			Model string `json:"model"`
		} `json:"models"`
		Tools   map[string]bool `json:"tools"`
		Network map[string]bool `json:"network"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"openrouter backend", got.Backend.Type == "openrouter"},
		{"key by name", got.Backend.KeyEnv == "OPENROUTER_API_KEY"},
		{"provider pinned", reflect.DeepEqual(got.Backend.Provider.Order, []string{"novita/fp8"})},
		{"no fallbacks", got.Backend.Provider.AllowFallbacks != nil && !*got.Backend.Provider.AllowFallbacks},
		{"require parameters", got.Backend.Provider.RequireParameters != nil && *got.Backend.Provider.RequireParameters},
		{"code model", got.Models["code"].Model == "qwen/qwen3-coder"},
		{"study model", got.Models["study"].Model == "qwen/qwen3-coder"},
		{"web off", v(got.Tools, "enable_web") == "false"},
		{"self heal off", v(got.Network, "self_heal") == "false"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: config %s", c.name, b)
		}
	}
}

func v(m map[string]bool, k string) string {
	val, ok := m[k]
	if !ok {
		return "missing"
	}
	if val {
		return "true"
	}
	return "false"
}

func TestPredictionModelName(t *testing.T) {
	got := predictionModelName("f8c015cdeadbeef", "qwen/qwen3-coder")
	if got != "cortex-f8c015c__qwen--qwen3-coder" {
		t.Errorf("got %s", got)
	}
	if strings.Contains(got, "/") {
		t.Error("model name must not contain '/' (the harness uses it as a path)")
	}
}

func TestJSONLSinkAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out", "p.jsonl")
	for i := 0; i < 2; i++ { // reopening appends, never truncates
		s, err := NewJSONLSink(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Append(Prediction{InstanceID: "i", ModelNameOrPath: "m", ModelPatch: "diff\n"}); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"instance_id", "model_name_or_path", "model_patch"} {
		if _, ok := p[k]; !ok {
			t.Errorf("prediction missing %s", k)
		}
	}
}

func TestSummarize(t *testing.T) {
	rows := []Row{
		{Resolved: true, AccountCostUSD: 0.2, WallMs: 100, TokensIn: 10},
		{FailureClass: ClassWrongPatch, AccountCostUSD: 0.4, WallMs: 300, TokensIn: 20},
		{FailureClass: ClassEnv, WallMs: 5},
	}
	s := Summarize(rows)
	if s.Total != 3 || s.Resolved != 1 || s.ClassCounts[ClassWrongPatch] != 1 || s.ClassCounts[ClassEnv] != 1 {
		t.Errorf("summary=%+v", s)
	}
	if s.AgentRows != 2 || s.MedianWallMs != 100 || s.TokensIn != 30 {
		t.Errorf("agent rows/median/tokens wrong: %+v", s)
	}
}
