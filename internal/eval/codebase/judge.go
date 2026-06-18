package codebase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
//
// Workdir + MaxGroundingBytes enable the slice-3-followup "code-grounded
// judge": when both are set, the judge prompt includes the contents of
// the cited files (capped at MaxGroundingBytes) so it can verify symbol
// existence and line numbers instead of pattern-matching. Without
// grounding the judge has to trust or reject pure text — high false-
// negative rate on any plausible-sounding but unverifiable claim.
type JudgeOptions struct {
	Provider          llm.Provider
	Model             string // informational; provider already pinned to a model
	Workdir           string // root for resolving fixture.must_cite_paths
	MaxGroundingBytes int    // 0 = no grounding; positive caps total bytes injected
}

// DefaultMaxGroundingBytes caps the source-code context the judge sees
// per fixture. ~12K chars ≈ ~3K tokens — enough for one or two small
// source files, small enough to keep the judge's context window
// uncontended. Files larger than this get head-truncated with a
// "[…N bytes omitted]" marker.
const DefaultMaxGroundingBytes = 12000

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
// When opts.Workdir + opts.MaxGroundingBytes are set and the fixture
// supplies must_cite_paths, those files are injected as additional
// context so the judge can verify symbol/line claims against actual
// source instead of pattern-matching. This is the slice-3 follow-up
// without which the judge false-negatives on most plausible answers.
//
// Returns (nil, nil) when opts.Provider is nil — the runner uses that
// to skip the judge in mechanical-only mode.
func Judge(ctx context.Context, prompt, answer, rubric string, opts JudgeOptions) (*JudgeResult, error) {
	return JudgeWithFixture(ctx, prompt, answer, rubric, nil, opts)
}

// JudgeWithFixture is Judge's variant that also takes the Fixture so the
// must_cite_paths grounding can be wired in. Judge() delegates here with
// fx=nil for callers that don't want grounding (or didn't set Workdir).
func JudgeWithFixture(ctx context.Context, prompt, answer, rubric string, fx *Fixture, opts JudgeOptions) (*JudgeResult, error) {
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

	const systemPrompt = `You are an LLM judge evaluating an AI coding agent's answer to a codebase-reading question. You have access to the actual source files when relevant. Return ONLY valid JSON, no markdown.`

	groundingBlock := ""
	if fx != nil && opts.Workdir != "" && opts.MaxGroundingBytes > 0 {
		groundingBlock = loadGrounding(opts.Workdir, fx.Expected.MustCitePaths, opts.MaxGroundingBytes)
	}

	userPrompt := fmt.Sprintf(`User asked the agent:
%s

Agent's answer:
%s

Substantive-correctness rubric:
%s
%s
Decide:
- pass: true if the answer correctly resolves the user's question AND every cited file/line/symbol is verifiable against the source above. false if the answer is wrong, evasive, fabricates a symbol/line, or contradicts the source.
- reason: one sentence stating WHY you chose pass/fail. Quote the rubric phrase or the source line that drove the decision.
- hallucination_flag: true if you spotted any fabricated file path, line number, function name, or behavior description — verified against the source above when grounding is supplied.

Important: when source grounding is supplied above, trust IT over your priors. A symbol that appears in the grounding IS real; an answer that names a symbol absent from the grounding (and absent from the rubric) is hallucinated.

Return only this JSON shape:
{"pass": true|false, "reason": "...", "hallucination_flag": true|false}`,
		prompt, answer, rubric, groundingBlock)

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

// loadGrounding resolves each path in mustCitePaths against workdir
// and returns a single source-context block to splice into the judge
// prompt. Directory entries are expanded one level deep (so a fixture
// citing `pkg/cognition/dag/` doesn't drag in megabytes of recursive
// children). Files exceeding the per-file budget are head-truncated
// with a marker.
//
// Returns "" when no real files resolve — the caller then runs the
// judge without grounding (same shape as before this hook existed).
func loadGrounding(workdir string, mustCitePaths []string, maxBytes int) string {
	if len(mustCitePaths) == 0 || maxBytes <= 0 {
		return ""
	}
	type fileChunk struct {
		Path  string
		Bytes []byte
	}
	var (
		seen   = map[string]bool{}
		chunks []fileChunk
		budget = maxBytes
	)
	// Per-file budget = maxBytes / N — gives each file a fair slice
	// when several are cited.
	perFile := maxBytes / max(1, len(mustCitePaths))
	if perFile < 512 {
		perFile = 512
	}

	for _, p := range mustCitePaths {
		if budget <= 0 {
			break
		}
		clean := strings.TrimPrefix(p, "./")
		full := filepath.Join(workdir, clean)
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		if st.IsDir() {
			// One level deep, sorted, source-like extensions only.
			entries, _ := os.ReadDir(full)
			names := []string{}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				if !isSourceLike(name) {
					continue
				}
				names = append(names, name)
			}
			sort.Strings(names)
			for _, n := range names {
				if budget <= 0 {
					break
				}
				rel := filepath.Join(clean, n)
				if seen[rel] {
					continue
				}
				b := readCapped(filepath.Join(full, n), perFile)
				if len(b) == 0 {
					continue
				}
				chunks = append(chunks, fileChunk{Path: rel, Bytes: b})
				seen[rel] = true
				budget -= len(b)
			}
			continue
		}
		if seen[clean] {
			continue
		}
		b := readCapped(full, perFile)
		if len(b) == 0 {
			continue
		}
		chunks = append(chunks, fileChunk{Path: clean, Bytes: b})
		seen[clean] = true
		budget -= len(b)
	}

	if len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nSource grounding (cited files; head-truncated to fit the judge context):\n")
	for _, c := range chunks {
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", c.Path, c.Bytes)
	}
	b.WriteString("\n--- end of source grounding ---\n")
	return b.String()
}

func readCapped(path string, max int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, max)
	n, _ := f.Read(buf)
	if n <= 0 {
		return nil
	}
	if n == max {
		// Indicate truncation so the judge knows the file is longer.
		marker := []byte("\n…[truncated; head only]\n")
		return append(buf[:n], marker...)
	}
	return buf[:n]
}

func isSourceLike(name string) bool {
	for _, ext := range []string{".go", ".js", ".mjs", ".ts", ".tsx", ".py", ".rs", ".md", ".json", ".yaml", ".yml", ".toml", ".sh", ".rb"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
