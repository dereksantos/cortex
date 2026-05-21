// Package commands — adapters between the LLM-aware extract ops
// (pkg/cognition/dag/ops) and the controller's ExtractFunc shape
// (internal/bootstrap). Lifted out of bootstrap.go so both
// `cortex bootstrap` and `cortex study` share one wiring without
// duplication.
package commands

import (
	"context"
	"strings"

	"github.com/dereksantos/cortex/internal/bootstrap"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/llm"
)

// wrapInsightFn adapts maintain.extract_insight's NodeSpec.Handler to
// the controller's ExtractFunc shape. The conversion is one-to-many
// in principle (insight emits 0-2); here we forward every entry.
func wrapInsightFn(provider llm.Provider) bootstrap.ExtractFunc {
	spec := ops.ExtractInsightSpec(ops.ExtractInsightConfig{Provider: provider})
	return func(ctx context.Context, content, source, langHint, fileRoleHint string) ([]bootstrap.ExtractedInsight, bool, error) {
		in := map[string]any{
			"content": content,
			"source":  source,
		}
		// extract_insight does not take lang/role hints, ignore them.
		_ = langHint
		_ = fileRoleHint
		res, err := spec.Handler(ctx, in, dag.Budget{LatencyMS: 60000, Tokens: 1500, Depth: 5})
		if err != nil {
			return nil, false, err
		}
		fb, _ := res.Out["fallback"].(bool)
		insights, _ := res.Out["insights"].([]ops.Insight)
		out := make([]bootstrap.ExtractedInsight, 0, len(insights))
		for _, i := range insights {
			out = append(out, bootstrap.ExtractedInsight{
				Content:    i.Content,
				Category:   i.Category,
				Importance: i.Importance,
			})
		}
		return out, fb, nil
	}
}

// wrapOverviewFn adapts maintain.extract_overview's handler to the
// controller's ExtractFunc shape. The Overview struct collapses into
// a single ExtractedInsight whose Tags carry exports + dependencies
// and Category encodes the role.
func wrapOverviewFn(provider llm.Provider) bootstrap.ExtractFunc {
	spec := ops.ExtractOverviewSpec(ops.ExtractOverviewConfig{Provider: provider})
	return func(ctx context.Context, content, source, langHint, fileRoleHint string) ([]bootstrap.ExtractedInsight, bool, error) {
		in := map[string]any{
			"content":        content,
			"source":         source,
			"lang_hint":      langHint,
			"file_role_hint": fileRoleHint,
		}
		res, err := spec.Handler(ctx, in, dag.Budget{LatencyMS: 60000, Tokens: 1500, Depth: 5})
		if err != nil {
			return nil, false, err
		}
		fb, _ := res.Out["fallback"].(bool)
		ov, _ := res.Out["overview"].(ops.Overview)
		if strings.TrimSpace(ov.Summary) == "" {
			return nil, fb, nil
		}
		tags := make([]string, 0, len(ov.Exports)+len(ov.Dependencies))
		tags = append(tags, ov.Exports...)
		tags = append(tags, ov.Dependencies...)
		category := "overview"
		if ov.Role != "" {
			category = "overview:" + ov.Role
		}
		return []bootstrap.ExtractedInsight{{
			Content:    ov.Summary,
			Category:   category,
			Importance: ov.Importance,
			Tags:       tags,
		}}, fb, nil
	}
}
