package longmemeval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// hfOracleURL is the canonical mirror for the Oracle split. The dataset
// itself is MIT-licensed (see xiaowu0162/longmemeval-cleaned on HF Hub);
// vendoring the JSON would bloat the repo for diminishing returns.
const hfOracleURL = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_oracle.json"

// SubsetOracle is the only subset supported in this PR (Phase A).
// SubsetS / SubsetM are reserved for Phase B; Load() rejects them with
// a "Phase B" message so the failure mode is clear to operators.
const (
	SubsetOracle = "oracle"
	SubsetS      = "s"
	SubsetM      = "m"
)

// FilterKey constants for opts.Filter — keep all CLI-derived flags
// keyed through these so the package owns the contract.
const (
	FilterQuestionType = "question-type"
	FilterStrategy     = "strategy"
	FilterModel        = "model"
	FilterJudge        = "judge"
	FilterJudgeModel   = "judge-model"
)

// loadFile is overridable so tests can swap in a local fixture without
// touching the HTTP cache layer. Defaults to the cache-backed fetch.
var loadFile = defaultLoadFile

func defaultLoadFile() (string, error) {
	return benchmarks.EnsureCached("longmemeval", "longmemeval_oracle.json", hfOracleURL)
}

// Load returns one Instance per (question × strategy), honoring:
//   - opts.Subset: only "oracle" accepted (empty defaults to oracle).
//   - opts.Limit: caps the number of distinct questions, NOT cells.
//     (--limit 5 + --strategy baseline,cortex still emits 5 questions ×
//     2 strategies = 10 instances.)
//   - opts.Filter["question-type"]: comma-separated list of axis labels
//     (normalized via NormalizeAxis) OR raw upstream question_type
//     strings. Questions outside the filter are skipped pre-limit.
//   - opts.Filter["strategy"]: comma-separated list of "baseline" and/or
//     "cortex" (default "cortex"). Each strategy multiplies the
//     instance count.
//
// Errors:
//   - Unsupported Subset returns "Phase B: subset %q not yet wired".
//   - Unknown strategy returns "unknown strategy %q".
//   - Malformed JSON returns the unmarshal error wrapped with the path.
func Load(ctx context.Context, opts benchmarks.LoadOpts) ([]benchmarks.Instance, error) {
	subset := opts.Subset
	if subset == "" {
		subset = SubsetOracle
	}
	switch subset {
	case SubsetOracle:
		// supported
	case SubsetS, SubsetM:
		return nil, fmt.Errorf("Phase B: subset %q not yet wired (only %q in this release)", subset, SubsetOracle)
	default:
		return nil, fmt.Errorf("unknown subset %q (want %q)", subset, SubsetOracle)
	}

	strategies, err := parseStrategies(opts.Filter[FilterStrategy])
	if err != nil {
		return nil, err
	}

	questionFilter := parseQuestionTypeFilter(opts.Filter[FilterQuestionType])

	path, err := loadFile()
	if err != nil {
		return nil, fmt.Errorf("fetch oracle: %w", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var questions []Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Apply question-type filter (matches normalized axis OR raw
	// upstream string). Drop instances that don't match.
	if len(questionFilter) > 0 {
		filtered := questions[:0]
		for _, q := range questions {
			axis := NormalizeAxis(q.QuestionType)
			if _, axisOK := questionFilter[axis]; axisOK {
				filtered = append(filtered, q)
				continue
			}
			if _, rawOK := questionFilter[q.QuestionType]; rawOK {
				filtered = append(filtered, q)
				continue
			}
		}
		questions = filtered
	}

	// Stable order: by QuestionID. The upstream JSON happens to be
	// ordered, but relying on that across mirrors is fragile.
	sort.Slice(questions, func(i, j int) bool { return questions[i].QuestionID < questions[j].QuestionID })

	if opts.Limit > 0 && opts.Limit < len(questions) {
		questions = questions[:opts.Limit]
	}

	out := make([]benchmarks.Instance, 0, len(questions)*len(strategies))
	for _, q := range questions {
		for _, strat := range strategies {
			out = append(out, benchmarks.Instance{
				ID:      "longmemeval/" + q.QuestionID + ":" + strat,
				Payload: InstancePayload{Q: q, Strategy: strat},
			})
		}
	}
	return out, nil
}

// parseStrategies splits a comma-separated value into a deduplicated,
// validated, stably-ordered strategy list. Empty input returns the
// default (cortex only) so existing callers without --strategy keep
// the prior single-cell behavior.
func parseStrategies(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{StrategyCortex}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		switch s {
		case StrategyBaseline, StrategyCortex:
		default:
			return nil, fmt.Errorf("unknown strategy %q (want %q or %q)", s, StrategyBaseline, StrategyCortex)
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{StrategyCortex}, nil
	}
	return out, nil
}

// parseQuestionTypeFilter splits a comma-separated value into a set.
// Empty input returns nil (signaling "no filter").
func parseQuestionTypeFilter(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		out[s] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
