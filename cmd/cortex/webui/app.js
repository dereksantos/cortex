// Cortex web UI — fetch/render/SSE-append only (GOAL.md §3 P5). No
// framework, no build step (D9); rendering logic lives in the Go
// view-model builders (cmd/cortex/webui*.go) — this file just fetches JSON
// and writes it into the DOM using the design-system classes from app.css
// (adopted from the docs/cortex-web.md mockup). Screen switching
// (projects/landscape/loops/models, plus the session screen reached via
// ?project=&session=) is a query-param + class toggle via plain <a href>
// navigation, not a router.

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

// changeChip describes a project's git change/clean state for its card.
function changeChip(p) {
  const cls = p.change_error ? "bad" : p.active_change ? "amb" : p.clean ? "ok" : "mute";
  const text = p.change_error
    ? "change status: " + p.change_error
    : p.active_change
      ? "change open · " + p.branch
      : p.clean
        ? "clean · " + p.branch
        : p.branch + " · dirty";
  return el("span", { className: "chip " + cls, textContent: text });
}

// renderProjectCard builds one .pcard: name, root, change chip, session
// count + link, and a session list linking into the session screen.
function renderProjectCard(p) {
  const sessions = p.sessions || [];
  const top = el("div", { className: "pcard-top" }, [
    el("span", { className: "pname", textContent: p.name }),
    changeChip(p),
    el("span", { className: "path", textContent: p.root }),
  ]);

  const metaKids = [el("span", { textContent: sessions.length + " session" + (sessions.length === 1 ? "" : "s") })];
  if (sessions.length > 0) {
    metaKids.push(
      el("a", {
        className: "btn primary",
        href: "?project=" + encodeURIComponent(p.name) + "&session=" + encodeURIComponent(sessions[0].id),
        textContent: "Open session",
      }),
    );
  }
  const card = el("div", { className: "pcard" }, [top, el("div", { className: "pmeta" }, metaKids)]);

  if (sessions.length > 0) {
    const items = sessions.map((s) =>
      el("li", {}, [
        el("a", {
          href: "?project=" + encodeURIComponent(p.name) + "&session=" + encodeURIComponent(s.id),
          textContent: s.id,
        }),
        el("span", { className: "dim", textContent: " · " + s.messages + " msgs" }),
      ]),
    );
    card.appendChild(el("ul", { className: "psessions" }, items));
  }
  return card;
}

// renderDashboard writes a dashboard view-model (dashboardViewModel's JSON
// shape) into the #dashboard container via plain DOM writes — textContent
// only, never innerHTML with response data, since project names/paths are
// untrusted-ish local-filesystem strings.
function renderDashboard(vm, container) {
  container.textContent = "";
  if (!vm.projects || vm.projects.length === 0) {
    container.appendChild(el("p", { className: "empty-note", textContent: "No registered projects yet." }));
    return;
  }
  container.appendChild(el("div", { className: "cards" }, vm.projects.map(renderProjectCard)));
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

// renderEntry appends one transcript entry as a .msg bubble, plus a
// .toolfeed block naming any tool calls it carries.
function renderEntry(container, e) {
  const cls = e.role === "assistant" ? "agent" : e.role === "tool" ? "tool" : "user";
  container.appendChild(
    el("div", { className: "msg " + cls }, [
      el("div", { className: "who", textContent: e.role }),
      el("div", { className: "bubble", textContent: e.content }),
    ]),
  );
  if (e.tool_calls && e.tool_calls.length > 0) {
    const lines = e.tool_calls.map((tc) =>
      el("div", {}, [el("span", { className: "t", textContent: tc.name }), " " + tc.args]),
    );
    container.appendChild(el("div", { className: "toolfeed" }, lines));
  }
}

// renderSession writes a transcript view-model (transcriptViewModel's JSON
// shape) into the #session container via plain DOM writes — textContent
// only, never innerHTML with response data (transcript content is
// untrusted-ish local turn history).
function renderSession(vm, container) {
  container.textContent = "";
  container.appendChild(
    el("div", { className: "sess-head" }, [
      el("span", { className: "crumb" }, ["session ", el("b", { textContent: vm.session_id })]),
    ]),
  );
  if (!vm.entries || vm.entries.length === 0) {
    container.appendChild(el("p", { className: "empty-note", textContent: "No turns yet." }));
    return;
  }
  for (const e of vm.entries) {
    renderEntry(container, e);
  }
}

// renderTurnInput appends a .composer text field + submit button to the
// #session container for posting a new turn, called AFTER renderSession
// on every render since renderSession clears and rebuilds the container.
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
// ?project=/&session= query params aren't both present.
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
