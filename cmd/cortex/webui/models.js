// models.js — the models screen. Fetches GET /api/models (serve_models.go
// — returns modelsResponse: every known role's effective binding, a
// per-role Source ("configured"/"fleet-auto"/"unbound"), and the discovered
// fleet, key material always absent) and renders it into the #models
// container using the design-system .scopes/.seg/table.loops/.lbox classes
// from app.css: a scope switcher (user/project/session, with the
// project/session inputs shown only when the selected scope needs them)
// plus a per-role text field + SET BY chip + Save button that PUTs a new
// binding via PUT /api/models/{role}?scope=user|project|session[&project=]
// [&session=]. Reuses el()/authToken()/apiFetch()/queryParam() from app.js
// as plain global functions — no module system, index.html loads app.js
// before this file.

// modelScopeState survives across loadModels() re-renders so the
// operator's scope choice isn't lost after a save. Seeded from the
// ?project=/&session= query params since an operator arriving from the
// session screen likely wants a session-scoped binding for that session.
const modelScopeState = {
  scope: "user",
  project: queryParam("project"),
  session: queryParam("session"),
};

// scopeInputVisibility shows the project input only for scope=project and
// the session input only for scope=session — a bare user-scope save has no
// use for either, and showing both unconditionally (the old behavior) reads
// as "fill in both fields" even when the selected scope only consults one.
function scopeInputVisibility(scope, projectWrap, sessionWrap) {
  projectWrap.style.display = scope === "project" ? "" : "none";
  sessionWrap.style.display = scope === "session" ? "" : "none";
}

// renderScopeSwitcher appends the scope <select> plus project/session text
// inputs, wiring their values into modelScopeState so the per-role save
// handlers (built after this call, in renderModels) read the operator's
// current choice at save time rather than at render time. The <select> is
// built via a direct createElement("select") call — a real native select
// is still how the scope choice is made.
function renderScopeSwitcher(container) {
  const select = document.createElement("select");
  for (const scope of ["user", "project", "session"]) {
    const opt = document.createElement("option");
    opt.value = scope;
    opt.textContent = scope;
    select.appendChild(opt);
  }
  select.value = modelScopeState.scope;

  const projectInput = el("input", { type: "text", placeholder: "project name", value: modelScopeState.project });
  projectInput.addEventListener("input", () => {
    modelScopeState.project = projectInput.value;
  });
  const projectWrap = el("span", {}, [projectInput]);

  const sessionInput = el("input", { type: "text", placeholder: "session id", value: modelScopeState.session });
  sessionInput.addEventListener("input", () => {
    modelScopeState.session = sessionInput.value;
  });
  const sessionWrap = el("span", {}, [sessionInput]);

  scopeInputVisibility(modelScopeState.scope, projectWrap, sessionWrap);
  select.addEventListener("change", () => {
    modelScopeState.scope = select.value;
    scopeInputVisibility(select.value, projectWrap, sessionWrap);
  });

  container.appendChild(
    el("div", { className: "scopes" }, [
      el("span", { className: "mono dim", textContent: "scope:" }),
      select,
      projectWrap,
      sessionWrap,
    ]),
  );
}

// saveBinding PUTs {"model": value} to /api/models/{role} at the current
// modelScopeState scope, writing the result into status and reloading the
// whole screen on success (full re-fetch-and-rerender, not a client-side
// patch).
function saveBinding(role, value, status) {
  status.textContent = "Saving…";
  let url = "/api/models/" + encodeURIComponent(role) + "?scope=" + encodeURIComponent(modelScopeState.scope);
  if (modelScopeState.scope === "project") {
    url += "&project=" + encodeURIComponent(modelScopeState.project);
  }
  if (modelScopeState.scope === "session") {
    url += "&session=" + encodeURIComponent(modelScopeState.session);
  }
  fetch(url, {
    method: "PUT",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + authToken() },
    body: JSON.stringify({ model: value }),
  })
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("PUT " + url + ": " + resp.status);
      }
      return resp.json();
    })
    .then(() => loadModels())
    .catch((err) => {
      status.textContent = "Failed to save: " + err.message;
    });
}

// sourceChip renders a role binding's Source (config.go's bindingSource*
// constants: "configured"/"fleet-auto"/"unbound") as a chip — configured is
// an explicit choice (amb, the same "operator set this" color the change
// chip uses), fleet-auto is a quiet default (mute), unbound is worth
// flagging (bad, since a turn in that role has nothing to call).
function sourceChip(source) {
  const cls = source === "configured" ? "amb" : source === "fleet-auto" ? "mute" : "bad";
  return el("span", { className: "chip " + cls, textContent: source || "unbound" });
}

// renderRoleRow appends one <tr> for a role: name, its bound model as a
// text field, the SET BY source chip, a Save button, and status.
function renderRoleRow(table, role, binding, source) {
  const input = el("input", { type: "text", value: binding.model || "" });
  const status = el("span", { className: "status-text" });
  const button = el("button", { type: "button", className: "btn", textContent: "Save" });
  button.addEventListener("click", () => saveBinding(role, input.value, status));

  table.appendChild(
    el("tr", {}, [
      el("td", { className: "mono strong", textContent: role }),
      el("td", {}, [input]),
      el("td", {}, [sourceChip(source)]),
      el("td", {}, [button, status]),
    ]),
  );
}

// renderFleet appends the discovered-fleet .lbox listing every known
// model name from modelsResponse.fleet.
function renderFleet(container, fleet) {
  const names = Object.keys(fleet || {}).sort();
  const rows = names.length
    ? names.map((name) => el("div", { className: "lrow" }, [el("span", { className: "mono", textContent: name })]))
    : [el("div", { className: "lrow" }, [el("span", { className: "dim", textContent: "None found." })])];
  container.appendChild(el("div", { className: "lbox" }, [el("h4", { textContent: "Discovered fleet" }), ...rows]));
}

// renderModels writes modelsResponse's JSON shape ({roles, fleet}) into
// the #models container via plain DOM writes — textContent only, never
// innerHTML with response data.
function renderModels(vm, container) {
  container.textContent = "";
  renderScopeSwitcher(container);

  const table = el("table", { className: "loops" }, [
    el("tr", {}, ["role", "binding", "set by", ""].map((h) => el("th", { textContent: h }))),
  ]);
  const roleNames = Object.keys(vm.roles || {}).sort();
  const sources = vm.sources || {};
  for (const role of roleNames) {
    renderRoleRow(table, role, vm.roles[role] || {}, sources[role]);
  }
  container.appendChild(table);

  renderFleet(container, vm.fleet);
}

// loadModels no-ops when the #models container is absent/hidden.
function loadModels() {
  const container = document.getElementById("models");
  if (!container) {
    return;
  }
  container.textContent = "Loading models…";
  apiFetch("/api/models")
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("GET /api/models: " + resp.status);
      }
      return resp.json();
    })
    .then((vm) => renderModels(vm, container))
    .catch((err) => {
      container.textContent = "Failed to load models: " + err.message;
    });
}

loadModels();
