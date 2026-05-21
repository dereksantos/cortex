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

// Overview is the structured form of one file/chunk's project-overview
// summary. Matches the JSON shape declared in
// pkg/cognition/prompts/maintain_extract_overview.tmpl.
type Overview struct {
	Role         string   `json:"role"`
	Summary      string   `json:"summary"`
	Exports      []string `json:"exports"`
	Dependencies []string `json:"dependencies"`
	Importance   float64  `json:"importance"`
}

// ExtractOverviewConfig wires the handler to a provider. Provider may
// be nil — the handler then runs the mechanical fallback.
type ExtractOverviewConfig struct {
	Provider llm.Provider
}

// ExtractOverviewSpec returns the NodeSpec for maintain.extract_overview.
// LLM-backed op tuned for project-bootstrap intent (architectural
// summary, not session-event extraction). Deterministic fallback when
// provider is unavailable or budget too thin.
func ExtractOverviewSpec(cfg ExtractOverviewConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "extract_overview",
		Description: "summarize a file/chunk for project-overview retrieval (bootstrap-targeted)",
		Inputs: []dag.ParamSpec{
			{Name: "content", Type: "string", Required: true},
			{Name: "source", Type: "string", Required: false},
			{Name: "lang_hint", Type: "string", Required: false},
			{Name: "file_role_hint", Type: "string", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "overview", Type: "Overview"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    extractOverviewCostHint,
		Handler: NewExtractOverviewHandler(cfg),
	}
}

// extractOverviewCostHint mirrors extract_insight (`maintain_extract_insight.go:61`)
// — same prompt + completion shape, so the calibrated 18s wall / 400
// tokens floor applies until we observe otherwise.
var extractOverviewCostHint = dag.Cost{LatencyMS: 18000, Tokens: 400}

// NewExtractOverviewHandler returns a dag.Handler for maintain.extract_overview.
//
// Inputs:
//   - content (string)        — required; the file/chunk body
//   - source (string)         — optional; provenance tag (e.g. "bootstrap:pkg/foo.go:abc")
//   - lang_hint (string)      — optional; "go" / "py" / "md" / ...
//   - file_role_hint (string) — optional; "source"/"config"/"test"/"doc"
//
// Outputs:
//   - overview (Overview)     — single summary
//   - fallback (bool)         — true when the mechanical path ran
//
// Self-modulates: when budget.LatencyMS < fallbackBelowLatencyMS or
// no provider is configured, runs the mechanical fallback (first
// non-blank line as summary, role=lang_hint or "other", empty exports
// and dependencies, importance 0.5).
func NewExtractOverviewHandler(cfg ExtractOverviewConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		content := readString(in, "content")
		if content == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("maintain.extract_overview: 'content' (string) is required")
		}
		source := readString(in, "source")
		if source == "" {
			source = "unknown"
		}
		langHint := readString(in, "lang_hint")
		if langHint == "" {
			langHint = "unknown"
		}
		fileRoleHint := readString(in, "file_role_hint")
		if fileRoleHint == "" {
			fileRoleHint = "source"
		}

		// Mechanical fallback path.
		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			ov := mechanicalOverview(content, langHint, fileRoleHint)
			return dag.NodeResult{
				Out: map[string]any{
					"overview": ov,
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("maintain_extract_overview")
		if terr != nil {
			ov := mechanicalOverview(content, langHint, fileRoleHint)
			return dag.NodeResult{
				Out: map[string]any{
					"overview": ov,
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		prompt, rerr := pt.Render(map[string]any{
			"content":        content,
			"source":         source,
			"lang_hint":      langHint,
			"file_role_hint": fileRoleHint,
		})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("maintain.extract_overview: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			ov := mechanicalOverview(content, langHint, fileRoleHint)
			return dag.NodeResult{
				Out: map[string]any{
					"overview": ov,
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		ov, perr := parseOverviewResponse(resp)
		if perr != nil {
			ov = mechanicalOverview(content, langHint, fileRoleHint)
			return dag.NodeResult{
				Out: map[string]any{
					"overview": ov,
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		return dag.NodeResult{
			Out: map[string]any{
				"overview": ov,
				"fallback": false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

// parseOverviewResponse extracts the Overview JSON envelope from
// anywhere in the response, tolerating leading or trailing prose.
// Validates role is non-empty + importance is in [0,1] + caps
// exports/dependencies at 5 entries (the prompt asks for ≤5, but
// stragglers happen).
func parseOverviewResponse(resp string) (Overview, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return Overview{}, fmt.Errorf("no JSON object found in response")
	}
	var ov Overview
	if err := json.Unmarshal([]byte(resp[start:end+1]), &ov); err != nil {
		return Overview{}, fmt.Errorf("unmarshal: %w", err)
	}
	if strings.TrimSpace(ov.Summary) == "" {
		return Overview{}, fmt.Errorf("empty summary")
	}
	if ov.Role == "" {
		ov.Role = "other"
	}
	if ov.Importance < 0 {
		ov.Importance = 0
	}
	if ov.Importance > 1 {
		ov.Importance = 1
	}
	ov.Exports = capStrings(ov.Exports, 5)
	ov.Dependencies = capStrings(ov.Dependencies, 5)
	return ov, nil
}

// capStrings trims the slice to at most n entries and drops empties.
func capStrings(in []string, n int) []string {
	out := make([]string, 0, n)
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
		if len(out) >= n {
			break
		}
	}
	return out
}

// mechanicalOverview is the deterministic fallback. Builds an
// Overview from the first non-blank line of content (capped to 120
// chars), role from the hint, no exports or dependencies, importance
// 0.5. Never invents content — surfaces only what's in the input.
func mechanicalOverview(content, langHint, fileRoleHint string) Overview {
	summary := firstNonBlankLine(content)
	if len(summary) > 120 {
		summary = summary[:117] + "..."
	}
	role := fileRoleHint
	if role == "" || role == "unknown" {
		role = "source"
	}
	// Map a couple of common lang hints to roles when the caller
	// didn't supply a useful hint.
	if fileRoleHint == "source" {
		switch langHint {
		case "md", "txt", "rst":
			role = "doc"
		case "toml", "yaml", "ini":
			role = "config"
		}
	}
	return Overview{
		Role:         role,
		Summary:      summary,
		Exports:      nil,
		Dependencies: nil,
		Importance:   0.5,
	}
}

// firstNonBlankLine returns the first line of s whose trimmed form
// has non-zero length, or "(empty)" if every line is blank.
func firstNonBlankLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			return t
		}
	}
	return "(empty)"
}
