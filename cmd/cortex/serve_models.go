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
//
// M4.2c2b1 adds PUT /api/models/{role}?scope=user|project — the file-backed
// half of the scoped role-binding write GOAL.md §6 M4.2 / docs/
// cortex-web.md Phase 4 call for.
//
// M4.2c2b2 adds scope=session&session=<id> — the in-memory-only half
// (docs/cortex-web.md Phase 4: "session (in-memory only, reverts on
// resume — the API form of /model)"). Confirmed against main.go's own
// "/model" handler (session.SetModel, session_core.go:106) that only
// cs.Request.Model has a session-level override today — SetModel mutates
// that field alone, never a config file — so session scope accepts
// role=code only; any other role at scope=session is a 400, same shape as
// an unsupported scope/role combination elsewhere in this handler. No
// config-file I/O happens for this leg at all; the write lands directly on
// the live *managedSession the SessionManager already tracks (mgr, the
// same seam handleTurn/handleTurnStream use), so it reverts for free the
// next time the id is resumed from its transcript (which never recorded
// the override) — no explicit "clear on resume" logic is needed.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/dereksantos/cortex/internal/registry"
)

// modelsResponse is GET /api/models' body: every known role's effective
// binding (Config.resolveBinding over the merged config + fleet), a
// per-role Source naming where that binding came from (bindingSource,
// webui_models.go — "configured"/"fleet-auto"/"unbound", the same
// presentation logic buildModelsViewModel uses), plus the discovered fleet
// itself.
type modelsResponse struct {
	Roles   map[string]ModelSpec `json:"roles"`
	Sources map[string]string    `json:"sources"`
	Fleet   Fleet                `json:"fleet"`
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
		sources := make(map[string]string, len(rolePolicies))
		for role := range rolePolicies {
			spec := cfg.resolveBinding(role, fleet)
			roles[role] = spec
			sources[role] = bindingSource(cfg, role, spec)
		}
		writeJSON(w, http.StatusOK, modelsResponse{Roles: roles, Sources: sources, Fleet: fleet})
	}
}

// PersistModelBinding writes fields (JSON field names matching ModelSpec's
// own tags — "model", "endpoint", "key_env", …) under models.<role> at
// path, via read-modify-write (configwrite.go): every other top-level
// field, every other role, and every field of this role NOT present in
// fields survives untouched (GOAL.md §2) — this writes exactly the fields
// the request body set, never a whole-object clobber.
func PersistModelBinding(path, role string, fields map[string]json.RawMessage) error {
	doc, err := readJSONDoc(path)
	if err != nil {
		return fmt.Errorf("failed to persist model binding: %w", err)
	}
	for field, raw := range fields {
		if err := setJSONPath(doc, []string{"models", role, field}, raw); err != nil {
			return fmt.Errorf("failed to persist model binding: %w", err)
		}
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return fmt.Errorf("failed to persist model binding: %w", err)
	}
	return nil
}

// knownRole reports whether role is one of the fixed role codes config.go's
// rolePolicies recognizes — the same set handleModels iterates.
func knownRole(role string) bool {
	_, ok := rolePolicies[role]
	return ok
}

// setModelBindingResponse is PUT /api/models/{role}'s response body: the
// resulting binding at the written scope, read back from disk (not just
// echoed) so the client sees exactly what was persisted.
type setModelBindingResponse struct {
	Role    string    `json:"role"`
	Scope   string    `json:"scope"`
	Binding ModelSpec `json:"binding"`
}

// handleSetModelBinding serves PUT /api/models/{role}?scope=user|project|
// session[&project=<name>][&session=<id>]: a scoped role-binding write.
// configPath is user scope (the same single path threaded into newServeMux
// for GET /api/models); project scope resolves the named project's root via
// reg and writes <root>/.cortex/config.json, matching findConfigPath's
// on-disk layout (config.go); session scope (role=code only — see the
// package doc comment) sets the live *managedSession's model via mgr,
// in-memory only, no file I/O. Re-keying (re-running the Phase 1 bootstrap
// chain) is explicitly out of scope (GOAL.md §1 Deferred) — the file-backed
// scopes only ever write the fields present in the request body.
func handleSetModelBinding(configPath string, reg registry.Registry, mgr *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := r.PathValue("role")
		if !knownRole(role) {
			http.Error(w, "unknown role: "+role, http.StatusBadRequest)
			return
		}

		scope := r.URL.Query().Get("scope")
		if scope == "session" {
			handleSetSessionModelBinding(w, r, role, mgr)
			return
		}

		var target string
		switch scope {
		case "user":
			target = configPath
		case "project":
			name := r.URL.Query().Get("project")
			if name == "" {
				http.Error(w, "project scope requires ?project=<name>", http.StatusBadRequest)
				return
			}
			proj, err := reg.Lookup(name)
			if err != nil {
				if errors.Is(err, registry.ErrProjectNotFound) {
					http.Error(w, "project not registered: "+name, http.StatusNotFound)
					return
				}
				http.Error(w, "failed to look up project: "+err.Error(), http.StatusInternalServerError)
				return
			}
			target = filepath.Join(proj.Root, ".cortex", "config.json")
		default:
			http.Error(w, "unsupported scope: "+scope+" (want user, project, or session)", http.StatusBadRequest)
			return
		}

		var fields map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			http.Error(w, "failed to decode request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := PersistModelBinding(target, role, fields); err != nil {
			http.Error(w, "failed to persist model binding: "+err.Error(), http.StatusInternalServerError)
			return
		}

		doc, err := readJSONDoc(target)
		if err != nil {
			http.Error(w, "failed to read back persisted binding: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var models map[string]ModelSpec
		if raw, ok := doc["models"]; ok {
			if err := json.Unmarshal(raw, &models); err != nil {
				http.Error(w, "failed to decode persisted models: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, setModelBindingResponse{Role: role, Scope: scope, Binding: models[role]})
	}
}

// handleSetSessionModelBinding is scope=session's handler, split out of
// handleSetModelBinding since it's a wholly different mechanism (no config
// file involved at all): resolves &session=<id> against mgr's live map and
// mutates the *managedSession directly via CortexSession.SetModel — the
// exact same call main.go's REPL "/model" command makes
// (session_core.go:106), which is why session scope is documented as "the
// API form of /model". Only role=code is accepted: SetModel is the only
// session-level override the codebase has today (it mutates
// cs.Request.Model alone), so any other role has no live field to write and
// refuses 400 rather than silently no-op-ing.
func handleSetSessionModelBinding(w http.ResponseWriter, r *http.Request, role string, mgr *SessionManager) {
	if role != roleCode {
		http.Error(w, "session scope supports role \""+roleCode+"\" only (no session-level override exists for role: "+role+")", http.StatusBadRequest)
		return
	}
	id := r.URL.Query().Get("session")
	if id == "" {
		http.Error(w, "session scope requires ?session=<id>", http.StatusBadRequest)
		return
	}
	ms, ok := mgr.Get(id)
	if !ok {
		http.Error(w, "session not live: "+id, http.StatusNotFound)
		return
	}

	var fields map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		http.Error(w, "failed to decode request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var model string
	if raw, ok := fields["model"]; ok {
		if err := json.Unmarshal(raw, &model); err != nil {
			http.Error(w, "failed to decode \"model\" field: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	ms.mu.Lock()
	ms.cs.SetModel(model)
	got := ms.cs.Request.Model
	ms.mu.Unlock()

	writeJSON(w, http.StatusOK, setModelBindingResponse{Role: role, Scope: "session", Binding: ModelSpec{Model: got}})
}
