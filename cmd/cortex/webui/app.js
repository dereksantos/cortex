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
// cmd/cortex/webui_transcript.go) into the #session container.
//
// M5.3c3 — the session screen's input box: a text field + submit button,
// appended after the rendered transcript.
//
// M5.3c4 — live SSE progress: the input box now POSTs to
// /api/projects/{name}/sessions/{id}/turn/stream (serve_stream.go) and
// consumes the response via sse.js's streamSSE() helper, rendering
// "progress" frames into the status span as they arrive and re-rendering
// the transcript (loadSession() again) once the terminal "result" frame
// lands — same "full re-fetch-and-rerender, not a client-side entry
// append" posture M5.3c3 chose, just triggered by the SSE "result" event
// instead of a single JSON response.

// queryParam reads a single query-string parameter from the page URL — the
// same window.location.search source authToken()'s "?token=" precedent
// uses, generalized for "?project=<name>&session=<id>".
function queryParam(name) {
  return new URLSearchParams(window.location.search).get(name) || "";
}

// authToken resolves the bearer token the API surface requires. A plain
// browser navigation can't attach a custom Authorization header, so the
// token rides in as a "?token=" query param on the page URL (the value
// `cortex serve` prints and auto-opens). A tokened visit is REMEMBERED in
// localStorage so later plain localhost visits keep working until the
// token rotates (the Jupyter posture; see serve.go's resolveServeToken).
function authToken() {
  const fromQuery = queryParam("token");
  if (fromQuery) {
    try {
      localStorage.setItem("cortex.token", fromQuery);
    } catch (e) {
      /* storage unavailable: fall through to the query value */
    }
    return fromQuery;
  }
  try {
    return localStorage.getItem("cortex.token") || "";
  } catch (e) {
    return "";
  }
}

// renderNoTokenBanner explains the one recoverable setup state — no token
// anywhere — instead of letting every screen surface a raw 401.
function renderNoTokenBanner() {
  const banner = document.createElement("p");
  banner.className = "no-token-banner";
  banner.textContent =
    "No access token — open this page via the URL `cortex serve` prints " +
    "(it ends in ?token=…); the page remembers it afterwards.";
  document.body.prepend(banner);
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

// renderTurnInput appends a text field + submit button to the #session
// container for posting a new turn (M5.3c3). Kept separate from
// renderSession, which is driven purely by the transcript view-model and
// has no notion of producing new turns — renderSession clears and rebuilds
// the container on every load, so this is called AFTER it on each render
// rather than lived as static markup in index.html.
function renderTurnInput(container, project, session) {
  const form = document.createElement("form");
  form.id = "turn-form";

  const input = document.createElement("input");
  input.type = "text";
  input.id = "turn-input";
  input.placeholder = "Say something…";
  form.appendChild(input);

  const button = document.createElement("button");
  button.type = "submit";
  button.textContent = "Send";
  form.appendChild(button);

  const status = document.createElement("span");
  status.id = "turn-status";
  form.appendChild(status);

  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const text = input.value.trim();
    if (!text) {
      return;
    }
    status.textContent = "Sending…";
    button.disabled = true;
    fetch(
      "/api/projects/" +
        encodeURIComponent(project) +
        "/sessions/" +
        encodeURIComponent(session) +
        "/turn/stream",
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: "Bearer " + authToken(),
        },
        body: JSON.stringify({ input: text }),
      },
    )
      .then((resp) => {
        if (!resp.ok) {
          throw new Error("POST turn/stream: " + resp.status);
        }
        return streamSSE(resp, {
          progress: (payload) => {
            status.textContent = payload.line;
          },
          result: () => {
            input.value = "";
            loadSession();
          },
          error: (payload) => {
            status.textContent = "Failed: " + payload.error;
          },
        });
      })
      .catch((err) => {
        status.textContent = "Failed to send: " + err.message;
      })
      .finally(() => {
        button.disabled = false;
      });
  });

  container.appendChild(form);
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
    .then((vm) => {
      renderSession(vm, container);
      renderTurnInput(container, project, session);
    })
    .catch((err) => {
      container.textContent = "Failed to load session: " + err.message;
    });
}

if (!authToken()) {
  renderNoTokenBanner();
}
loadDashboard();
loadSession();\n