package shellrisk

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeProvider implements llm.Provider with a canned GenerateWithSystem
// response (or error). Only GenerateWithSystem is exercised by the classifier.
type fakeProvider struct {
	resp    string
	err     error
	gotUser *string // when non-nil, captures the user prompt passed in
}

func (f fakeProvider) Generate(_ context.Context, _ string) (string, error) {
	return f.resp, f.err
}
func (f fakeProvider) GenerateWithSystem(_ context.Context, user, _ string) (string, error) {
	if f.gotUser != nil {
		*f.gotUser = user
	}
	return f.resp, f.err
}
func (f fakeProvider) GenerateWithStats(_ context.Context, _ string) (string, llm.GenerationStats, error) {
	return f.resp, llm.GenerationStats{}, f.err
}
func (f fakeProvider) IsAvailable() bool { return true }
func (f fakeProvider) Name() string      { return "fake" }

func TestProviderClassifier_Parsing(t *testing.T) {
	cases := []struct {
		name    string
		resp    string
		wantLvl Level
		wantErr bool
	}{
		{"safe", `{"risk":"safe","reason":"reads a file"}`, Safe, false},
		{"risky", `{"risk":"risky","reason":"pushes commits"}`, Risky, false},
		{"fenced", "```json\n{\"risk\":\"safe\",\"reason\":\"ok\"}\n```", Safe, false},
		{"prose-wrapped", `Sure! {"risk":"risky","reason":"deletes"} hope that helps`, Risky, false},
		{"unknown-risk-value", `{"risk":"maybe","reason":"?"}`, Risky, false}, // committed verdict → risky
		{"no-json", `I think this is fine`, Risky, true},                      // → fail closed
		{"empty-risk", `{"reason":"x"}`, Risky, true},                         // → fail closed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fn := ProviderClassifier(fakeProvider{resp: c.resp}, "")
			lvl, _, err := fn(context.Background(), "some command")
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if lvl != c.wantLvl {
				t.Errorf("level = %s, want %s", lvl, c.wantLvl)
			}
		})
	}
}

func TestProviderClassifier_TransportError(t *testing.T) {
	fn := ProviderClassifier(fakeProvider{err: errors.New("backend down")}, "")
	_, _, err := fn(context.Background(), "git push")
	if err == nil {
		t.Fatal("want transport error surfaced (so Classify fails closed)")
	}
}

// End-to-end through Classify: an LLM that says "safe" on a gray-zone command
// is honored, but the deny-floor still wins.
func TestProviderClassifier_ThroughClassify(t *testing.T) {
	saysSafe := ProviderClassifier(fakeProvider{resp: `{"risk":"safe","reason":"fine"}`}, "")

	v := Classify(context.Background(), "mv a.txt b.txt", saysSafe)
	if v.Level != Safe || v.Tier != "classified" {
		t.Errorf("gray-zone safe: got %s/%s, want safe/classified", v.Level, v.Tier)
	}

	// Deny-floor command never reaches the (safe-saying) classifier.
	v = Classify(context.Background(), "rm -rf /", saysSafe)
	if v.Level != Blocked || v.Tier != "deny-floor" {
		t.Errorf("deny-floor: got %s/%s, want blocked/deny-floor", v.Level, v.Tier)
	}
}

func TestProviderClassifier_TaskContext(t *testing.T) {
	t.Run("context is folded into the prompt", func(t *testing.T) {
		var got string
		fn := ProviderClassifier(fakeProvider{resp: `{"risk":"safe","reason":"ok"}`, gotUser: &got}, "clean up the build directory")
		if _, _, err := fn(context.Background(), "rm -rf ./build"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "clean up the build directory") {
			t.Errorf("task context missing from prompt: %q", got)
		}
		if !strings.Contains(got, "rm -rf ./build") {
			t.Errorf("command missing from prompt: %q", got)
		}
	})

	t.Run("empty context omits the task section", func(t *testing.T) {
		var got string
		fn := ProviderClassifier(fakeProvider{resp: `{"risk":"safe","reason":"ok"}`, gotUser: &got}, "   ")
		if _, _, err := fn(context.Background(), "ls"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "Task the agent is working on") {
			t.Errorf("empty context should not add a task section: %q", got)
		}
	})

	t.Run("over-long context is capped", func(t *testing.T) {
		var got string
		long := strings.Repeat("x", maxTaskContextChars+500)
		fn := ProviderClassifier(fakeProvider{resp: `{"risk":"safe","reason":"ok"}`, gotUser: &got}, long)
		if _, _, err := fn(context.Background(), "ls"); err != nil {
			t.Fatal(err)
		}
		if strings.Count(got, "x") > maxTaskContextChars {
			t.Errorf("context not capped: got %d x's, want <= %d", strings.Count(got, "x"), maxTaskContextChars)
		}
	})
}
