package codebase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// JudgeResult is the LLM judge's verdict on whether an answer is
// substantively correct against the fixture's rubric. Mirrors the
// "judge_pass + judge_reason" contract docs/eval-suite-codebase-reading.md
// names in slice 3, with one extra signal field (HallucinationFlag)
// the model surfaces opportunistically.
type JudgeResult struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`

	// HallucinationFlag, when true, signals that the judge believes
	// the answer fabricated a file path, line number, or symbol that
	// doesn't actually exist. Distinct from "wrong content" so the
	// dashboard can break the failure mode apart in slice 4.
	HallucinationFlag bool `json:"hallucination_flag,omitempty"`

	// Model + RawResponse let slice-4 audit how the judge arrived at
	// the verdict. Stored on the RunResult, not persisted to the cell
	// row unless the user explicitly enables --verbose-judge.
	Model       string `json:"model,omitempty"`
	RawResponse string `json:"raw_response,omitempty"`
}

// JudgeOptions threads the judge provider + model id through the
// runner. When Provider is nil, Judge returns (nil, nil) so callers
// gracefully degrade to mechanical-only scoring.
type JudgeOptions struct {
	Provider llm.Provider
	Model    string // informational; provider already pinned to a model
}

// Judge runs the LLM judge over (prompt, answer, rubric) and returns
// the pass/reason verdict. R/B-class fixtures never call this — their
// bounds are fully mechanical. Q-class fixtures (especially Q2/Q3/Q4)
// invoke it to catch "valid-looking but wrong content" answers.
//
// The judge's prompt is intentionally tight: pass=true ONLY when the
// answer correctly resolves the user's prompt against the rubric AND
// every cited file/line is real (no fabrications). Slice-1's mechanical
// must_not_invent catches name-shaped hallucinations; the judge catches
// behavioral ones ("the function returns X" when it doesn't).
//
// Returns (nil, nil) when opts.Provider is nil — the runner uses that
// to skip the judge in mechanical-only mode.
func Judge(ctx context.Context, prompt, answer, rubric string, opts JudgeOptions) (*JudgeResult, error) {
	if opts.Provider == nil {
		return nil, nil
	}
	if strings.TrimSpace(answer) == "" {
		return &JudgeResult{
			Pass:   false,
			Reason: "answer text is empty — synthesis turn did not produce output",
			Model:  opts.Model,
		}, nil
	}
	if strings.TrimSpace(rubric) == "" {
		return nil, errors.New("Judge: rubric is required when invoking the LLM judge")
	}

	const systemPrompt = `You are an LLM judge evaluating an AI coding agent's answer to a codebase-reading question. Return ONLY valid JSON, no markdown.`

	userPrompt := fmt.Sprintf(`User asked the agent:
%s

Agent's answer:
%s

Substantive-correctness rubric:
%s

Decide:
- pass: true if the answer correctly resolves the user's question AND every cited file/line/symbol is plausibly real for a real codebase (no obvious fabrications). false if the answer is wrong, evasive, or invented content.
- reason: one sentence stating WHY you chose pass/fail. Quote the rubric phrase if a specific clause drove the decision.
- hallucination_flag: true if you spotted any fabricated file path, line number, function name, or behavior description.

Return only this JSON shape:
{"pass": true|false, "reason": "...", "hallucination_flag": true|false}`,
		prompt, answer, rubric)

	raw, err := opts.Provider.GenerateWithSystem(ctx, userPrompt, systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("judge generate: %w", err)
	}
	parsed, err := parseJudgeJSON(raw)
	if err != nil {
		return &JudgeResult{
			Pass:        false,
			Reason:      fmt.Sprintf("judge parse error: %v", err),
			Model:       opts.Model,
			RawResponse: raw,
		}, nil
	}
	parsed.Model = opts.Model
	parsed.RawResponse = raw
	return parsed, nil
}

// ShouldJudge reports whether a fixture is in the slice's judge-bound
// subset. Q-class evals get judged when they carry a rubric; R-class
// and B-class are mechanical-only by design (per the doc: "R-class and
// B-class are fully mechanical").
func ShouldJudge(fx *Fixture) bool {
	if fx == nil || fx.JudgeRubric == "" {
		return false
	}
	return fx.Group == GroupQuestion
}

// parseJudgeJSON is forgiving on whitespace + the model's tendency to
// wrap JSON in stray prose. Falls back to the {…} substring when a
// strict Unmarshal fails.
func parseJudgeJSON(raw string) (*JudgeResult, error) {
	jr := &JudgeResult{}
	if err := json.Unmarshal([]byte(raw), jr); err == nil {
		return jr, nil
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in judge response: %q", truncate(raw, 200))
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), jr); err != nil {
		return nil, fmt.Errorf("decode %q: %w", truncate(raw[start:end+1], 200), err)
	}
	return jr, nil
}
