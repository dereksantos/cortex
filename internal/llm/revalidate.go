// Package llm — REPL-launch revalidation of the saved role map.
//
// At session start, probe each endpoint referenced by the role map to
// confirm (a) the endpoint is reachable and (b) the pinned model is
// still in its catalog. Surfaces stale entries as one-line warnings;
// never blocks the REPL.
//
// Phase 4 Slice E.

package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// StaleRoleEntry describes one role-map entry that failed
// revalidation. Reason is human-readable (printed verbatim by the
// REPL); Role identifies which role-map slot is affected.
type StaleRoleEntry struct {
	Role     string
	Endpoint string
	Model    string
	Reason   string
}

// RevalidateRoleMap probes the role-map's referenced endpoints with a
// short timeout. Returns one StaleRoleEntry per stale assignment; an
// empty result means the map is fully routable.
//
// Endpoints not referenced by the role map are ignored — re-validating
// every configured endpoint would be wasteful on a session that only
// uses one role.
func RevalidateRoleMap(cfg *config.Config) []StaleRoleEntry {
	if cfg == nil || cfg.Models == nil {
		return nil
	}
	assignments := []struct {
		role string
		ra   *config.RoleAssignment
	}{
		{"code", cfg.Models.Code},
		{"reason", cfg.Models.Reason},
		{"fast", cfg.Models.Fast},
		{"embed", cfg.Models.Embed},
		{"rerank", cfg.Models.Rerank},
	}

	// Group assignments by endpoint so we only probe each endpoint
	// once even when multiple roles share it.
	byEP := map[string][]struct {
		role  string
		model string
	}{}
	for _, a := range assignments {
		if a.ra == nil {
			continue
		}
		byEP[a.ra.Endpoint] = append(byEP[a.ra.Endpoint], struct {
			role  string
			model string
		}{role: a.role, model: a.ra.Model})
	}

	var stale []StaleRoleEntry
	for epName, entries := range byEP {
		ep := cfg.FindEndpoint(epName)
		if ep == nil {
			for _, e := range entries {
				stale = append(stale, StaleRoleEntry{
					Role: e.role, Endpoint: epName, Model: e.model,
					Reason: "endpoint not in config.endpoints (re-run `cortex models --save`)",
				})
			}
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		client := llm.NewOpenAICompatClient(llm.EndpointConfig{
			Name: ep.Name, BaseURL: ep.BaseURL, APIKey: ep.ResolveAPIKey(),
		})
		models, err := client.ListModels(ctx)
		cancel()
		if err != nil {
			for _, e := range entries {
				stale = append(stale, StaleRoleEntry{
					Role: e.role, Endpoint: epName, Model: e.model,
					Reason: fmt.Sprintf("endpoint unreachable: %s", shortErr(err)),
				})
			}
			continue
		}
		// Build a set for O(1) model lookups.
		have := make(map[string]struct{}, len(models))
		for _, m := range models {
			have[m.ID] = struct{}{}
		}
		for _, e := range entries {
			if _, ok := have[e.model]; !ok {
				stale = append(stale, StaleRoleEntry{
					Role: e.role, Endpoint: epName, Model: e.model,
					Reason: "model not in endpoint catalog (model unloaded? id changed?)",
				})
			}
		}
	}
	return stale
}

func shortErr(err error) string {
	s := err.Error()
	// Trim verbose framing from net/http errors so the REPL banner stays one line.
	if i := strings.Index(s, ": "); i > 0 && i < 80 {
		s = s[i+2:]
	}
	if len(s) > 100 {
		s = s[:100] + "..."
	}
	return s
}
