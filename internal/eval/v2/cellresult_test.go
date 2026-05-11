package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCellResultJSONShape locks the on-disk JSON shape so a rename or tag
// drift during the eval-harness loop fails loudly instead of silently
// breaking downstream rollups.
func TestCellResultJSONShape(t *testing.T) {
	seed := int64(42)
	r := CellResult{
		SchemaVersion:         CellResultSchemaVersion,
		RunID:                 "01HZTESTRUN",
		Timestamp:             "2026-05-10T00:00:00Z",
		GitCommitSHA:          "abc123",
		GitBranch:             "feat/eval-harness",
		ScenarioID:            "library-service",
		SessionID:             "01-scaffold-and-books",
		Harness:               HarnessAider,
		Provider:              ProviderOpenRouter,
		Model:                 "openrouter/anthropic/claude-3-5-haiku",
		ContextStrategy:       StrategyCortex,
		CortexVersion:         "0.1.0",
		Seed:                  &seed,
		Temperature:           0.0,
		TokensIn:              18342,
		TokensOut:             944,
		InjectedContextTokens: 312,
		CostUSD:               0.0042,
		LatencyMs:             8123,
		AgentTurnsTotal:       9,
		CorrectionTurns:       2,
		TestsPassed:           18,
		TestsFailed:           1,
		TaskSuccess:           true,
		TaskSuccessCriterion:  CriterionTestsPassAll,
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	b, err := json.Marshal(&r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	wantSubstrings := []string{
		`"schema_version":"1"`,
		`"run_id":"01HZTESTRUN"`,
		`"scenario_id":"library-service"`,
		`"session_id":"01-scaffold-and-books"`,
		`"harness":"aider"`,
		`"provider":"openrouter"`,
		`"model":"openrouter/anthropic/claude-3-5-haiku"`,
		`"context_strategy":"cortex"`,
		`"cortex_version":"0.1.0"`,
		`"seed":42`,
		`"tokens_in":18342`,
		`"tokens_out":944`,
		`"injected_context_tokens":312`,
		`"cost_usd":0.0042`,
		`"latency_ms":8123`,
		`"agent_turns_total":9`,
		`"correction_turns":2`,
		`"tests_passed":18`,
		`"tests_failed":1`,
		`"task_success":true`,
		`"task_success_criterion":"tests_pass_all"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(got, w) {
			t.Errorf("json missing %q\nfull: %s", w, got)
		}
	}
}

func TestCellResultOmitsUnsetOptionals(t *testing.T) {
	r := CellResult{
		SchemaVersion:        CellResultSchemaVersion,
		RunID:                "x",
		Timestamp:            "2026-05-10T00:00:00Z",
		ScenarioID:           "x",
		Harness:              HarnessAider,
		Provider:             ProviderOllama,
		Model:                "ollama/qwen2.5-coder:1.5b",
		ContextStrategy:      StrategyBaseline,
		TaskSuccessCriterion: CriterionTestsPassAll,
	}
	b, err := json.Marshal(&r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, k := range []string{`"seed"`, `"backend"`, `"cortex_version"`, `"git_commit_sha"`, `"git_branch"`, `"session_id"`, `"notes"`} {
		if strings.Contains(got, k) {
			t.Errorf("expected %s to be omitted on baseline run, got: %s", k, got)
		}
	}
}

func TestCellResultValidate(t *testing.T) {
	base := func() CellResult {
		return CellResult{
			SchemaVersion:        CellResultSchemaVersion,
			RunID:                "x",
			Timestamp:            "2026-05-10T00:00:00Z",
			ScenarioID:           "x",
			Harness:              HarnessAider,
			Provider:             ProviderOpenRouter,
			Model:                "openrouter/x",
			ContextStrategy:      StrategyBaseline,
			TaskSuccessCriterion: CriterionTestsPassAll,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*CellResult)
		wantErr string
	}{
		{"happy baseline", func(r *CellResult) {}, ""},
		{"wrong schema version", func(r *CellResult) { r.SchemaVersion = "0" }, "schema_version"},
		{"missing run_id", func(r *CellResult) { r.RunID = "" }, "run_id"},
		{"missing timestamp", func(r *CellResult) { r.Timestamp = "" }, "timestamp"},
		{"missing scenario_id", func(r *CellResult) { r.ScenarioID = "" }, "scenario_id"},
		{"missing model", func(r *CellResult) { r.Model = "" }, "model"},
		{"unknown harness", func(r *CellResult) { r.Harness = "claude_code" }, "unknown harness"},
		{"unknown provider", func(r *CellResult) { r.Provider = "groq" }, "unknown provider"},
		{"unknown strategy", func(r *CellResult) { r.ContextStrategy = "rag" }, "unknown context_strategy"},
		{"cortex without version", func(r *CellResult) {
			r.ContextStrategy = StrategyCortex
		}, "cortex_version required"},
		{"cortex_extension without version", func(r *CellResult) {
			r.ContextStrategy = StrategyCortexExtension
		}, "cortex_version required"},
		{"happy cortex_extension with version", func(r *CellResult) {
			r.ContextStrategy = StrategyCortexExtension
			r.CortexVersion = "0.1.0"
		}, ""},
		{"happy cortex_extension with injection tokens", func(r *CellResult) {
			r.ContextStrategy = StrategyCortexExtension
			r.CortexVersion = "0.1.0"
			r.InjectedContextTokens = 80
			r.TokensIn = 1000
		}, ""},
		{"injection on baseline", func(r *CellResult) {
			r.InjectedContextTokens = 100
			r.TokensIn = 200
		}, "only cortex-flavor strategies may inject"},
		{"injection exceeds tokens_in", func(r *CellResult) {
			r.ContextStrategy = StrategyCortex
			r.CortexVersion = "0.1.0"
			r.InjectedContextTokens = 200
			r.TokensIn = 100
		}, "exceeds tokens_in"},
		{"unknown criterion", func(r *CellResult) { r.TaskSuccessCriterion = "vibes" }, "task_success_criterion"},
		{"negative latency", func(r *CellResult) { r.LatencyMs = -1 }, "latency_ms"},
		{"negative cost", func(r *CellResult) { r.CostUSD = -0.01 }, "cost_usd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mutate(&r)
			err := r.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
