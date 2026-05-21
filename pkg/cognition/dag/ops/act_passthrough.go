// Package ops — act.passthrough.
//
// Mechanical zero-LLM terminal node for trivial conversational turns
// (greetings, acknowledgments). When sense.classify_intent flags the
// prompt as a greeting, the REPL seeds act.passthrough instead of
// the full sense.prompt → decide.next → decide.coding_turn chain —
// short-circuits the harness / agent-loop / verifier cost that those
// inputs don't warrant.
//
// The reply is selected mechanically from a small canned set keyed
// off the prompt's surface form. Not clever; that's the point — the
// LLM saw the prompt at classify_intent time and decided it didn't
// warrant a generation call.
package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// PassthroughConfig wires the handler to an optional response sink.
//
// OnResponse, when non-nil, is invoked synchronously with the chosen
// reply text. The REPL uses this to capture the response into its
// HarnessResult / LoopResult envelope so the standard render / journal
// path keeps working unchanged. Tests can leave it nil; the Out map
// still carries the reply.
type PassthroughConfig struct {
	OnResponse func(reply string)
}

// PassthroughSpec returns the NodeSpec for act.passthrough. FuncAct
// because it produces visible side-effect output (the reply shown to
// the user); read-only and never confirmed.
func PassthroughSpec(cfg PassthroughConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncAct,
		Op:          "passthrough",
		Description: "respond with a canned reply for trivial conversational turns; no LLM, no tools, no verifier",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "response", Type: "string"},
		},
		Cost: dag.Cost{LatencyMS: 5, Tokens: 0},
		AxisContract: &dag.AxisContract{
			Mutator:              false,
			RequiresConfirmation: false,
		},
		Handler: NewPassthroughHandler(cfg),
	}
}

// NewPassthroughHandler returns the dag.Handler for act.passthrough.
//
// Inputs:
//   - prompt (string) — required; the original user prompt.
//
// Outputs:
//   - response (string) — the canned reply (also forwarded to
//     cfg.OnResponse when set).
//
// Pure mechanical; never errors except on a missing prompt input.
func NewPassthroughHandler(cfg PassthroughConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("act.passthrough: 'prompt' (string) is required")
		}

		reply := pickPassthroughReply(prompt)
		if cfg.OnResponse != nil {
			cfg.OnResponse(reply)
		}
		return dag.NodeResult{
			Out: map[string]any{
				"response": reply,
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
		}, nil
	}
}

// pickPassthroughReply selects a reply by inspecting the prompt's
// lowered, trimmed surface form. Matches are intentionally narrow —
// anything outside the small greeting / ack space falls through to
// the generic "ready when you are" reply that doesn't pretend to
// have answered something it didn't.
func pickPassthroughReply(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	switch {
	case p == "" || p == "hi" || p == "hello" || p == "hey" || p == "yo" || p == "sup":
		return "Hi — what would you like to work on?"
	case strings.HasPrefix(p, "good morning"),
		strings.HasPrefix(p, "good afternoon"),
		strings.HasPrefix(p, "good evening"):
		return "Hey. What's on the list?"
	case p == "thanks" || p == "thank you" || p == "ty":
		return "Anytime."
	case p == "ok" || p == "okay" || p == "k" || p == "got it":
		return "Standing by."
	case p == "bye" || p == "goodbye" || p == "later":
		return "Catch you later."
	default:
		return "Ready when you are."
	}
}
