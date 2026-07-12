// Cortex web UI — fetch/render/SSE-append only (GOAL.md §3 P5). No
// framework, no build step (D9); every screen talks only to the Phase 4
// API, and rendering logic itself lives in the Go view-model builders
// (cmd/cortex/webui*.go) — this file just fetches JSON and writes it into
// the DOM.
//
// M5.3b — the dashboard screen: fetch('/api/dashboard') renders the
// registered projects (name, git branch/change status, session list) into
// the #dashboard container built by buildDashboardViewModel
// (cmd/cortex/webui_dashboard.go).
//
// M5.3c2 — the session screen: fetch('/api/projects/{name}/sessions/{id}')
// renders the transcript (buildTranscriptViewModel,
// cmd/cortex/webui_transcript.go) into the #session container. Read-only
// render only — the input box (M5.3c3) and live SSE (M5.3c4) are separate,
// later sub-items.

// queryParam reads a single query-string parameter from the page URL — the
// same window.location.search source authToken()'s "?token=" precedent
// uses, generalized for "?project=<name>&session=<id>".
function queryParam(name) {
  return new URLSearchParams(window.location.search).get(name) || "";
}

// authToken resolves the bearer token the API surface requires. A plain
// browser navigation can't attach a custom Authorization header, so the
// token rides in as a "?token=" query param on the page URL (the value
// `cortex serve`'s startup line prints) — read once here and reused for
// every /api/... fetch this page makes (see serve.go's authMiddleware,
// M5.3b Decisions Log).
function authToken() {
  return queryParam("token");
}

// apiFetch issues an authenticated GET against the given /api/... path.
function apiFetch(path) {
  return fetch(path, {
    headers: { Authorization: "Bearer " + authToken() },
  });
}

// renderDashboard writes a dashboard view-model (dashboardViewModel's JSON
// shape) into the #dashboard container via plain DOM writes — textContent
// only, never innerHTML with response data, since project names/paths are
// untrusted-ish local-filesystem strings.
function renderDashboard(vm, container) {
  container.textContent = "";
  if (!vm.projects || vm.projects.length === 0) {
    const empty = document.createElement("p");
    empty.textContent = "No registered projects yet.";
    container.appendChild(empty);
    return;
  }

  const list = document.createElement("ul");
  for (const p of vm.projects) {
    const row = document.createElement("li");

    const name = document.createElement("strong");
    name.textContent = p.name;
    row.appendChild(name);

    const root = document.createElement("span");
    root.textContent = " (" + p.root + ")";
    row.appendChild(root);

    const status = document.createElement("div");
    if (p.change_error) {
      status.textContent = "change status: " + p.change_error;
    } else {
      status.textContent =
        "branch " +
        p.branch +
        (p.active_change ? " · active change" : "") +
        (p.clean ? " · clean" : " · dirty");
    }
    row.appendChild(status);

    const sessions = document.createElement("div");
    const count = p.sessions ? p.sessions.length : 0;
    sessions.textContent = count + " session" + (count === 1 ? "" : "s");
    row.appendChild(sessions);

    list.appendChild(row);
  }
  container.appendChild(list);
}

function loadDashboard() {
  const container = document.getElementById("dashboard");
  if (!container) {
    return;
  }
  apiFetch("/api/dashboard")
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("GET /api/dashboard: " + resp.status);
      }
      return resp.json();
    })
    .then((vm) => renderDashboard(vm, container))
    .catch((err) => {
      container.textContent = "Failed to load dashboard: " + err.message;
    });
}

// renderSession writes a transcript view-model (transcriptViewModel's JSON
// shape) into the #session container via plain DOM writes — textContent
// only, never innerHTML with response data, matching renderDashboard's
// posture (transcript content is untrusted-ish local turn history).
function renderSession(vm, container) {
  container.textContent = "";

  const heading = document.createElement("h2");
  heading.textContent = "Session " + vm.session_id;
  container.appendChild(heading);

  if (!vm.entries || vm.entries.length === 0) {
    const empty = document.createElement("p");
    empty.textContent = "No turns yet.";
    container.appendChild(empty);
    return;
  }

  const list = document.createElement("ul");
  for (const e of vm.entries) {
    const row = document.createElement("li");

    const role = document.createElement("strong");
    role.textContent = e.role + ": ";
    row.appendChild(role);

    const content = document.createElement("span");
    content.textContent = e.content;
    row.appendChild(content);

    if (e.tool_calls && e.tool_calls.length > 0) {
      const tools = document.createElement("div");
      tools.textContent =
        "tools: " + e.tool_calls.map((tc) => tc.name).join(", ");
      row.appendChild(tools);
    }

    list.appendChild(row);
  }
  container.appendChild(list);
}

// loadSession no-ops when the #session container is absent (dashboard-only
// pages) or the ?project=/&session= query params aren't both present —
// this screen is reached by navigating with those params set, following
// authToken()'s ?token= precedent (no router/framework, per D9).
function loadSession() {
  const container = document.getElementById("session");
  const project = queryParam("project");
  const session = queryParam("session");
  if (!container || !project || !session) {
    return;
  }
  container.textContent = "Loading session…";
  apiFetch(
    "/api/projects/" +
      encodeURIComponent(project) +
      "/sessions/" +
      encodeURIComponent(session),
  )
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("GET session: " + resp.status);
      }
      return resp.json();
    })
    .then((vm) => renderSession(vm, container))
    .catch((err) => {
      container.textContent = "Failed to load session: " + err.message;
    });
}

loadDashboard();
loadSession();
