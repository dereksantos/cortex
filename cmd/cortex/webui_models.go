// webui_models.go — M5.2d: the models view-model (GOAL.md §6 M5.2, split
// into M5.2a/b/c/d per STATE.md; GOAL.md §2 names cmd/cortex/webui*.go as
// home for the web UI's Go view-model builders). Per GOAL.md §3 P5,
// rendering logic lives in golden-tested Go view-models — this file is pure
// data assembly, no HTML/JS.
//
// docs/cortex-web.md Phase 5 asks for "role bindings ... an effective-model
// column showing where each binding resolves from." Config.resolveBinding
// (config.go, the exact logic GET /api/models already runs — M4.2c2a,
// serve_models.go) already collapses an explicit config.Models[role].Model
// and an auto-selected fleet candidate (selectModel) into the same Model
// field, so "where from" isn't something the existing /api/models response
// shape (modelsResponse) carries — it's new presentation logic this
// view-model computes. That means, unlike M5.2c's landscape screen (a pure
// pass-through of ScanReport, the same shape /api/landscape already
// returns), this is a genuinely new shape — the same relationship M5.2a's
// dashboard has to no pre-existing project-list endpoint. serve_models.go's
// modelsResponse and handleModels are left untouched by this increment; a
// route wiring this view-model into the HTTP surface (if the eventual JS
// screen needs one distinct from GET /api/models) is deferred to M5.3/M5.4.
//
// Scope note: like M4.2c2a's GET /api/models, this covers ONE config layer
// (whichever configPath-equivalent *Config the caller resolved) — not the
// three-scope (user/project/session) switcher docs/cortex-web.md's Phase 5
// text describes. That switcher is UI-level scope selection built on
// M4.2c2b's already-existing scope-write endpoints (?scope=user|project|
// session); this increment's job is the per-role "source" presentation
// logic, reusable at whichever scope's resolved *Config the JS screen asks
// for.
package main

import "sort"

// Binding sources: where resolveBinding's Model field actually came from.
const (
	bindingSourceConfigured = "configured" // explicit models.<role>.model in config
	bindingSourceFleetAuto  = "fleet-auto" // selectModel picked it from the discovered fleet
	bindingSourceUnbound    = "unbound"    // neither a config entry nor a fleet candidate
)

// modelBindingView is one role's effective binding plus its source.
type modelBindingView struct {
	Role    string    `json:"role"`
	Binding ModelSpec `json:"binding"`
	Source  string    `json:"source"`
}

// modelsViewModel is the full models screen: every known role's binding,
// sorted by role name for a deterministic render order (rolePolicies is a
// map — mirrors dashboardViewModel's registry-sort precedent), plus the
// discovered fleet to pick a new binding from.
type modelsViewModel struct {
	Bindings []modelBindingView `json:"bindings"`
	Fleet    Fleet              `json:"fleet"`
}

// buildModelsViewModel composes every known role's effective binding
// (Config.resolveBinding, unchanged) with a Source: "configured" when cfg
// has an explicit non-empty models.<role>.model entry, "fleet-auto" when
// resolveBinding filled the model in from the fleet instead, "unbound" when
// neither produced one. cfg may be nil (resolveBinding, and this function,
// both handle it — a missing config file is not an error at this layer,
// matching loadMergedConfig's own nil-returning behavior).
func buildModelsViewModel(cfg *Config, fleet Fleet) modelsViewModel {
	roles := make([]string, 0, len(rolePolicies))
	for role := range rolePolicies {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	vm := modelsViewModel{Bindings: make([]modelBindingView, 0, len(roles)), Fleet: fleet}
	for _, role := range roles {
		spec := cfg.resolveBinding(role, fleet)

		source := bindingSourceUnbound
		if cfg != nil {
			if m, ok := cfg.Models[role]; ok && m.Model != "" {
				source = bindingSourceConfigured
			}
		}
		if source == bindingSourceUnbound && spec.Model != "" {
			source = bindingSourceFleetAuto
		}

		vm.Bindings = append(vm.Bindings, modelBindingView{Role: role, Binding: spec, Source: source})
	}
	return vm
}
