package codebase

import (
	"context"
	"errors"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// stubProvider is a minimal llm.Provider that returns a canned response
// for the judge unit test. We don't want the judge tests to require a
// live model — that's what the codebase eval --judge-model CLI is for.
type stubProvider struct {
	resp string
	err  error
}

func (s *stubProvider) Generate(ctx context.Context, prompt string) (string, error) {
	return s.resp, s.err
}
func (s *stubProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return s.resp, s.err
}
func (s *stubProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	return s.resp, llm.GenerationStats{}, s.err
}
func (s *stubProvider) IsAvailable() bool { return true }
func (s *stubProvider) Name() string      { return "stub" }

func TestJudgeNilProviderReturnsNil(t *testing.T) {
	jr, err := Judge(context.Background(), "p", "a", "r", JudgeOptions{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if jr != nil {
		t.Errorf("expected nil result when no provider, got %+v", jr)
	}
}

func TestJudgeEmptyAnswerFailsClosed(t *testing.T) {
	jr, err := Judge(context.Background(), "p", "", "r", JudgeOptions{
		Provider: &stubProvider{resp: `{"pass":true}`},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if jr == nil || jr.Pass {
		t.Errorf("empty answer should fail-closed without calling provider: %+v", jr)
	}
}

func TestJudgeRequiresRubric(t *testing.T) {
	_, err := Judge(context.Background(), "p", "answer", "", JudgeOptions{
		Provider: &stubProvider{resp: `{"pass":true}`},
	})
	if err == nil {
		t.Fatal("expected error for empty rubric")
	}
}

func TestJudgeParsesStrictJSON(t *testing.T) {
	prov := &stubProvider{resp: `{"pass":true,"reason":"all good","hallucination_flag":false}`}
	jr, err := Judge(context.Background(), "p", "answer", "rubric", JudgeOptions{
		Provider: prov, Model: "test-model",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !jr.Pass {
		t.Errorf("expected pass=true, got %+v", jr)
	}
	if jr.Reason != "all good" {
		t.Errorf("reason = %q", jr.Reason)
	}
	if jr.Model != "test-model" {
		t.Errorf("model = %q", jr.Model)
	}
}

func TestJudgeToleratesProseWrappedJSON(t *testing.T) {
	prov := &stubProvider{resp: "Sure, here's the verdict:\n```json\n{\"pass\":false,\"reason\":\"bad cite\"}\n```\nLet me know if you need more."}
	jr, err := Judge(context.Background(), "p", "answer", "rubric", JudgeOptions{Provider: prov})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if jr.Pass {
		t.Error("expected pass=false in wrapped JSON")
	}
	if jr.Reason != "bad cite" {
		t.Errorf("reason = %q", jr.Reason)
	}
}

func TestJudgeProviderErrorFails(t *testing.T) {
	prov := &stubProvider{err: errors.New("provider timeout")}
	_, err := Judge(context.Background(), "p", "answer", "rubric", JudgeOptions{Provider: prov})
	if err == nil {
		t.Fatal("expected error from provider")
	}
}

func TestJudgeBadJSONReturnsFailingResult(t *testing.T) {
	prov := &stubProvider{resp: "garbage from the model"}
	jr, err := Judge(context.Background(), "p", "answer", "rubric", JudgeOptions{Provider: prov})
	if err != nil {
		t.Fatalf("Judge should swallow parse errors: %v", err)
	}
	if jr.Pass {
		t.Error("bad JSON should result in pass=false")
	}
	if jr.Reason == "" {
		t.Error("expected reason to include parse error")
	}
}

func TestShouldJudge(t *testing.T) {
	cases := []struct {
		name string
		fx   *Fixture
		want bool
	}{
		{"nil", nil, false},
		{"R-class is mechanical-only", &Fixture{Group: GroupRead, JudgeRubric: "x"}, false},
		{"B-class is mechanical-only", &Fixture{Group: GroupBehavior, JudgeRubric: "x"}, false},
		{"Q-class without rubric", &Fixture{Group: GroupQuestion}, false},
		{"Q-class with rubric", &Fixture{Group: GroupQuestion, JudgeRubric: "x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldJudge(c.fx); got != c.want {
				t.Errorf("ShouldJudge = %v, want %v", got, c.want)
			}
		})
	}
}
