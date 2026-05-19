// Package ops — decide.plan.
//
// Decomposes a project-shaped prompt ("build me a CLI tool that…")
// into an ordered list of sub-tasks. Each sub-task carries a
// complexity tag the chain uses to route to a model (via
// model.route) and a description the chain uses as the prompt for
// a per-sub-task decide.coding_turn invocation.
//
// V0 scope: single LLM pass, no re-planning, ordered output. The
// chain (commands/run.go runProjectDAG) consumes the sub-tasks
// sequentially.
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

// Subtask is one ordered piece of work the planner emits.
type Subtask struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	// Complexity guides model.route: "simple" (local 1.5b), "moderate"
	// (haiku), "hard" (haiku — sonnet is opt-in only, too costly).
	Complexity string `json:"complexity"`
	// VerifyCmd, if non-empty, is run by act.verify after the sub-task's
	// coding_turn finishes. Empty = no per-step verify; the project's
	// final verify (provided to runProjectDAG) still runs at the end.
	VerifyCmd string `json:"verify_cmd,omitempty"`
}

// planResponse is the parser target for the model's JSON output.
type planResponse struct {
	ProjectIntent string    `json:"project_intent"`
	Subtasks      []Subtask `json:"subtasks"`
}

// PlanConfig wires NewPlanHandler to a provider.
type PlanConfig struct {
	Provider llm.Provider
	// MaxSubtasks caps the planner's output. Defaults to 6.
	MaxSubtasks int
}

// PlanSpec returns the NodeSpec for decide.plan.
func PlanSpec(cfg PlanConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "plan",
		Description: "decompose a project prompt into ordered sub-tasks tagged by complexity",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
			{Name: "language", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "project_intent", Type: "string"},
			{Name: "subtasks", Type: "[]Subtask"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    planCostHint,
		Handler: NewPlanHandler(cfg),
	}
}

// planCostHint — sized for a Haiku-class planner: ~8-12s wall + ~600
// tokens for a 4-6 sub-task decomposition. Real calibration will
// adjust once we have rolling traces.
var planCostHint = dag.Cost{LatencyMS: 15000, Tokens: 800}

// planPrompt is the system prompt the planner uses. Keep it short
// and concrete; the format-instruction at the bottom is what most
// affects parse success.
const planPrompt = `You are a project planner. The user wants to build something. Decompose their request into an ordered list of small, concrete sub-tasks a coding agent can execute one at a time.

Constraints:
- Output 2-{{MAX}} sub-tasks. Fewer is fine if the project is small.
- Each sub-task does ONE thing — write a file, add a test, run a command. Not "implement the whole feature."
- Order matters: a later sub-task may depend on an earlier one.
- Tag each sub-task's complexity: "simple" (boilerplate, scaffold, single file), "moderate" (multi-file edit, requires API knowledge), "hard" (architecture, cross-cutting concern).
- For each sub-task, suggest a verify_cmd if obvious (e.g., "go build ./...", "go test ./..."). Leave empty if not applicable.

Respond with ONLY a JSON object matching this exact schema:
{
  "project_intent": "<one-sentence summary of what the user wants to build>",
  "subtasks": [
    {"id": "s1", "description": "...", "complexity": "simple|moderate|hard", "verify_cmd": ""},
    {"id": "s2", "description": "...", "complexity": "simple|moderate|hard", "verify_cmd": "go build ./..."}
  ]
}

No prose before or after the JSON. No markdown fences.`

// NewPlanHandler returns the dag.Handler for decide.plan.
//
// Fallback (no provider configured): emit one Subtask containing the
// raw prompt as description + complexity="moderate". This lets the
// chain still walk end-to-end without an API key — coding_turn just
// gets the original prompt as if no planning happened.
func NewPlanHandler(cfg PlanConfig) dag.Handler {
	if cfg.MaxSubtasks <= 0 {
		cfg.MaxSubtasks = 6
	}
	systemPrompt := strings.ReplaceAll(planPrompt, "{{MAX}}", fmt.Sprintf("%d", cfg.MaxSubtasks))

	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.plan: 'prompt' (string) is required")
		}

		// No provider → mechanical fallback: pass-through single sub-task.
		if cfg.Provider == nil || !budget.CanAfford(planCostHint) {
			return dag.NodeResult{
				Out: map[string]any{
					"project_intent": prompt,
					"subtasks": []Subtask{{
						ID:          "s1",
						Description: prompt,
						Complexity:  "moderate",
					}},
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), Tokens: 0},
			}, nil
		}

		respText, err := cfg.Provider.GenerateWithSystem(ctx, prompt, systemPrompt)
		latency := int(time.Since(started).Milliseconds())
		if err != nil {
			// Provider call failed → fall back to single-sub-task pass-through
			// rather than fail the whole project run.
			return dag.NodeResult{
				Out: map[string]any{
					"project_intent": prompt,
					"subtasks": []Subtask{{
						ID:          "s1",
						Description: prompt,
						Complexity:  "moderate",
					}},
					"fallback": true,
					"error":    err.Error(),
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: 0},
			}, nil
		}

		parsed, parseErr := parsePlanResponse(respText)
		if parseErr != nil || len(parsed.Subtasks) == 0 {
			return dag.NodeResult{
				Out: map[string]any{
					"project_intent": prompt,
					"subtasks": []Subtask{{
						ID:          "s1",
						Description: prompt,
						Complexity:  "moderate",
					}},
					"fallback":    true,
					"parse_error": fmt.Sprintf("%v (raw=%.200s)", parseErr, respText),
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(respText)},
			}, nil
		}

		// Truncate to MaxSubtasks defensively in case the model ignored
		// the cap.
		if len(parsed.Subtasks) > cfg.MaxSubtasks {
			parsed.Subtasks = parsed.Subtasks[:cfg.MaxSubtasks]
		}
		// Normalize complexity values; default unknown → "moderate".
		for i := range parsed.Subtasks {
			switch parsed.Subtasks[i].Complexity {
			case "simple", "moderate", "hard":
			default:
				parsed.Subtasks[i].Complexity = "moderate"
			}
			if parsed.Subtasks[i].ID == "" {
				parsed.Subtasks[i].ID = fmt.Sprintf("s%d", i+1)
			}
		}

		return dag.NodeResult{
			Out: map[string]any{
				"project_intent": parsed.ProjectIntent,
				"subtasks":       parsed.Subtasks,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(respText)},
		}, nil
	}
}

// estimateTokens approximates token count from byte length using the
// common 4-bytes-per-token heuristic. The Provider interface doesn't
// expose token counts; this is good enough for budget accounting.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// parsePlanResponse extracts the JSON object from the model's
// response. Tolerates leading/trailing whitespace and optional
// markdown fences ("```json ... ```") in case the model can't help
// itself.
func parsePlanResponse(raw string) (planResponse, error) {
	s := strings.TrimSpace(raw)
	// Strip a leading ```json or ``` fence if present.
	if strings.HasPrefix(s, "```") {
		// Drop the opening fence line.
		nl := strings.IndexByte(s, '\n')
		if nl > 0 {
			s = s[nl+1:]
		}
		// Drop the trailing fence.
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	var out planResponse
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return out, err
	}
	return out, nil
}
