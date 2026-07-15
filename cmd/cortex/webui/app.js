// Cortex web UI — fetch/render/SSE-append only (GOAL.md §3 P5). No
// framework, no build step (D9); rendering logic lives in the Go
// view-model builders (cmd/cortex/webui*.go) — this file just fetches JSON
// and writes it into the DOM using the design-system classes from app.css
// (adopted from the docs/cortex-web.md mockup). Screen switching
// (projects/landscape/loops/models, plus the session screen reached via
// ?project=&session=) is a query-param + class toggle via plain <a href>
// navigation, not a router.
//
// This file keeps the core el()/authToken()/apiFetch() helpers plus the
// dashboard and session fetch calls (loadDashboard/loadSession) — several
// structural tests read their endpoint URLs directly out of app.js's
// source, so those two fetches stay here. The turn composer
// (renderTurnInput, tested the same way) also stays here. Dashboard/session
// *rendering* (renderDashboard, renderSession, tool-result formatting) is
// split into dashboard.js/session.js to keep this file under the per-file
// JS size cap (webui_jscap_test.go) — both are loaded right after this file
// and, since renderDashboard/renderSession are only ever called from inside
// a fetch().then() callback (which can't run until every synchronous
// <script> tag — including dashboard.js/session.js — has already
// executed), the split is safe without a module system.

// el creates an element, assigns props, and appends children (strings
// become text nodes) — keeps the render functions below terse.
function el(tag, props, children) {
  const e = document.createElement(tag);
  if (props) {
    Object.assign(e, props);
  }
  for (const c of children || []) {
    e.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return e;
}

// queryParam reads a single query-string parameter from the page URL.
function queryParam(name) {
  return new URLSearchParams(window.location.search).get(name) || "";
}

// authToken resolves the bearer token: "?token=" on the page URL `cortex
// serve` prints, remembered in localStorage so later plain visits work.
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
  document.body.prepend(
    el("p", {
      className: "no-token-banner",
      textContent:
        "No access token — open this page via the URL `cortex serve` prints " +
        "(it ends in ?token=…); the page remembers it afterwards.",
    }),
  );
}

// apiFetch issues an authenticated GET against the given /api/... path.
function apiFetch(path) {
  return fetch(path, { headers: { Authorization: "Bearer " + authToken() } });
}

// activeScreen/showScreen are the entire "router": a query-param read plus
// a class toggle on the matching #screen-* section and nav link.
function activeScreen() {
  if (queryParam("project") && queryParam("session")) {
    return "session";
  }
  return queryParam("screen") || "projects";
}

function showScreen() {
  const active = activeScreen();
  document.querySelectorAll(".screen").forEach((s) => s.classList.toggle("active", s.dataset.screen === active));
  document.querySelectorAll("#nav a").forEach((a) => a.classList.toggle("on", a.dataset.screen === active));
}

// loadDashboard fetches GET /api/dashboard and hands the view-model to
// renderDashboard (dashboard.js).
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

// renderTurnInput appends a .composer text field + submit button to the
// #session container for posting a new turn, called AFTER renderSession
// (session.js) on every render since renderSession clears and rebuilds the
// container. Renders in normal document flow at the bottom of the
// transcript — no sticky positioning needed, the container just scrolls.
function renderTurnInput(container, project, session) {
  const form = document.createElement("form");
  form.className = "composer";
  form.id = "turn-form";

  const input = document.createElement("input");
  input.type = "text";
  input.id = "turn-input";
  input.placeholder = "Message cortex…";
  form.appendChild(input);

  const button = document.createElement("button");
  button.type = "submit";
  button.className = "btn primary";
  button.textContent = "Send";
  form.appendChild(button);

  const status = el("span", { className: "status-text", id: "turn-status" });
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
      "/api/projects/" + encodeURIComponent(project) + "/sessions/" + encodeURIComponent(session) + "/turn/stream",
      {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: "Bearer " + authToken() },
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

// loadSession no-ops when the #session container is absent or the
// ?project=/&session= query params aren't both present. Rendering
// (renderSession) lives in session.js.
function loadSession() {
  const container = document.getElementById("session");
  const project = queryParam("project");
  const session = queryParam("session");
  if (!container || !project || !session) {
    return;
  }
  container.textContent = "Loading session…";
  apiFetch("/api/projects/" + encodeURIComponent(project) + "/sessions/" + encodeURIComponent(session))
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
showScreen();
loadDashboard();
loadSession();
