package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// ShouldCaptureConfig wires the handler to a provider.
type ShouldCaptureConfig struct {
	Provider llm.Provider
}

// shouldCaptureResponse is the parser target.
type shouldCaptureResponse struct {
	Capture bool   `json:"capture"`
	Tag     string `json:"tag"`
	Why     string `json:"why"`
}

// ShouldCaptureSpec returns the NodeSpec for decide.should_capture.
func ShouldCaptureSpec(cfg ShouldCaptureConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "should_capture",
		Description: "decide whether to record an event in the project journal (Y/N + tag)",
		Inputs: []dag.ParamSpec{
			{Name: "event", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "capture", Type: "bool"},
			{Name: "tag", Type: "string"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    shouldCaptureCostHint,
		Handler: NewShouldCaptureHandler(cfg),
	}
}

// shouldCaptureCostHint — tight Y/N + tag, ~30-50 tok output, 500ms
// p50 Haiku 4.5. Set 600ms / 100 tok for headroom.
var shouldCaptureCostHint = dag.Cost{LatencyMS: 600, Tokens: 100}

// validCaptureTags is the allowed set the prompt asks the model to
// pick from. Anything else gets normalized to "none" (and capture
// forced to false).
var validCaptureTags = map[string]bool{
	"decision": true, "constraint": true, "pattern": true,
	"correction": true, "none": true,
}

// NewShouldCaptureHandler returns a dag.Handler for
// decide.should_capture.
//
// Inputs:
//   - event (string) — required; the event content (typically a
//     transcript line, commit message, or tool result)
//
// Outputs:
//   - capture (bool)
//   - tag (string)  — decision | constraint | pattern | correction | none
//   - why (string)
//   - fallback (bool)
//
// Fallback: keyword-marker capture. If event contains decision /
// constraint / correction markers (subset of the
// mechanicalExtractInsights markers — reused), capture=true with the
// matched tag. Otherwise capture=false / tag=none.
func NewShouldCaptureHandler(cfg ShouldCaptureConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		event := readString(in, "event")
		if event == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.should_capture: 'event' (string) is required")
		}

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			cap, tag := keywordMarkerCapture(event)
			return dag.NodeResult{
				Out: map[string]any{
					"capture":  cap,
					"tag":      tag,
					"why":      "keyword-marker fallback",
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("decide_should_capture")
		if terr != nil {
			cap, tag := keywordMarkerCapture(event)
			return dag.NodeResult{
				Out: map[string]any{
					"capture":  cap,
					"tag":      tag,
					"why":      fmt.Sprintf("template load failed: %v", terr),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		prompt, rerr := pt.Render(map[string]any{"event": event})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.should_capture: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			cap, tag := keywordMarkerCapture(event)
			return dag.NodeResult{
				Out: map[string]any{
					"capture":  cap,
					"tag":      tag,
					"why":      fmt.Sprintf("llm error: %v", gerr),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		parsed, perr := parseShouldCaptureResponse(resp)
		if perr != nil {
			cap, tag := keywordMarkerCapture(event)
			return dag.NodeResult{
				Out: map[string]any{
					"capture":  cap,
					"tag":      tag,
					"why":      fmt.Sprintf("parse error: %v", perr),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		// Normalize tag.
		tag := strings.ToLower(strings.TrimSpace(parsed.Tag))
		if !validCaptureTags[tag] {
			tag = "none"
		}
		// If capture is true but tag normalized to "none", flip capture
		// — the tag is the authoritative classification signal.
		if tag == "none" {
			parsed.Capture = false
		}

		return dag.NodeResult{
			Out: map[string]any{
				"capture":  parsed.Capture,
				"tag":      tag,
				"why":      parsed.Why,
				"fallback": false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parseShouldCaptureResponse(resp string) (shouldCaptureResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return shouldCaptureResponse{}, fmt.Errorf("no JSON object found")
	}
	var parsed shouldCaptureResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return shouldCaptureResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return parsed, nil
}

// keywordMarkerCapture is the deterministic fallback. Returns
// (capture, tag) — tag is one of the validCaptureTags.
func keywordMarkerCapture(event string) (bool, string) {
	lower := strings.ToLower(event)
	switch {
	case strings.Contains(lower, "don't ") || strings.Contains(lower, "never ") || strings.Contains(lower, "avoid "):
		return true, "constraint"
	case strings.Contains(lower, "always ") || strings.Contains(lower, "must "):
		return true, "constraint"
	case strings.Contains(lower, "instead of "):
		return true, "correction"
	case strings.Contains(lower, "decided ") || strings.Contains(lower, "chose ") || strings.Contains(lower, "prefer "):
		return true, "decision"
	}
	return false, "none"
}
