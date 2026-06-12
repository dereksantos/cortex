package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string { return f.name }
func (f fakeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Type: "function", Function: llm.ToolFunc{Name: f.name}}
}
func (f fakeTool) Call(ctx context.Context, args string) (string, error) {
	return `{"ok":"` + f.name + `"}`, nil
}

// Aliases resolve at dispatch but never appear in Specs — the model is
// steered to the canonical tool while stale habits keep working.
func TestRegistryAlias(t *testing.T) {
	r := NewToolRegistry()
	r.Register(fakeTool{name: "study_file"})
	r.RegisterAlias("read_file", "study_file")

	out, err := r.Dispatch(context.Background(), llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	})
	if err != nil || !strings.Contains(out, "study_file") {
		t.Fatalf("alias dispatch failed: out=%q err=%v", out, err)
	}

	for _, s := range r.Specs() {
		if s.Function.Name == "read_file" {
			t.Errorf("alias must not be advertised in Specs")
		}
	}
	if _, err := r.Dispatch(context.Background(), llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "nope", Arguments: `{}`},
	}); err == nil {
		t.Errorf("unknown non-aliased tool should still error")
	}
}
