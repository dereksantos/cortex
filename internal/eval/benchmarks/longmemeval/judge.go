package longmemeval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// DefaultJudgeModel is the OpenRouter model id the package uses when
// the caller does not override it via --judge-model. Claude Haiku 4.5
// is a deliberate deviation from upstream LongMemEval's GPT-4o
// reference; documented in docs/benchmarks/longmemeval.md. Operators
// running parity comparisons should pass --judge-model openai/gpt-4o
// (or whatever OpenRouter slug matches their parity target).
const DefaultJudgeModel = "anthropic/claude-haiku-4.5"

// JudgePromptTemplate is a single-string template — keeping it in one
// place lets the golden test pin its wording so behavioral drift in
// the prompt is caught explicitly rather than as score noise.
//
// Format conventions ported from LongMemEval's evaluate_qa.py:
//   - Question, gold answer, and hypothesis are passed verbatim.
//   - Judge returns a binary correct/incorrect verdict + short
//     justification. We extend the upstream contract with a stable
//     JSON envelope so the parser does not have to chase free-form
//     "Correct."/"Incorrect." replies.
//
// Why JSON: small judge models (Haiku 4.5 still counts here) emit
// fewer parse errors when asked for `{"correct": true|false, ...}`
// than when asked for natural-language verdicts. The semantic
// content of the prompt is identical to upstream — only the output
// envelope changes.
const JudgePromptTemplate = `You are evaluating whether a candidate answer matches a known-correct gold answer for a long-horizon memory benchmark.

The question was asked at a specific date and may reference prior conversations the assistant should remember.

Question (asked %s): %s
Gold answer: %s

Candidate answer:
%s

Judge whether the candidate answer is correct. Apply these rules:
  1. The candidate is CORRECT if it captures the same factual content as the gold answer. Minor paraphrasing, extra detail, and different wording are fine as long as the gold answer's information is present and not contradicted.
  2. The candidate is INCORRECT if it gives a different fact, contradicts the gold answer, hallucinates information, or fails to commit to an answer when the gold answer is concrete.
  3. ABSTENTION: When the gold answer is "abstain" (or any clear refusal-to-answer marker), the candidate is CORRECT only if it also abstains, says it does not know, or refuses to invent a fact. Confidently asserting a wrong fact is INCORRECT.

Return ONLY valid JSON with no markdown fencing or surrounding text:
{"correct": true|false, "reason": "one short sentence"}`

// JudgeSystemPrompt frames the judge as a strict grader returning only
// JSON. Kept short — long system prompts cost tokens and small judges
// ignore them anyway.
const JudgeSystemPrompt = "You are a strict evaluation judge for long-horizon memory QA. Return only the JSON object specified by the user prompt. No prose, no markdown."

// JudgeVerdict is the parsed result of one judge call.
type JudgeVerdict struct {
	Correct bool   `json:"correct"`
	Reason  string `json:"reason"`
}

// BuildJudgePrompt fills the template with one question's data.
// Exported so tests and adjacent tooling can reproduce the exact
// prompt the judge will see.
func BuildJudgePrompt(q Question, hypothesis string) string {
	return fmt.Sprintf(JudgePromptTemplate, q.QuestionDate, q.Question, q.Answer, hypothesis)
}

// Judge calls the LLM judge once and returns a parsed verdict.
//
// On parse failure, the function returns the raw response wrapped in
// the error so the caller can log it and decide whether to retry or
// fail-closed. A nil judge is a programming error (the caller forgot
// to wire --judge); the function reports it explicitly rather than
// silently treating absence as "pass".
func Judge(ctx context.Context, judge llm.Provider, q Question, hypothesis string) (*JudgeVerdict, error) {
	if judge == nil {
		return nil, fmt.Errorf("judge provider is nil — pass --judge to enable scoring")
	}
	prompt := BuildJudgePrompt(q, hypothesis)
	raw, err := judge.GenerateWithSystem(ctx, prompt, JudgeSystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("judge generate: %w", err)
	}
	v, perr := parseJudgeVerdict(raw)
	if perr != nil {
		return nil, fmt.Errorf("parse judge response: %w (raw: %s)", perr, raw)
	}
	return v, nil
}

// parseJudgeVerdict tolerates the three failure modes we see from
// small judges:
//  1. Clean JSON.
//  2. JSON wrapped in markdown ```json fences.
//  3. Leading prose followed by JSON ("Verdict: {...}").
//
// Any other shape returns an error; callers should not silently treat
// unparseable as "incorrect" because that would mask judge regressions.
func parseJudgeVerdict(raw string) (*JudgeVerdict, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty response")
	}
	// First try direct unmarshal.
	var v JudgeVerdict
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return &v, nil
	}
	// Then extract the first {...} block.
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found")
	}
	candidate := trimmed[start : end+1]
	if err := json.Unmarshal([]byte(candidate), &v); err != nil {
		return nil, err
	}
	return &v, nil
}
