package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestVerify_PassOnTrueCommand(t *testing.T) {
	h := NewVerifyHandler(VerifyConfig{})
	res, err := h(context.Background(),
		map[string]any{"cmd": "true"},
		dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if pass, _ := res.Out["pass"].(bool); !pass {
		t.Errorf("expected pass=true; got %+v", res.Out)
	}
}

func TestVerify_FailOnFalseCommand(t *testing.T) {
	h := NewVerifyHandler(VerifyConfig{})
	res, err := h(context.Background(),
		map[string]any{"cmd": "false"},
		dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if pass, _ := res.Out["pass"].(bool); pass {
		t.Errorf("expected pass=false; got %+v", res.Out)
	}
}

func TestVerify_EmptyCmdIsPass(t *testing.T) {
	h := NewVerifyHandler(VerifyConfig{})
	res, _ := h(context.Background(),
		map[string]any{"cmd": ""},
		dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 5})
	if pass, _ := res.Out["pass"].(bool); !pass {
		t.Errorf("empty cmd should pass; got %+v", res.Out)
	}
}

func TestVerify_OutputTailCaptured(t *testing.T) {
	h := NewVerifyHandler(VerifyConfig{})
	res, _ := h(context.Background(),
		map[string]any{"cmd": "echo hello-from-verify"},
		dag.Budget{LatencyMS: 5000, Tokens: 0, Depth: 5})
	tail, _ := res.Out["output_tail"].(string)
	if tail != "hello-from-verify" {
		t.Errorf("output_tail: got %q, want hello-from-verify", tail)
	}
}
