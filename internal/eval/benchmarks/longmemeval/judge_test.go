package longmemeval

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// mockJudge implements llm.Provider, returning a canned response from
// GenerateWithSystem so we can exercise prompt formatting + parsing
// without a real LLM. Only the methods actually invoked by Judge are
// non-trivial; the rest satisfy the interface.
type mockJudge struct {
	gotPrompt string
	gotSystem string
	reply     string
	err       error
}

func (m *mockJudge) Name() string { return "mock-judge" }
func (m *mockJudge) Generate(_ context.Context, _ string) (string, error) {
	return m.reply, m.err
}
func (m *mockJudge) GenerateWithSystem(_ context.Context, prompt, system string) (string, error) {
	m.gotPrompt = prompt
	m.gotSystem = system
	return m.reply, m.err
}
func (m *mockJudge) GenerateWithStats(_ context.Context, _ string) (string, llm.GenerationStats, error) {
	return m.reply, llm.GenerationStats{}, m.err
}
func (m *mockJudge) IsAvailable() bool { return true }

func TestBuildJudgePrompt_GoldenContract(t *testing.T) {
	// Golden test: the prompt template encodes the judge contract.
	// If anyone edits the wording without intent, this test fires.
	q := Question{
		QuestionID:   "qa_demo",
		QuestionType: "single-session-user",
		Question:     "Where did I move?",
		Answer:       "Berlin",
		QuestionDate: "2024-08-15",
	}
	got := BuildJudgePrompt(q, "You moved to Berlin.")

	// The golden assertions pin every semantically load-bearing part
	// of the contract. If you intentionally change the template, the
	// fix is to update these strings — not to weaken the checks.
	mustContain := []string{
		"long-horizon memory benchmark",
		"Question (asked 2024-08-15): Where did I move?",
		"Gold answer: Berlin",
		"You moved to Berlin.",
		`{"correct": true|false, "reason": "one short sentence"}`,
		"ABSTENTION", // axis-specific rule must survive
		"CORRECT if it captures the same factual content",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n----\n%s\n----", want, got)
		}
	}

	mustNotContain := []string{
		"```", // no markdown fencing
	}
	for _, bad := range mustNotContain {
		if strings.Contains(got, bad) {
			t.Errorf("prompt should NOT contain %q\n----\n%s\n----", bad, got)
		}
	}
}

func TestJudge_PassesPromptAndSystemToProvider(t *testing.T) {
	m := &mockJudge{reply: `{"correct":true,"reason":"matches gold"}`}
	q := Question{
		Question:     "Q",
		Answer:       "A",
		QuestionDate: "2024-01-01",
	}
	v, err := Judge(context.Background(), m, q, "candidate answer")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !v.Correct {
		t.Errorf("Correct=false want true")
	}
	if v.Reason == "" {
		t.Errorf("Reason empty")
	}
	if !strings.Contains(m.gotPrompt, "Q") || !strings.Contains(m.gotPrompt, "A") {
		t.Errorf("prompt missing question/answer; got=%q", m.gotPrompt)
	}
	if m.gotSystem != JudgeSystemPrompt {
		t.Errorf("system prompt = %q want %q", m.gotSystem, JudgeSystemPrompt)
	}
}

func TestJudge_NilProviderErrors(t *testing.T) {
	_, err := Judge(context.Background(), nil, Question{}, "x")
	if err == nil {
		t.Fatal("want error on nil judge")
	}
	if !strings.Contains(err.Error(), "judge provider is nil") {
		t.Errorf("err=%q should mention nil provider", err)
	}
}

func TestParseJudgeVerdict(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOK    bool
		wantTrue  bool
		wantParse bool
	}{
		{
			name:      "clean JSON correct",
			raw:       `{"correct":true,"reason":"ok"}`,
			wantOK:    true,
			wantTrue:  true,
			wantParse: true,
		},
		{
			name:      "clean JSON incorrect",
			raw:       `{"correct":false,"reason":"wrong city"}`,
			wantOK:    true,
			wantTrue:  false,
			wantParse: true,
		},
		{
			name:      "markdown-fenced JSON",
			raw:       "```json\n{\"correct\":true,\"reason\":\"ok\"}\n```",
			wantOK:    true,
			wantTrue:  true,
			wantParse: true,
		},
		{
			name:      "prose-prefixed JSON",
			raw:       "Verdict: {\"correct\":true,\"reason\":\"fine\"}",
			wantOK:    true,
			wantTrue:  true,
			wantParse: true,
		},
		{
			name:      "empty response",
			raw:       "",
			wantOK:    false,
			wantParse: false,
		},
		{
			name:      "no JSON at all",
			raw:       "I think this is correct.",
			wantOK:    false,
			wantParse: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseJudgeVerdict(tc.raw)
			if tc.wantParse {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if v.Correct != tc.wantTrue {
					t.Errorf("Correct=%v want %v", v.Correct, tc.wantTrue)
				}
			} else {
				if err == nil {
					t.Errorf("want parse error, got %+v", v)
				}
			}
		})
	}
}

func TestJudge_ParseFailureBubblesRaw(t *testing.T) {
	m := &mockJudge{reply: "no json here"}
	_, err := Judge(context.Background(), m, Question{}, "x")
	if err == nil {
		t.Fatal("want parse error")
	}
	if !strings.Contains(err.Error(), "no json here") {
		t.Errorf("err should include raw response for diagnosis; got %q", err)
	}
}
