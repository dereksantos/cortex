// Package ops — catalog formatter.
//
// FormatOpCatalog produces a compact text catalogue of the ops a
// steering layer (decide.next) advertises to its LLM. Each line shows
// qualified name, required input parameters, and the description from
// registration. Optional inputs are flagged with `?` so the LLM knows
// what's safe to omit.
//
// Filters to specs marked Exposable=true. Sorts by qualified name for
// deterministic output (regression-friendly + cache-stable).
package ops

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// FormatOpCatalog renders the exposable ops in reg as a compact list
// suitable for injection into a system prompt. Returns the empty
// string when no ops are marked exposable.
//
// Format per line:
//
//	<function>.<op>(param1, param2?, param3) - description
//
// Optional parameters get a trailing `?`. Description is truncated to
// 80 chars to keep the catalogue scannable. The whole catalogue is
// indented by two spaces so it nests naturally inside a system prompt.
func FormatOpCatalog(reg *dag.Registry) string {
	if reg == nil {
		return ""
	}
	specs := reg.Exposable()
	if len(specs) == 0 {
		return ""
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].QualifiedName() < specs[j].QualifiedName()
	})

	var b strings.Builder
	for _, s := range specs {
		fmt.Fprintf(&b, "  %s(%s) - %s\n",
			s.QualifiedName(),
			formatParams(s.Inputs),
			truncateOneLine(s.Description, 80),
		)
	}
	return b.String()
}

func formatParams(params []dag.ParamSpec) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		if p.Required {
			parts = append(parts, p.Name)
		} else {
			parts = append(parts, p.Name+"?")
		}
	}
	return strings.Join(parts, ", ")
}

// truncateOneLine collapses newlines to spaces and truncates to n
// characters with an ellipsis. Keeps the catalogue scannable when an
// op's Description is verbose.
func truncateOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
