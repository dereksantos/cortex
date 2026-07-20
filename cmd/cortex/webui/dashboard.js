// dashboard.js — the projects screen's rendering. Split out of app.js to
// keep app.js under the per-file JS size cap (webui_jscap_test.go);
// app.js's loadDashboard() still owns the GET /api/dashboard fetch call
// (that endpoint string is asserted directly against app.js's source) and
// hands the parsed dashboardViewModel to renderDashboard, defined here.
// Reuses el() from app.js as a plain global function — no module system,
// index.html loads app.js before this file.
//
// Two views share one view-model: cards (the original per-project card
// grid) and list (a compact table, one row per project). The choice
// persists in localStorage so it survives a reload, same pattern as any
// other "cortex.<setting>" preference key this UI accrues.

const projectsViewKey = "cortex.projectsView";

function getProjectsView() {
  return localStorage.getItem(projectsViewKey) === "list" ? "list" : "cards";
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

// fmtSessionDate renders a sessionSummary's mod_time (an RFC3339 string, per
// serve_routes.go's sessionSummary) as a short locale date; falls back to
// the raw string if it doesn't parse.
function fmtSessionDate(iso) {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

// relativeAgo renders a past RFC3339 instant as a short "Nm/Nh/Nd ago" —
// same compact-glance idea as loops.js's relativeish, but for elapsed time.
function relativeAgo(iso) {
  const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return mins + "m ago";
  const hours = Math.round(mins / 60);
  return hours < 24 ? hours + "h ago" : Math.round(hours / 24) + "d ago";
}

// newestNonEmpty returns a project's most recently touched non-empty
// session, or null when it has none yet.
function newestNonEmpty(sessions) {
  const nonEmpty = (sessions || []).filter((s) => s.messages > 0);
  return nonEmpty.length === 0
    ? null
    : nonEmpty.reduce((a, b) => (new Date(a.mod_time) > new Date(b.mod_time) ? a : b));
}

// sessionRow builds one two-line session row for a project card: the
// session's first message (truncated) on line 1, mono id/count/date meta
// on line 2 — replaces the old flat "id · N msgs" line so a 50-session
// project reads as content, not a wall of near-identical rows.
function sessionRow(project, s) {
  const first = (s.first || "(no messages)").slice(0, 90);
  return el("li", {}, [
    el(
      "a",
      {
        className: "srow",
        href: "?project=" + encodeURIComponent(project) + "&session=" + encodeURIComponent(s.id),
      },
      [
        el("div", { className: "srow-first", textContent: first }),
        el("div", {
          className: "srow-meta mono",
          textContent: s.id + " · " + s.messages + " msgs · " + fmtSessionDate(s.mod_time),
        }),
      ],
    ),
  ]);
}

// emptySessionRow builds one compact row for the collapsed "N empty
// sessions" details body — id and date only, since there's no first
// message to show.
function emptySessionRow(project, s) {
  return el("li", {}, [
    el("a", {
      className: "srow-empty mono",
      href: "?project=" + encodeURIComponent(project) + "&session=" + encodeURIComponent(s.id),
      textContent: s.id + " · " + fmtSessionDate(s.mod_time),
    }),
  ]);
}

// renderSessionList appends a project card's session list: non-empty
// sessions render as sessionRow()s (at most `cap` shown inline — 8 in the
// single-project layout, 4 in the multi-project grid — the rest behind a
// "show all N sessions" <details>); empty (0-message) sessions collapse
// behind one "N empty sessions" <details> line instead of padding out the
// list with near-useless rows.
function renderSessionList(card, p, cap) {
  const sessions = p.sessions || [];
  const nonEmpty = sessions.filter((s) => s.messages > 0);
  const empty = sessions.filter((s) => s.messages === 0);

  if (nonEmpty.length > 0) {
    const shown = nonEmpty.slice(0, cap);
    const rest = nonEmpty.slice(cap);
    card.appendChild(el("ul", { className: "psessions" }, shown.map((s) => sessionRow(p.name, s))));
    if (rest.length > 0) {
      card.appendChild(
        el("details", { className: "more-sessions" }, [
          el("summary", { className: "mono dim", textContent: "show all " + nonEmpty.length + " sessions" }),
          el("ul", { className: "psessions" }, rest.map((s) => sessionRow(p.name, s))),
        ]),
      );
    }
  }
  if (empty.length > 0) {
    card.appendChild(
      el("details", { className: "empty-sessions" }, [
        el("summary", { className: "mono dim", textContent: empty.length + " empty sessions" }),
        el("ul", { className: "psessions" }, empty.map((s) => emptySessionRow(p.name, s))),
      ]),
    );
  }
}

// openOrCreateSession is the "Open session" click's shared logic. Cards
// view (openSessionButton below) always spawns a fresh session — a
// deliberate "start clean" affordance. The list view's row/button use this
// instead: jump straight to the newest non-empty session when one exists
// (no round trip — the dashboard view-model already has it) and only POST
// /api/projects/{name}/sessions to create one when the project has none.
function openOrCreateSession(p, btn) {
  const latest = newestNonEmpty(p.sessions);
  if (latest) {
    window.location.href = "?project=" + encodeURIComponent(p.name) + "&session=" + encodeURIComponent(latest.id);
    return;
  }
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Opening…";
  }
  fetch("/api/projects/" + encodeURIComponent(p.name) + "/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  })
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("POST create session: " + resp.status);
      }
      return resp.json();
    })
    .then((created) => {
      window.location.href = "?project=" + encodeURIComponent(p.name) + "&session=" + encodeURIComponent(created.id);
    })
    .catch((err) => {
      if (btn) {
        btn.disabled = false;
        btn.textContent = "Open session";
        btn.title = "Failed: " + err.message;
      }
    });
}

// openSessionButton builds the cards view's "Open session" button: POSTs
// /api/projects/{name}/sessions (no "resume" body → handleCreateSession
// starts a brand-new live session, serve_routes.go) and navigates to its
// session view on success. Disabled with "Opening…" while the request is
// in flight; a failure re-enables it with the error surfaced via title.
function openSessionButton(name) {
  const btn = el("button", { type: "button", className: "btn primary", textContent: "Open session" });
  btn.addEventListener("click", () => openOrCreateSession({ name, sessions: [] }, btn));
  return btn;
}

// renderProjectCard builds one .pcard: name, root, change chip, session
// count + the "Open session" button, and the session list capped at `cap`
// inline rows.
function renderProjectCard(p, cap) {
  const sessions = p.sessions || [];
  const top = el("div", { className: "pcard-top" }, [
    el("span", { className: "pname", textContent: p.name }),
    changeChip(p),
    el("span", { className: "path", textContent: p.root }),
  ]);

  const metaKids = [
    el("span", { textContent: sessions.length + " session" + (sessions.length === 1 ? "" : "s") }),
    openSessionButton(p.name),
  ];
  const card = el("div", { className: "pcard" }, [top, el("div", { className: "pmeta" }, metaKids)]);
  renderSessionList(card, p, cap);
  return card;
}

// renderProjectListRow appends one compact <tr> for a project: name (links
// to its newest session when one exists), branch/clean chip, session
// count, last-activity, truncated path (full path in title), and an Open
// session button. The whole row is clickable — same openOrCreateSession
// the button uses — so a click anywhere lands on the same place the name
// or the button would.
function renderProjectListRow(table, p) {
  const sessions = p.sessions || [];
  const last = newestNonEmpty(sessions) ? sessions.reduce((a, b) => (a.mod_time > b.mod_time ? a : b)) : null;

  const tr = el("tr", { className: "list-row" });
  tr.addEventListener("click", () => openOrCreateSession(p));

  const btn = el("button", { type: "button", className: "btn", textContent: "Open session" });
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openOrCreateSession(p, btn);
  });

  tr.appendChild(el("td", {}, [el("span", { className: "mono strong list-name", textContent: p.name })]));
  tr.appendChild(el("td", {}, [changeChip(p)]));
  tr.appendChild(el("td", { className: "mono", textContent: String(sessions.length) }));
  tr.appendChild(el("td", { className: "mono dim", textContent: last ? relativeAgo(last.mod_time) : "—" }));
  tr.appendChild(el("td", { className: "path list-path", title: p.root, textContent: p.root }));
  tr.appendChild(el("td", {}, [btn]));
  table.appendChild(tr);
}

// renderProjectsList writes the list view: one table.loops row per project,
// no session sublists (that detail lives behind the row/button click).
function renderProjectsList(container, projects) {
  const table = el(
    "table",
    { className: "loops" },
    [["name", "branch", "sessions", "last activity", "path", ""].map((h) => el("th", { textContent: h }))].map(
      (row) => el("tr", {}, row),
    ),
  );
  projects.forEach((p) => renderProjectListRow(table, p));
  container.appendChild(table);
}

// renderViewSwitcher appends the cards/list .seg control, persisting the
// choice to localStorage and re-rendering the already-fetched view-model
// immediately — no re-fetch needed to flip views.
function renderViewSwitcher(container, vm) {
  const current = getProjectsView();
  const seg = el(
    "div",
    { className: "seg" },
    ["cards", "list"].map((view) => {
      const btn = el("button", { type: "button", className: view === current ? "on" : "", textContent: view });
      btn.addEventListener("click", () => {
        localStorage.setItem(projectsViewKey, view);
        renderDashboard(vm, container);
      });
      return btn;
    }),
  );
  container.appendChild(el("div", { className: "projects-toolbar" }, [seg]));
}

// renderDashboard writes a dashboard view-model (dashboardViewModel's JSON
// shape) into the #dashboard container via plain DOM writes — textContent
// only, never innerHTML with response data, since project names/paths are
// untrusted-ish local-filesystem strings. Cards view lays out as a
// responsive grid once there's more than one project (a single project
// keeps the roomier full-width card, and its session list stays uncapped
// at 8 rather than the grid's 4).
function renderDashboard(vm, container) {
  container.textContent = "";
  if (!vm.projects || vm.projects.length === 0) {
    container.appendChild(el("p", { className: "empty-note", textContent: "No registered projects yet." }));
    return;
  }
  renderViewSwitcher(container, vm);
  if (getProjectsView() === "list") {
    renderProjectsList(container, vm.projects);
    return;
  }
  const multi = vm.projects.length > 1;
  const cardsClass = "cards" + (multi ? " grid" : "");
  const cap = multi ? 4 : 8;
  container.appendChild(el("div", { className: cardsClass }, vm.projects.map((p) => renderProjectCard(p, cap))));
}
