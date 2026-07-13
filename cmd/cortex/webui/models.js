// models.js — M5.3e: the models screen. Fetches GET /api/models
// (serve_models.go, live since M4.2c2a — returns modelsResponse: every known
// role's effective binding plus the discovered fleet, key material always
// absent) and renders it into the #models container: a scope switcher
// (user/project/session) plus a per-role text field + Save button that PUTs
// a new binding via M4.2c2b1/b2's PUT /api/models/{role}?scope=user|project|
// session[&project=<name>][&session=<id>] (serve_models.go). No new endpoint
// needed — the read+write surface was already complete before this screen.
// Kept as its own file per M5.3a's mechanical size caps — same reasoning
// M5.3c4/M5.3d used to split sse.js/landscape.js out. Reuses
// authToken()/apiFetch() from app.js as plain global functions (no module
// system, per D9) — index.html loads app.js before this file.

// modelScopeState survives across loadModels() re-renders (renderModels
// clears and rebuilds #models on every call) so the operator's scope choice
// isn't lost after a save. Seeded from the ?project=/&session= query params
// (authToken()'s ?token= precedent) since an operator arriving from the
// session screen likely wants to write a session-scoped binding for the
// session they were just looking at.
const modelScopeState = {
  scope: "user",
  project: queryParam("project"),
  session: queryParam("session"),
};

// renderScopeSwitcher appends the scope <select> plus project/session text
// inputs to container, wiring their values into modelScopeState so the
// per-role save handlers (built after this call, in renderModels) read the
// operator's current choice at save time rather than at render time.
function renderScopeSwitcher(container) {
  const row = document.createElement("div");

  const label = document.createElement("label");
  label.textContent = "Scope: ";
  row.appendChild(label);

  const select = document.createElement("select");
  for (const scope of ["user", "project", "session"]) {
    const opt = document.createElement("option");
    opt.value = scope;
    opt.textContent = scope;
    select.appendChild(opt);
  }
  select.value = modelScopeState.scope;
  select.addEventListener("change", () => {
    modelScopeState.scope = select.value;
  });
  row.appendChild(select);

  const projectInput = document.createElement("input");
  projectInput.type = "text";
  projectInput.placeholder = "project name (scope=project)";
  projectInput.value = modelScopeState.project;
  projectInput.addEventListener("input", () => {
    modelScopeState.project = projectInput.value;
  });
  row.appendChild(projectInput);

  const sessionInput = document.createElement("input");
  sessionInput.type = "text";
  sessionInput.placeholder = "session id (scope=session)";
  sessionInput.value = modelScopeState.session;
  sessionInput.addEventListener("input", () => {
    modelScopeState.session = sessionInput.value;
  });
  row.appendChild(sessionInput);

  container.appendChild(row);
}

// saveBinding PUTs {"model": value} to /api/models/{role} at the current
// modelScopeState scope, writing the result into status and reloading the
// whole screen on success (full re-fetch-and-rerender, matching
// renderTurnInput's/app.js's posture: not a client-side patch).
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
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + authToken(),
    },
    body: JSON.stringify({ model: value }),
  })
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("PUT " + url + ": " + resp.status);
      }
      return resp.json();
    })
    .then(() => {
      loadModels();
    })
    .catch((err) => {
      status.textContent = "Failed to save: " + err.message;
    });
}

// renderModels writes modelsResponse's JSON shape ({roles, fleet}) into the
// #models container via plain DOM writes — textContent only, never innerHTML
// with response data, matching renderDashboard's/renderLandscape's posture.
function renderModels(vm, container) {
  container.textContent = "";
  renderScopeSwitcher(container);

  const roleNames = Object.keys(vm.roles || {}).sort();
  const list = document.createElement("ul");
  for (const role of roleNames) {
    const binding = vm.roles[role] || {};
    const row = document.createElement("li");

    const name = document.createElement("strong");
    name.textContent = role + ": ";
    row.appendChild(name);

    const input = document.createElement("input");
    input.type = "text";
    input.value = binding.model || "";
    row.appendChild(input);

    const button = document.createElement("button");
    button.type = "button";
    button.textContent = "Save";
    row.appendChild(button);

    const status = document.createElement("span");
    row.appendChild(status);

    button.addEventListener("click", () => {
      saveBinding(role, input.value, status);
    });

    list.appendChild(row);
  }
  container.appendChild(list);

  const fleetHeading = document.createElement("h3");
  fleetHeading.textContent = "Discovered fleet";
  container.appendChild(fleetHeading);

  const fleetNames = Object.keys(vm.fleet || {}).sort();
  if (fleetNames.length === 0) {
    const empty = document.createElement("p");
    empty.textContent = "None found.";
    container.appendChild(empty);
    return;
  }
  const fleetList = document.createElement("ul");
  for (const name of fleetNames) {
    const item = document.createElement("li");
    item.textContent = name;
    fleetList.appendChild(item);
  }
  container.appendChild(fleetList);
}

// loadModels no-ops when the #models container is absent (dashboard/session/
// landscape-only pages), matching loadDashboard/loadSession/loadLandscape's
// posture.
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
