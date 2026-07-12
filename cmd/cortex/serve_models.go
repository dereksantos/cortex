// serve_models.go — M4.2c2a: GET /api/models, the read-only half of the
// models slice GOAL.md §6 M4.2 asks for (split M4.2c2 → M4.2c2a read /
// M4.2c2b scoped role-binding writes — STATE.md). Reads the merged config
// at configPath and the discovered fleet via discoverFleet (config.go,
// already exercised against a fake /model/info server by main_test.go's
// fleetServer helper) — no parallel fleet or binding-resolution logic.
//
// Key-absence (GOAL.md §6 M4.2: "key material absent from every
// response"): ModelSpec (config.go) only ever carries KeyEnv/KeyService
// (the SOURCE name — an env var or keychain service to read from), never a
// resolved key value; this handler calls Config.resolveBinding, never
// resolveKey, so there is no resolved key value in scope to leak.
package main

import "net/http"

// modelsResponse is GET /api/models' body: every known role's effective
// binding (Config.resolveBinding over the merged config + fleet) plus the
// discovered fleet itself.
type modelsResponse struct {
	Roles map[string]ModelSpec `json:"roles"`
	Fleet Fleet                `json:"fleet"`
}

// handleModels serves GET /api/models. configPath is the same single
// config path already threaded into newServeMux for /api/landscape
// (M4.2c1) — this read-only endpoint has no project segment in its route,
// so "merged config" here means that one file, not a user+project layer
// stack (a project-scoped view is M4.2c2b's concern, alongside the scoped
// writes).
func handleModels(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := loadMergedConfig(configPath, "")
		fleet := discoverFleet(r.Context(), cfg.backendEndpoint())
		roles := make(map[string]ModelSpec, len(rolePolicies))
		for role := range rolePolicies {
			roles[role] = cfg.resolveBinding(role, fleet)
		}
		writeJSON(w, http.StatusOK, modelsResponse{Roles: roles, Fleet: fleet})
	}
}
