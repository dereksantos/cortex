//go:build !windows

package commands

import (
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestSalienceCapForSession_WindowKnown verifies the size-aware regime:
// when ctxWindow > 0, the cap scales as ctxWindow/windowFractionForCap
// with the floor at defaultToolOutputSalienceCap.
func TestSalienceCapForSession_WindowKnown(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		ctxWindow int
		wantCap   int
	}{
		{
			// Chatterbox-class 30B with --ctx-size 65536: cap should be
			// 65536/8 = 8192. Reads up to ~32 KB pass through (≈8K tokens
			// at 4 chars/token) without LLM-mediated compression.
			name:      "chatterbox 64K window",
			model:     "Qwen3-Coder-30B-A3B-Instruct-GGUF",
			ctxWindow: 65536,
			wantCap:   8192,
		},
		{
			// Qwen3-4B-2507 advertises 262K. Cap = 262144/8 = 32768 —
			// generous enough for nearly any file in a single read.
			name:      "Qwen3-4B-2507 256K window",
			model:     "Qwen3-4B-Instruct-2507-GGUF",
			ctxWindow: 262144,
			wantCap:   32768,
		},
		{
			// 4K window: 4096/8 = 512, narrowly above the 500 floor.
			name:      "tiny 4K window",
			model:     "qwen2.5-coder:1.5b",
			ctxWindow: 4096,
			wantCap:   512,
		},
		{
			// 2K window goes below the floor — 2048/8 = 256, floor 500
			// wins.
			name:      "2K window strictly below floor",
			model:     "tiny-model",
			ctxWindow: 2048,
			wantCap:   defaultToolOutputSalienceCap,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCap, _ := salienceCapForSession(tc.model, tc.ctxWindow)
			if gotCap != tc.wantCap {
				t.Errorf("model=%s ctx=%d: got cap=%d want %d", tc.model, tc.ctxWindow, gotCap, tc.wantCap)
			}
		})
	}
}

// TestSalienceCapForSession_WindowUnknownFallsBackToClass verifies the
// pre-registry regime: when ctxWindow == 0, the cap comes from the
// static SalienceCapForClass bucket.
func TestSalienceCapForSession_WindowUnknownFallsBackToClass(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantClass llm.ContextClass
	}{
		{"haiku → large", "anthropic/claude-haiku-4.5", llm.ContextLarge},
		{"qwen2.5-coder:1.5b → small", "qwen2.5-coder:1.5b", llm.ContextSmall},
		{"unknown id → medium fallback", "some-random-model-name", llm.ContextMedium},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCap, gotClass := salienceCapForSession(tc.model, 0)
			if gotClass != tc.wantClass {
				t.Errorf("model=%s: got class=%s want %s", tc.model, gotClass, tc.wantClass)
			}
			// Cap is the class default — non-zero, equals the static
			// bucket value.
			wantCap := llm.SalienceCapForClass(tc.wantClass)
			if wantCap <= 0 {
				wantCap = defaultToolOutputSalienceCap
			}
			if gotCap != wantCap {
				t.Errorf("model=%s: got cap=%d want %d (class default)", tc.model, gotCap, wantCap)
			}
		})
	}
}
