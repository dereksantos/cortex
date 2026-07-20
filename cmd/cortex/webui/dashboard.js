// dashboard.js — the projects screen's rendering. Split out of app.js to
// keep app.js under the per-file JS size cap (webui_jscap_test.go);
// app.js's loadDashboard() owns the GET /api/dashboard fetch, handing the
// parsed dashboardViewModel to renderDashboard here. Reuses el() from
// app.js as a plain global — no module system, index.html loads app.js
// first.
//
// Two views share one view-model: cards (per-project card grid) and list
// (a compact table). The choice persists in localStorage under a
// "cortex.<setting>" key, same pattern as any other UI preference here.

const projectsViewKey = "cortex.projectsView";

function getProjectsView() {
  return localStorage.getItem(projectsViewKey) === "list" ? "list" : "cards";
}

// changeChip describes a project's git change/clean state. A missing root
// (p.missing, buildDashboardViewModel) renders one "path missing" chip
// instead of ever reaching git; a genuine git error title-carries its full
// text so CSS can ellipsize the chip without losing it.
function changeChip(p) {
  if (p.missing) {
    return el("span", { className: "chip bad", textContent: "path missing", title: p.root });
  }
  const cls = p.change_error ? "bad" : p.active_change ? "amb" : p.clean ? "ok" : "mute";
  const text = p.change_error
    ? "change status: " + p.change_error
    : p.active_change
      ? "change open · " + p.branch
      : p.clean
        ? "clean · " + p.branch
        : p.branch + " · dirty";
  const props = { className: "chip " + cls, textContent: text };
  if (p.change_error) props.title = p.change_error;
  return el("span", props);
}

// fmtSessionDate renders a sessionSummary's mod_time (RFC3339, per
// serve_routes.go) as a short locale date; falls back to the raw string.
function fmtSessionDate(iso) {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

// relativeAgo renders a past RFC3339 instant as a short "Nm/Nh/Nd ago".
function relativeAgo(iso) {
  const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return mins + "m ago";
  const hours = Math.round(mins / 60);
  return hours < 24 ? hours + "h ago" : Math.round(hours / 24) + "d ago";
}

// mostRecent returns the session with the latest mod_time, or null.
function mostRecent(sessions) {
  return (sessions || []).length === 0
    ? null
    : sessions.reduce((a, b) => (new Date(a.mod_time) > new Date(b.mod_time) ? a : b));
}

// newestNonEmpty returns a project's most recently touched non-empty
// session, or null — the session a navigation should land on.
function newestNonEmpty(sessions) {
  return mostRecent((sessions || []).filter((s) => s.messages > 0));
}

// sessionRow builds one two-line session row: first message on line 1,
// mono id/count/date meta on line 2.
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
// sessions" details body — id and date only.
function emptySessionRow(project, s) {
  return el("li", {}, [
    el("a", {
      className: "srow-empty mono",
      href: "?project=" + encodeURIComponent(project) + "&session=" + encodeURIComponent(s.id),
      textContent: s.id + " · " + fmtSessionDate(s.mod_time),
    }),
  ]);
}

// renderSessionList appends a card's session list: non-empty sessions
// render as sessionRow()s (at most `cap` inline, rest behind a "show all"
// <details>); empty sessions collapse behind one "N empty" <details>.
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

// openOrCreateSession is the "Open session" click's shared logic (cards
// button, list row, list button): jump straight to the newest non-empty
// session when one exists, else POST /api/projects/{name}/sessions.
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

// openSessionButton builds the cards view's "Open session" button — always
// spawns a fresh session (openOrCreateSession with no prior sessions), a
// deliberate "start clean" affordance distinct from the list view's row.
function openSessionButton(name) {
  const btn = el("button", { type: "button", className: "btn primary", textContent: "Open session" });
  btn.addEventListener("click", () => openOrCreateSession({ name, sessions: [] }, btn));
  return btn;
}

// renderProjectCard builds one .pcard: name, root, change chip, session
// count + "Open session" (hidden when the root is missing), and the
// session list capped at `cap` inline rows.
function renderProjectCard(p, cap) {
  const sessions = p.sessions || [];
  const top = el("div", { className: "pcard-top" }, [
    el("span", { className: "pname", textContent: p.name }),
    changeChip(p),
    el("span", { className: "path", title: p.root, textContent: p.root }),
  ]);

  const metaKids = [el("span", { textContent: sessions.length + " session" + (sessions.length === 1 ? "" : "s") })];
  if (!p.missing) metaKids.push(openSessionButton(p.name));
  const card = el("div", { className: "pcard" }, [top, el("div", { className: "pmeta" }, metaKids)]);
  renderSessionList(card, p, cap);
  return card;
}

// renderProjectListRow appends one compact <tr>: name, branch/clean chip,
// session count, last-activity, truncated path (full path in title —
// .plist's table-layout: fixed, app.css, so this truncation actually
// bites) and an Open session button. The whole row is clickable (same
// openOrCreateSession the button uses). A missing-root project gets no
// click handler and no button: nothing to open.
function renderProjectListRow(table, p) {
  const sessions = p.sessions || [];
  const last = mostRecent(sessions);

  const tr = el("tr", { className: "list-row" });
  tr.appendChild(el("td", {}, [el("span", { className: "mono strong list-name", textContent: p.name })]));
  tr.appendChild(el("td", {}, [changeChip(p)]));
  tr.appendChild(el("td", { className: "mono", textContent: String(sessions.length) }));
  tr.appendChild(el("td", { className: "mono dim", textContent: last ? relativeAgo(last.mod_time) : "—" }));
  tr.appendChild(el("td", { className: "path list-path", title: p.root, textContent: p.root }));

  if (p.missing) {
    tr.appendChild(el("td", {}));
    table.appendChild(tr);
    return;
  }
  tr.addEventListener("click", () => openOrCreateSession(p));
  const btn = el("button", { type: "button", className: "btn", textContent: "Open session" });
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openOrCreateSession(p, btn);
  });
  tr.appendChild(el("td", {}, [btn]));
  table.appendChild(tr);
}

// renderProjectsList writes the list view: one table.loops row per
// project, tagged .plist so app.css can give it its own table-layout:
// fixed + column widths without touching loops.js/models.js's shared
// table.loops. Fixed layout keeps the actions column from being pushed
// offscreen; .table-scroll (.toolfeed's pattern) is the fallback so an
// overflow scrolls the table, never the page body.
function renderProjectsList(container, projects) {
  const wrap = el("div", { className: "table-scroll" });
  const table = el(
    "table",
    { className: "loops" },
    [["name", "branch", "sessions", "last activity", "path", ""].map((h) => el("th", { textContent: h }))].map(
      (row) => el("tr", {}, row),
    ),
  );
  table.classList.add("plist");
  projects.forEach((p) => renderProjectListRow(table, p));
  wrap.appendChild(table);
  container.appendChild(wrap);
}

// renderViewSwitcher appends the cards/list .seg control, persisting the
// choice and re-rendering the already-fetched view-model immediately.
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

// renderDashboard writes a dashboard view-model into the #dashboard
// container via plain DOM writes — textContent only, never innerHTML with
// response data. Cards view lays out as a responsive grid once there's
// more than one project (a single project keeps the full-width card and
// an uncapped-at-8 session list rather than the grid's cap of 4).
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
